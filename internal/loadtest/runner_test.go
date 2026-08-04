package loadtest

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerCountsOnlyCreatedHTTP2ResponsesAsSuccessfulTPS(t *testing.T) {
	var factoryCalls atomic.Int64
	var closeCalls atomic.Int64
	var requestCalls atomic.Uint64
	factory := func() (http.RoundTripper, func()) {
		factoryCalls.Add(1)
		return roundTripFunc(func(*http.Request) (*http.Response, error) {
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
	cfg.Duration = 30 * time.Millisecond
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
	if result.TotalRequests == 0 || result.SuccessfulRequests == 0 || result.FailedRequests == 0 {
		t.Fatalf("request counts = total:%d success:%d failed:%d, want all non-zero",
			result.TotalRequests, result.SuccessfulRequests, result.FailedRequests)
	}
	if result.SuccessfulRequests != result.StatusCodes["201"] {
		t.Errorf("successful requests = %d, 201 responses = %d",
			result.SuccessfulRequests, result.StatusCodes["201"])
	}
	if result.SuccessfulRequests+result.FailedRequests != result.TotalRequests {
		t.Error("successful and failed counts do not equal total requests")
	}
	wantTPS := float64(result.SuccessfulRequests) / result.DurationSeconds
	if result.SuccessfulTPS != wantTPS {
		t.Errorf("successful TPS = %f, want %f", result.SuccessfulTPS, wantTPS)
	}
	if result.ConcurrentStreams != cfg.Connections*cfg.StreamsPerConnection {
		t.Errorf("concurrent streams = %d, want %d",
			result.ConcurrentStreams, cfg.Connections*cfg.StreamsPerConnection)
	}
}

func TestAggregateCalculatesNearestRankLatencyPercentiles(t *testing.T) {
	workers := make(chan workerResult, 1)
	latencies := make([]time.Duration, 100)
	for index := range latencies {
		latencies[index] = time.Duration(index+1) * time.Millisecond
	}
	workers <- workerResult{
		total:       100,
		successful:  100,
		latencies:   latencies,
		statusCodes: map[string]uint64{"201": 100},
	}
	close(workers)

	result := aggregate(validConfig(), 2*time.Second, workers)
	if result.SuccessfulTPS != 50 {
		t.Errorf("successful TPS = %f, want 50", result.SuccessfulTPS)
	}
	if result.LatencyP50Millis != 50 || result.LatencyP95Millis != 95 || result.LatencyP99Millis != 99 {
		t.Errorf("latencies p50=%f p95=%f p99=%f, want 50/95/99ms",
			result.LatencyP50Millis, result.LatencyP95Millis, result.LatencyP99Millis)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "target", mutate: func(cfg *Config) { cfg.Target = "https://gateway/session" }},
		{name: "duration", mutate: func(cfg *Config) { cfg.Duration = 0 }},
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

func TestNewRunnerRejectsNilFactoryAndRunRejectsNilContext(t *testing.T) {
	if _, err := NewRunner(nil); err == nil {
		t.Fatal("NewRunner(nil) error = nil, want validation error")
	}
	runner, err := NewRunner(func() (http.RoundTripper, func()) {
		return roundTripFunc(func(*http.Request) (*http.Response, error) {
			return loadTestResponse(http.StatusCreated, 2), nil
		}), func() {}
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if _, err := runner.Run(nil, validConfig()); err == nil {
		t.Fatal("Run(nil) error = nil, want validation error")
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
		Target:               "http://gateway/nsmf-pdusession/v1/sm-contexts",
		Duration:             time.Second,
		Connections:          1,
		StreamsPerConnection: 1,
		RequestTimeout:       time.Second,
	}
}
