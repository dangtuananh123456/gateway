package discovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/registry"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestCollectorPublishesOnlyMatchingHealthAndMetrics(t *testing.T) {
	candidates := registry.New()
	address := addCollectorCandidate(t, candidates, "10.0.0.1:8081")
	collector := newTestCollector(t, candidates, roundTripFunc(successfulProbe))
	observedAt := discoveryTestTime().Add(time.Minute)
	collector.now = func() time.Time { return observedAt }

	if err := collector.CollectHealth(context.Background()); err != nil {
		t.Fatalf("CollectHealth() error = %v", err)
	}
	if got := candidates.HealthySnapshot().Len(); got != 0 {
		t.Fatalf("snapshot after health only = %d, want 0", got)
	}
	if err := collector.CollectMetrics(context.Background()); err != nil {
		t.Fatalf("CollectMetrics() error = %v", err)
	}

	snapshot := candidates.HealthySnapshot()
	instance, found := snapshot.At(0)
	if snapshot.Len() != 1 || !found {
		t.Fatalf("snapshot length = %d, want 1", snapshot.Len())
	}
	if instance.Address != address || instance.InstanceID != "pdu-session-1" ||
		instance.Weight != 3 || instance.ActiveRequests != 7 {
		t.Errorf("instance = %+v, want collected identity, weight, and load", instance)
	}
	candidate, _ := candidates.Get(address)
	if !candidate.LastHealthSuccessAt.Equal(observedAt) ||
		!candidate.LastMetricsSuccessAt.Equal(observedAt) ||
		!candidate.LastSuccessAt.Equal(observedAt) {
		t.Errorf("success timestamps = %+v, want %s", candidate, observedAt)
	}
}

func TestCollectorRemovesFailedCandidateAndRestoresIt(t *testing.T) {
	candidates := registry.New()
	addCollectorCandidate(t, candidates, "10.0.0.1:8081")
	var failing atomic.Bool
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if failing.Load() && request.URL.Path == constants.HealthPath {
			return probeResponse(http.StatusServiceUnavailable, `{}`), nil
		}
		return successfulProbe(request)
	})
	collector := newTestCollector(t, candidates, transport)

	collectBoth(t, collector)
	if got := candidates.HealthySnapshot().Len(); got != 1 {
		t.Fatalf("initial snapshot length = %d, want 1", got)
	}
	failing.Store(true)
	for attempt := 1; attempt <= constants.DiscoveryHealthFailureThreshold; attempt++ {
		if err := collector.CollectHealth(context.Background()); err == nil {
			t.Fatalf("failed CollectHealth() attempt %d error = nil, want error", attempt)
		}
		want := 1
		if attempt == constants.DiscoveryHealthFailureThreshold {
			want = 0
		}
		if got := candidates.HealthySnapshot().Len(); got != want {
			t.Fatalf("snapshot after health failure %d = %d, want %d", attempt, got, want)
		}
	}
	failing.Store(false)
	collectBoth(t, collector)
	if got := candidates.HealthySnapshot().Len(); got != 1 {
		t.Errorf("snapshot after recovery = %d, want 1", got)
	}
}

func TestCollectorMetricsFailureKeepsLastHealthySnapshot(t *testing.T) {
	candidates := registry.New()
	addCollectorCandidate(t, candidates, "10.0.0.1:8081")
	var failMetrics atomic.Bool
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if failMetrics.Load() && request.URL.Path == constants.MetricsPath {
			return nil, context.DeadlineExceeded
		}
		return successfulProbe(request)
	})
	collector := newTestCollector(t, candidates, transport)

	collectBoth(t, collector)
	failMetrics.Store(true)
	if err := collector.CollectMetrics(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CollectMetrics() error = %v, want deadline exceeded", err)
	}

	if got := candidates.HealthySnapshot().Len(); got != 1 {
		t.Fatalf("snapshot after metrics failure = %d, want last healthy backend retained", got)
	}
}

func TestCollectorHealthSuccessResetsConsecutiveFailureCount(t *testing.T) {
	candidates := registry.New()
	addCollectorCandidate(t, candidates, "10.0.0.1:8081")
	var failing atomic.Bool
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if failing.Load() && request.URL.Path == constants.HealthPath {
			return nil, context.DeadlineExceeded
		}
		return successfulProbe(request)
	})
	collector := newTestCollector(t, candidates, transport)
	collectBoth(t, collector)

	failing.Store(true)
	for range constants.DiscoveryHealthFailureThreshold - 1 {
		_ = collector.CollectHealth(context.Background())
	}
	failing.Store(false)
	if err := collector.CollectHealth(context.Background()); err != nil {
		t.Fatalf("recovery CollectHealth() error = %v", err)
	}
	failing.Store(true)
	for range constants.DiscoveryHealthFailureThreshold - 1 {
		_ = collector.CollectHealth(context.Background())
	}

	if got := candidates.HealthySnapshot().Len(); got != 1 {
		t.Fatalf("snapshot after non-consecutive failures = %d, want 1", got)
	}
}

func TestCollectorRetriesUnhealthyTransitionAfterThreshold(t *testing.T) {
	collector := &Collector{failures: make(map[netip.AddrPort]int)}
	address := netip.MustParseAddrPort("10.0.0.1:8081")
	for attempt := 1; attempt <= constants.DiscoveryHealthFailureThreshold+1; attempt++ {
		got := collector.reachedHealthFailureThreshold(address)
		want := attempt >= constants.DiscoveryHealthFailureThreshold
		if got != want {
			t.Errorf("threshold attempt %d = %t, want %t", attempt, got, want)
		}
	}
}

func TestCollectorSlowCandidateDoesNotDelayFastCandidate(t *testing.T) {
	candidates := registry.New()
	addCollectorCandidate(t, candidates, "10.0.0.1:8081")
	fastAddress := addCollectorCandidate(t, candidates, "10.0.0.2:8081")
	fastCompleted := make(chan struct{})
	var signalOnce sync.Once
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "10.0.0.1:8081" {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		signalOnce.Do(func() { close(fastCompleted) })
		return probeResponse(http.StatusOK, `{"instanceId":"pdu-fast","status":"UP"}`), nil
	})
	collector := newTestCollector(t, candidates, transport)
	collector.config.HealthTimeout = 100 * time.Millisecond

	roundDone := make(chan error, 1)
	go func() { roundDone <- collector.CollectHealth(context.Background()) }()
	select {
	case <-fastCompleted:
	case <-time.After(time.Second):
		t.Fatal("fast candidate was not probed")
	}
	deadline := time.Now().Add(50 * time.Millisecond)
	fastCandidate, _ := candidates.Get(fastAddress)
	for !fastCandidate.Healthy && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		fastCandidate, _ = candidates.Get(fastAddress)
	}
	if !fastCandidate.Healthy {
		t.Fatal("fast candidate was not updated while slow probe was pending")
	}
	select {
	case <-roundDone:
		t.Fatal("round completed before slow candidate timeout")
	default:
	}
	if err := <-roundDone; err == nil {
		t.Fatal("round error = nil, want slow probe timeout")
	}
}

func TestCollectorBoundsConcurrencyAcrossHealthAndMetrics(t *testing.T) {
	candidates := registry.New()
	for index := range 8 {
		addCollectorCandidate(t, candidates, fmt.Sprintf("10.0.0.%d:8081", index+1))
	}
	const limit = 3
	var active atomic.Int64
	var maximum atomic.Int64
	release := make(chan struct{})
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		select {
		case <-release:
			return successfulProbe(request)
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	collector := newTestCollector(t, candidates, transport)
	collector.config.MaxConcurrency = limit
	collector.semaphore = make(chan struct{}, limit)

	done := make(chan error, 2)
	go func() { done <- collector.CollectHealth(context.Background()) }()
	go func() { done <- collector.CollectMetrics(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for maximum.Load() < limit && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("collection error = %v", err)
		}
	}
	if got := maximum.Load(); got != limit {
		t.Errorf("maximum concurrent probes = %d, want %d", got, limit)
	}
}

func TestCollectorRunStopsOnContextCancellation(t *testing.T) {
	candidates := registry.New()
	addCollectorCandidate(t, candidates, "10.0.0.1:8081")
	collector := newTestCollector(t, candidates, roundTripFunc(successfulProbe))
	collector.config.HealthInterval = 5 * time.Millisecond
	collector.config.MetricsInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- collector.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for candidates.HealthySnapshot().Len() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := candidates.HealthySnapshot().Len(); got != 1 {
		t.Fatalf("snapshot length = %d, want 1", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

func TestCollectorCancellationStopsBlockedProbeRounds(t *testing.T) {
	candidates := registry.New()
	for index := range 32 {
		addCollectorCandidate(t, candidates, fmt.Sprintf("10.0.0.%d:8081", index+1))
	}

	const concurrency = 8
	var active atomic.Int64
	allWorkersBlocked := make(chan struct{})
	var signalOnce sync.Once
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if active.Add(1) == concurrency {
			signalOnce.Do(func() { close(allWorkersBlocked) })
		}
		defer active.Add(-1)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	collector := newTestCollector(t, candidates, transport)
	collector.config.MaxConcurrency = concurrency
	collector.semaphore = make(chan struct{}, concurrency)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- collector.CollectHealth(ctx) }()
	go func() { done <- collector.CollectMetrics(ctx) }()

	select {
	case <-allWorkersBlocked:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("probe workers did not reach the configured concurrency")
	}
	cancel()
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("collection error = %v, want context cancellation", err)
			}
		case <-time.After(time.Second):
			t.Fatal("blocked collection round did not stop after cancellation")
		}
	}
	if got := active.Load(); got != 0 {
		t.Errorf("active probes after cancellation = %d, want 0", got)
	}
}

func TestCollectorRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{`},
		{name: "unknown field", body: `{"instanceId":"pdu-1","status":"UP","extra":true}`},
		{name: "invalid identity", body: `{"instanceId":"","status":"UP"}`},
		{name: "multiple values", body: `{"instanceId":"pdu-1","status":"UP"} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidates := registry.New()
			addCollectorCandidate(t, candidates, "10.0.0.1:8081")
			collector := newTestCollector(t, candidates, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return probeResponse(http.StatusOK, test.body), nil
			}))
			if err := collector.CollectHealth(context.Background()); err == nil {
				t.Fatal("CollectHealth() error = nil, want invalid response error")
			}
			if got := candidates.HealthySnapshot().Len(); got != 0 {
				t.Errorf("snapshot length = %d, want 0", got)
			}
		})
	}
}

func TestNewCollectorValidatesDependenciesAndConfig(t *testing.T) {
	validConfig := CollectorConfig{
		HealthInterval: time.Second, HealthTimeout: time.Second,
		MetricsInterval: time.Second, MetricsTimeout: time.Second, MaxConcurrency: 1,
	}
	validRegistry := registry.New()
	validTransport := roundTripFunc(successfulProbe)
	tests := []struct {
		name      string
		registry  CollectorRegistry
		transport http.RoundTripper
		config    CollectorConfig
	}{
		{name: "nil registry", transport: validTransport, config: validConfig},
		{name: "nil transport", registry: validRegistry, config: validConfig},
		{name: "health duration", registry: validRegistry, transport: validTransport, config: CollectorConfig{MetricsInterval: time.Second, MetricsTimeout: time.Second, MaxConcurrency: 1}},
		{name: "metrics duration", registry: validRegistry, transport: validTransport, config: CollectorConfig{HealthInterval: time.Second, HealthTimeout: time.Second, MaxConcurrency: 1}},
		{name: "concurrency", registry: validRegistry, transport: validTransport, config: CollectorConfig{HealthInterval: time.Second, HealthTimeout: time.Second, MetricsInterval: time.Second, MetricsTimeout: time.Second}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCollector(test.registry, test.transport, test.config); err == nil {
				t.Fatal("NewCollector() error = nil, want validation error")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func newTestCollector(t *testing.T, candidates CollectorRegistry, transport http.RoundTripper) *Collector {
	t.Helper()
	collector, err := NewCollector(candidates, transport, CollectorConfig{
		HealthInterval: time.Second, HealthTimeout: time.Second,
		MetricsInterval: time.Second, MetricsTimeout: time.Second, MaxConcurrency: 4,
	})
	if err != nil {
		t.Fatalf("NewCollector() error = %v", err)
	}
	return collector
}

func addCollectorCandidate(t *testing.T, candidates *registry.Registry, rawAddress string) netip.AddrPort {
	t.Helper()
	address := netip.MustParseAddrPort(rawAddress)
	if _, err := candidates.Upsert(address, discoveryTestTime()); err != nil {
		t.Fatalf("Upsert(%s) error = %v", address, err)
	}
	return address
}

func collectBoth(t *testing.T, collector *Collector) {
	t.Helper()
	if err := collector.CollectHealth(context.Background()); err != nil {
		t.Fatalf("CollectHealth() error = %v", err)
	}
	if err := collector.CollectMetrics(context.Background()); err != nil {
		t.Fatalf("CollectMetrics() error = %v", err)
	}
}

func successfulProbe(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case constants.HealthPath:
		return probeResponse(http.StatusOK, `{"instanceId":"pdu-session-1","status":"UP"}`), nil
	case constants.MetricsPath:
		return probeResponse(http.StatusOK, `{"instanceId":"pdu-session-1","weight":3,"activeRequests":7}`), nil
	default:
		return nil, errors.New("unexpected probe path")
	}
}

func probeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
