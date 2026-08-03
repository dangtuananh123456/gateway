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
	if err := collector.CollectHealth(context.Background()); err == nil {
		t.Fatal("failed CollectHealth() error = nil, want error")
	}
	if got := candidates.HealthySnapshot().Len(); got != 0 {
		t.Fatalf("snapshot after failure = %d, want 0", got)
	}
	failing.Store(false)
	collectBoth(t, collector)
	if got := candidates.HealthySnapshot().Len(); got != 1 {
		t.Errorf("snapshot after recovery = %d, want 1", got)
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
