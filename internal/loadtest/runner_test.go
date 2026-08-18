package loadtest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerCountsWrittenRequestsAndSuccessfulResponsesWithinWindow(t *testing.T) {
	var factoryCalls atomic.Int64
	var closeCalls atomic.Int64
	var requestCalls atomic.Uint64
	factory := func() (http.RoundTripper, func()) {
		factoryCalls.Add(1)
		return roundTripFunc(func(request *http.Request) (*http.Response, error) {
			markRequestWritten(request, nil)
			status := http.StatusCreated
			if requestCalls.Add(1)%3 == 0 {
				status = http.StatusServiceUnavailable
			}
			return loadTestResponse(status, 2), nil
		}), func() { closeCalls.Add(1) }
	}
	runner, err := NewRunner(factory)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	cfg := validConfig()
	cfg.Duration = 300 * time.Millisecond
	cfg.RequestCount = 60
	cfg.MinimumSuccessfulRequests = 40
	cfg.Connections = 2
	cfg.StreamsPerConnection = 3

	result, err := runner.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if factoryCalls.Load() != int64(cfg.Connections) || closeCalls.Load() != int64(cfg.Connections) {
		t.Errorf("transport lifecycle create=%d close=%d, want %d each",
			factoryCalls.Load(), closeCalls.Load(), cfg.Connections)
	}
	if result.TotalRequests != 60 || result.SentRequests != 60 ||
		result.SuccessfulRequests != 40 || result.FailedRequests != 20 {
		t.Fatalf("counts = attempts:%d sent:%d success:%d failed:%d, want 60/60/40/20",
			result.TotalRequests, result.SentRequests, result.SuccessfulRequests, result.FailedRequests)
	}
	if result.StatusCodes["201"] != 40 || result.StatusCodes["503"] != 20 {
		t.Errorf("status codes = %#v, want 201:40 and 503:20", result.StatusCodes)
	}
	if result.Errors["http_status"] != 20 {
		t.Errorf("HTTP status errors = %d, want 20", result.Errors["http_status"])
	}
	wantTPS := float64(40) / cfg.Duration.Seconds()
	if result.SuccessfulTPS != wantTPS {
		t.Errorf("successful TPS = %f, want %f", result.SuccessfulTPS, wantTPS)
	}
	if result.ConcurrentStreams != 6 {
		t.Errorf("concurrent streams = %d, want fixed worker count 6", result.ConcurrentStreams)
	}
	if result.TargetRequests != 60 || result.TargetSuccessfulRequests != 40 {
		t.Errorf("targets = requests:%d successes:%d, want 60/40",
			result.TargetRequests, result.TargetSuccessfulRequests)
	}
	if !result.TargetMet {
		t.Error("target met = false, want true at the configured 40-success threshold")
	}
}

func TestRunnerSchedulesAttemptsAcrossConfiguredDuration(t *testing.T) {
	writes := make(chan time.Time, 5)
	runner := mustRunner(t, func(request *http.Request) (*http.Response, error) {
		markRequestWritten(request, nil)
		writes <- time.Now()
		return loadTestResponse(http.StatusCreated, 2), nil
	})
	cfg := validConfig()
	cfg.RequestCount = 5
	cfg.MinimumSuccessfulRequests = 5
	cfg.Duration = 100 * time.Millisecond
	cfg.StreamsPerConnection = 1

	result, err := runner.Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	close(writes)
	var observed []time.Time
	for wroteAt := range writes {
		observed = append(observed, wroteAt)
	}
	if len(observed) != 5 {
		t.Fatalf("write count = %d, want 5", len(observed))
	}
	span := observed[len(observed)-1].Sub(observed[0])
	if span < 60*time.Millisecond {
		t.Fatalf("write span = %s, want attempts paced across the 100ms window", span)
	}
	if result.DispatchDurationSeconds < 0.060 || result.DispatchDurationSeconds > 0.100 {
		t.Errorf("dispatch duration = %.6fs, want last successful write inside paced window",
			result.DispatchDurationSeconds)
	}
	if !result.TargetMet {
		t.Fatalf("target met = false, result = %+v", result)
	}
}

func TestRunnerUsesFixedWorkerPoolWhenRequestsExceedCapacity(t *testing.T) {
	var active atomic.Int64
	var maximum atomic.Int64
	var factoryCalls atomic.Int64
	var closeCalls atomic.Int64
	runner, err := NewRunner(func() (http.RoundTripper, func()) {
		factoryCalls.Add(1)
		return roundTripFunc(func(request *http.Request) (*http.Response, error) {
			current := active.Add(1)
			defer active.Add(-1)
			updateMaximum(&maximum, current)
			markRequestWritten(request, nil)
			time.Sleep(8 * time.Millisecond)
			return loadTestResponse(http.StatusCreated, 2), nil
		}), func() { closeCalls.Add(1) }
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := validConfig()
	cfg.RequestCount = 20
	cfg.MinimumSuccessfulRequests = 1
	cfg.Duration = 100 * time.Millisecond
	cfg.Connections = 2
	cfg.StreamsPerConnection = 2

	result, err := runner.Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if maximum.Load() > 4 {
		t.Errorf("maximum active requests = %d, want at most 4 workers", maximum.Load())
	}
	if result.ConcurrentStreams != 4 {
		t.Errorf("concurrent streams = %d, want 4", result.ConcurrentStreams)
	}
	if result.TotalRequests != cfg.RequestCount {
		t.Errorf("attempts = %d, want %d even though capacity is smaller",
			result.TotalRequests, cfg.RequestCount)
	}
	if factoryCalls.Load() != 2 || closeCalls.Load() != 2 {
		t.Errorf("transport lifecycle create=%d close=%d, want 2 each",
			factoryCalls.Load(), closeCalls.Load())
	}
}

func TestRunnerWarmsEveryConfiguredConnectionBeforeMeasurement(t *testing.T) {
	var warmups atomic.Int64
	var measured atomic.Int64
	runner, err := NewRunner(func() (http.RoundTripper, func()) {
		return roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("X-Loadtest-Warmup") == "true" {
				warmups.Add(1)
				return loadTestResponse(http.StatusCreated, 2), nil
			}
			measured.Add(1)
			markRequestWritten(request, nil)
			return loadTestResponse(http.StatusCreated, 2), nil
		}), func() {}
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := validConfig()
	cfg.RequestCount = 2
	cfg.MinimumSuccessfulRequests = 2
	cfg.Duration = 50 * time.Millisecond
	cfg.Connections = 3
	cfg.StreamsPerConnection = 1
	cfg.WarmupConnections = true

	result, err := runner.Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := warmups.Load(); got != int64(cfg.Connections) {
		t.Errorf("warmup calls = %d, want %d", got, cfg.Connections)
	}
	if got := measured.Load(); got != int64(cfg.RequestCount) {
		t.Errorf("measured calls = %d, want %d", got, cfg.RequestCount)
	}
	if !result.TargetMet {
		t.Fatalf("target met = false, result = %+v", result)
	}
}

func TestRunnerDoesNotCountResponseCompletedAfterMeasurementWindow(t *testing.T) {
	runner := mustRunner(t, func(request *http.Request) (*http.Response, error) {
		markRequestWritten(request, nil)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	cfg := validConfig()
	cfg.RequestCount = 1
	cfg.MinimumSuccessfulRequests = 1
	cfg.Duration = 20 * time.Millisecond
	cfg.RequestTimeout = time.Second

	result, err := runner.Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalRequests != 1 || result.SentRequests != 1 ||
		result.SuccessfulRequests != 0 || result.FailedRequests != 1 {
		t.Fatalf("counts = attempts:%d sent:%d success:%d failed:%d, want 1/1/0/1",
			result.TotalRequests, result.SentRequests, result.SuccessfulRequests, result.FailedRequests)
	}
	if result.Errors["timeout"] != 1 || result.TargetMet {
		t.Errorf("errors/target = %#v/%t, want timeout and failed target", result.Errors, result.TargetMet)
	}
	if result.LatencyP50Millis != 0 || result.LatencyP95Millis != 0 || result.LatencyP99Millis != 0 {
		t.Errorf("late request contributed latency: p50/p95/p99=%f/%f/%f",
			result.LatencyP50Millis, result.LatencyP95Millis, result.LatencyP99Millis)
	}
}

func TestRunnerRequiresSuccessfulWroteRequestForSentCount(t *testing.T) {
	runner := mustRunner(t, func(request *http.Request) (*http.Response, error) {
		markRequestWritten(request, errors.New("write failed"))
		return loadTestResponse(http.StatusCreated, 2), nil
	})
	cfg := validConfig()
	cfg.RequestCount = 2
	cfg.MinimumSuccessfulRequests = 1
	cfg.Duration = 20 * time.Millisecond

	result, err := runner.Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.SentRequests != 0 {
		t.Errorf("sent requests = %d, want 0 for failed WroteRequest traces", result.SentRequests)
	}
	if result.SuccessfulRequests != 2 {
		t.Errorf("successful responses = %d, want 2", result.SuccessfulRequests)
	}
	if result.TargetMet {
		t.Error("target met = true, want false when requests were not confirmed written")
	}
}

func TestAggregateUsesConfiguredWindowForTPSAndSuccessfulLatencies(t *testing.T) {
	requests := make(chan requestResult, 100)
	for index := range 100 {
		requests <- requestResult{
			status:  http.StatusCreated,
			latency: time.Duration(index+1) * time.Millisecond,
			sent:    true,
			sentAt:  time.Duration(index+1) * time.Millisecond,
		}
	}
	close(requests)
	cfg := validConfig()
	cfg.Duration = 2 * time.Second

	result := aggregate(cfg, 10, 3*time.Second, requests)
	if result.SuccessfulTPS != 50 {
		t.Errorf("successful TPS = %f, want 50 from 100 successes / configured 2s", result.SuccessfulTPS)
	}
	if result.LatencyP50Millis != 50 || result.LatencyP95Millis != 95 || result.LatencyP99Millis != 99 {
		t.Errorf("latencies p50=%f p95=%f p99=%f, want 50/95/99ms",
			result.LatencyP50Millis, result.LatencyP95Millis, result.LatencyP99Millis)
	}
	if result.DispatchDurationSeconds != 0.1 {
		t.Errorf("dispatch duration = %f, want 0.1s", result.DispatchDurationSeconds)
	}
}

func TestRunnerReturnsPartialResultWhenCallerCancels(t *testing.T) {
	runner := mustRunner(t, func(request *http.Request) (*http.Response, error) {
		markRequestWritten(request, nil)
		return loadTestResponse(http.StatusCreated, 2), nil
	})
	cfg := validConfig()
	cfg.RequestCount = 100
	cfg.MinimumSuccessfulRequests = 50
	cfg.Duration = time.Second
	cfg.StreamsPerConnection = 2
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()

	result, err := runner.Run(ctx, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want caller deadline exceeded", err)
	}
	if result.TotalRequests == 0 || result.TotalRequests >= cfg.RequestCount {
		t.Errorf("partial attempts = %d, want between 1 and %d", result.TotalRequests, cfg.RequestCount-1)
	}
	if result.TargetMet {
		t.Error("partial canceled run unexpectedly met target")
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "target", mutate: func(cfg *Config) { cfg.Target = "https://gateway/session" }},
		{name: "duration", mutate: func(cfg *Config) { cfg.Duration = 0 }},
		{name: "request count", mutate: func(cfg *Config) { cfg.RequestCount = 0 }},
		{name: "minimum successful zero", mutate: func(cfg *Config) { cfg.MinimumSuccessfulRequests = 0 }},
		{name: "minimum successful above request count", mutate: func(cfg *Config) { cfg.MinimumSuccessfulRequests = cfg.RequestCount + 1 }},
		{name: "connections", mutate: func(cfg *Config) { cfg.Connections = 0 }},
		{name: "streams", mutate: func(cfg *Config) { cfg.StreamsPerConnection = 0 }},
		{name: "request timeout", mutate: func(cfg *Config) { cfg.RequestTimeout = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want validation error")
			}
		})
	}

	cfg := validConfig()
	cfg.RequestCount = 1000
	cfg.MinimumSuccessfulRequests = 800
	cfg.Connections = 1
	cfg.StreamsPerConnection = 2
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected fixed worker capacity below request count: %v", err)
	}
}

func TestNewH2CTransportUsesOneReusableConnection(t *testing.T) {
	roundTripper, closeTransport := NewH2CTransport()
	t.Cleanup(closeTransport)
	transport, ok := roundTripper.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", roundTripper)
	}
	if transport.Protocols == nil || !transport.Protocols.UnencryptedHTTP2() ||
		transport.Protocols.HTTP1() || transport.Protocols.HTTP2() {
		t.Errorf("protocols = %+v, want h2c only", transport.Protocols)
	}
	if transport.MaxConnsPerHost != 1 || transport.MaxIdleConnsPerHost != 1 {
		t.Errorf("connection limits max=%d idle=%d, want 1/1",
			transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost)
	}
}

func TestNewRunnerRejectsNilDependencies(t *testing.T) {
	if _, err := NewRunner(nil); err == nil {
		t.Fatal("NewRunner(nil) error = nil, want validation error")
	}
	runner := mustRunner(t, func(request *http.Request) (*http.Response, error) {
		markRequestWritten(request, nil)
		return loadTestResponse(http.StatusCreated, 2), nil
	})
	if _, err := runner.Run(nil, validConfig()); err == nil {
		t.Fatal("Run(nil) error = nil, want validation error")
	}

	badRunner, err := NewRunner(func() (http.RoundTripper, func()) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badRunner.Run(context.Background(), validConfig()); err == nil {
		t.Fatal("Run() with nil transport dependencies error = nil, want error")
	}
}

func mustRunner(t *testing.T, roundTrip roundTripFunc) *Runner {
	t.Helper()
	runner, err := NewRunner(func() (http.RoundTripper, func()) { return roundTrip, func() {} })
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func markRequestWritten(request *http.Request, err error) {
	trace := httptrace.ContextClientTrace(request.Context())
	if trace != nil && trace.WroteRequest != nil {
		trace.WroteRequest(httptrace.WroteRequestInfo{Err: err})
	}
}

func updateMaximum(maximum *atomic.Int64, candidate int64) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func loadTestResponse(status, protocolMajor int) *http.Response {
	return &http.Response{
		StatusCode: status,
		ProtoMajor: protocolMajor,
		Proto:      "HTTP/2.0",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"status":"ACTIVE"}`)),
	}
}

func validConfig() Config {
	return Config{
		Target:                    "http://gateway/nsmf-pdusession/v1/sm-contexts",
		RequestCount:              100,
		MinimumSuccessfulRequests: 80,
		Duration:                  time.Second,
		Connections:               1,
		StreamsPerConnection:      10,
		RequestTimeout:            time.Second,
	}
}
