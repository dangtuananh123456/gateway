package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/config"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestApplicationComposesAndStopsAllComponentsRepeatedly(t *testing.T) {
	lookup := &applicationLookup{}
	transport := &applicationTransport{}
	handlers := make(chan http.Handler, 3)
	server := ServerRunner(func(ctx context.Context, _ ServerConfig, handler http.Handler) error {
		handlers <- handler
		<-ctx.Done()
		return nil
	})
	application, err := NewApplicationWithDependencies(
		applicationTestConfig(),
		discardLogger(),
		ApplicationDependencies{Lookup: lookup, Transport: transport, Serve: server},
	)
	if err != nil {
		t.Fatalf("NewApplicationWithDependencies() error = %v", err)
	}
	apiHandler, ok := application.handler.(*APIHandler)
	if !ok || apiHandler.proxy.(*Proxy).transport != transport {
		t.Fatal("proxy does not use the injected shared transport")
	}

	for run := 1; run <= 3; run++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- application.Run(ctx) }()
		var handler http.Handler
		select {
		case handler = <-handlers:
		case <-time.After(time.Second):
			cancel()
			t.Fatalf("run %d server did not start", run)
		}

		deadline := time.Now().Add(time.Second)
		for {
			response := httptest.NewRecorder()
			handler.ServeHTTP(
				response,
				httptest.NewRequest(http.MethodPost, "http://gateway"+constants.CreateSMContextPath, nil),
			)
			if response.Code == http.StatusCreated && response.Body.String() == `{"handledBy":"pdu-1"}` {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatalf("run %d never became routable; last status=%d body=%q", run, response.Code, response.Body.String())
			}
			time.Sleep(time.Millisecond)
		}

		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run %d error = %v", run, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("run %d did not stop", run)
		}
		lookupCount := lookup.calls.Load()
		time.Sleep(15 * time.Millisecond)
		if got := lookup.calls.Load(); got != lookupCount {
			t.Errorf("run %d scheduler continued after shutdown: lookups %d -> %d", run, lookupCount, got)
		}
		if got := transport.closed.Load(); got != int64(run) {
			t.Errorf("run %d CloseIdleConnections calls = %d, want %d", run, got, run)
		}
	}
	if transport.healthCalls.Load() == 0 || transport.metricsCalls.Load() == 0 || transport.proxyCalls.Load() == 0 {
		t.Errorf("shared transport calls: health=%d metrics=%d proxy=%d, want all non-zero",
			transport.healthCalls.Load(), transport.metricsCalls.Load(), transport.proxyCalls.Load())
	}
}

func TestApplicationComponentFailureCancelsLifecycle(t *testing.T) {
	transport := &applicationTransport{}
	serverFailure := errors.New("server failed")
	application, err := NewApplicationWithDependencies(
		applicationTestConfig(),
		discardLogger(),
		ApplicationDependencies{
			Lookup:    &applicationLookup{},
			Transport: transport,
			Serve: func(context.Context, ServerConfig, http.Handler) error {
				return serverFailure
			},
		},
	)
	if err != nil {
		t.Fatalf("NewApplicationWithDependencies() error = %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- application.Run(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, serverFailure) {
			t.Fatalf("Run() error = %v, want server failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not cancel remaining components after server failure")
	}
	if got := transport.closed.Load(); got != 1 {
		t.Errorf("CloseIdleConnections calls = %d, want 1", got)
	}
}

func TestApplicationShutdownCancelsSlowDNSProbesAndInflightRequests(t *testing.T) {
	lookup := newBlockingApplicationLookup()
	transport := newBlockingApplicationTransport()
	const inflightRequests = 24
	serverReady := make(chan struct{})
	server := ServerRunner(func(ctx context.Context, _ ServerConfig, handler http.Handler) error {
		deadline := time.Now().Add(time.Second)
		for {
			response := httptest.NewRecorder()
			handler.ServeHTTP(
				response,
				httptest.NewRequest(http.MethodPost, "http://gateway"+constants.CreateSMContextPath, nil).WithContext(ctx),
			)
			if response.Code == http.StatusCreated {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("Gateway did not become routable")
			}
			time.Sleep(time.Millisecond)
		}

		var requests sync.WaitGroup
		requests.Add(inflightRequests)
		for range inflightRequests {
			go func() {
				defer requests.Done()
				response := httptest.NewRecorder()
				handler.ServeHTTP(
					response,
					httptest.NewRequest(http.MethodPost, "http://gateway"+constants.CreateSMContextPath, nil).WithContext(ctx),
				)
			}()
		}
		close(serverReady)
		<-ctx.Done()
		requests.Wait()
		return nil
	})

	application, err := NewApplicationWithDependencies(
		applicationTestConfig(),
		discardLogger(),
		ApplicationDependencies{Lookup: lookup, Transport: transport, Serve: server},
	)
	if err != nil {
		t.Fatalf("NewApplicationWithDependencies() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	select {
	case <-serverReady:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("server did not start inflight requests")
	}

	deadline := time.Now().Add(time.Second)
	for (lookup.blocked.Load() == 0 || transport.blockedHealth.Load() == 0 ||
		transport.blockedMetrics.Load() == 0 || transport.blockedProxy.Load() < inflightRequests) &&
		time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if lookup.blocked.Load() == 0 || transport.blockedHealth.Load() == 0 ||
		transport.blockedMetrics.Load() == 0 || transport.blockedProxy.Load() < inflightRequests {
		cancel()
		t.Fatalf("slow operations not all active: DNS=%d health=%d metrics=%d proxy=%d",
			lookup.blocked.Load(), transport.blockedHealth.Load(),
			transport.blockedMetrics.Load(), transport.blockedProxy.Load())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("application did not stop after lifecycle cancellation")
	}
	if got := lookup.active.Load(); got != 0 {
		t.Errorf("active DNS lookups after shutdown = %d, want 0", got)
	}
	if got := transport.active.Load(); got != 0 {
		t.Errorf("active transport calls after shutdown = %d, want 0", got)
	}
	if got := transport.closed.Load(); got != 1 {
		t.Errorf("CloseIdleConnections calls = %d, want 1", got)
	}
}

func TestNewApplicationWithDependenciesValidatesInputs(t *testing.T) {
	validConfig := applicationTestConfig()
	validLogger := discardLogger()
	validDependencies := ApplicationDependencies{
		Lookup: &applicationLookup{}, Transport: &applicationTransport{},
		Serve: func(ctx context.Context, _ ServerConfig, _ http.Handler) error {
			<-ctx.Done()
			return nil
		},
	}
	tests := []struct {
		name         string
		cfg          config.Config
		logger       *slog.Logger
		dependencies ApplicationDependencies
	}{
		{name: "logger", cfg: validConfig, dependencies: validDependencies},
		{name: "lookup", cfg: validConfig, logger: validLogger, dependencies: ApplicationDependencies{Transport: validDependencies.Transport, Serve: validDependencies.Serve}},
		{name: "transport", cfg: validConfig, logger: validLogger, dependencies: ApplicationDependencies{Lookup: validDependencies.Lookup, Serve: validDependencies.Serve}},
		{name: "server", cfg: validConfig, logger: validLogger, dependencies: ApplicationDependencies{Lookup: validDependencies.Lookup, Transport: validDependencies.Transport}},
		{name: "config", cfg: config.Config{}, logger: validLogger, dependencies: validDependencies},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewApplicationWithDependencies(test.cfg, test.logger, test.dependencies); err == nil {
				t.Fatal("NewApplicationWithDependencies() error = nil, want validation error")
			}
		})
	}
}

func TestApplicationRejectsNilRunContext(t *testing.T) {
	application, err := NewApplicationWithDependencies(
		applicationTestConfig(),
		discardLogger(),
		ApplicationDependencies{
			Lookup: &applicationLookup{}, Transport: &applicationTransport{},
			Serve: func(context.Context, ServerConfig, http.Handler) error { return nil },
		},
	)
	if err != nil {
		t.Fatalf("NewApplicationWithDependencies() error = %v", err)
	}
	if err := application.Run(nil); err == nil {
		t.Fatal("Run(nil) error = nil, want validation error")
	}
}

func TestNewSharedTransportIsTunedForReuse(t *testing.T) {
	transport := NewSharedTransport()
	t.Cleanup(transport.CloseIdleConnections)
	if transport.Proxy != nil {
		t.Error("Proxy is configured, want direct internal PDU connections")
	}
	if !transport.DisableCompression {
		t.Error("DisableCompression = false, want true to preserve upstream payload")
	}
	if transport.MaxIdleConns != constants.UpstreamMaxIdleConnections {
		t.Errorf("MaxIdleConns = %d, want %d", transport.MaxIdleConns, constants.UpstreamMaxIdleConnections)
	}
	if transport.MaxIdleConnsPerHost != constants.UpstreamMaxIdleConnectionsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", transport.MaxIdleConnsPerHost, constants.UpstreamMaxIdleConnectionsPerHost)
	}
}

type applicationLookup struct {
	calls atomic.Int64
}

func (lookup *applicationLookup) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	lookup.calls.Add(1)
	return []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}}, nil
}

type applicationTransport struct {
	healthCalls  atomic.Int64
	metricsCalls atomic.Int64
	proxyCalls   atomic.Int64
	closed       atomic.Int64
}

type blockingApplicationLookup struct {
	calls   atomic.Int64
	blocked atomic.Int64
	active  atomic.Int64
}

func newBlockingApplicationLookup() *blockingApplicationLookup {
	return &blockingApplicationLookup{}
}

func (lookup *blockingApplicationLookup) LookupIPAddr(ctx context.Context, _ string) ([]net.IPAddr, error) {
	if lookup.calls.Add(1) == 1 {
		return []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}}, nil
	}
	lookup.blocked.Add(1)
	lookup.active.Add(1)
	defer lookup.active.Add(-1)
	<-ctx.Done()
	return nil, ctx.Err()
}

type blockingApplicationTransport struct {
	healthCalls    atomic.Int64
	metricsCalls   atomic.Int64
	proxyCalls     atomic.Int64
	blockedHealth  atomic.Int64
	blockedMetrics atomic.Int64
	blockedProxy   atomic.Int64
	active         atomic.Int64
	closed         atomic.Int64
}

func newBlockingApplicationTransport() *blockingApplicationTransport {
	return &blockingApplicationTransport{}
}

func (transport *blockingApplicationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case constants.HealthPath:
		if transport.healthCalls.Add(1) == 1 {
			return proxyResponse(http.StatusOK, `{"instanceId":"pdu-1","status":"UP"}`), nil
		}
		transport.blockedHealth.Add(1)
	case constants.MetricsPath:
		if transport.metricsCalls.Add(1) == 1 {
			return proxyResponse(http.StatusOK, `{"instanceId":"pdu-1","weight":1,"activeRequests":0}`), nil
		}
		transport.blockedMetrics.Add(1)
	default:
		if transport.proxyCalls.Add(1) == 1 {
			return proxyResponse(http.StatusCreated, `{"handledBy":"pdu-1"}`), nil
		}
		transport.blockedProxy.Add(1)
	}
	transport.active.Add(1)
	defer transport.active.Add(-1)
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func (transport *blockingApplicationTransport) CloseIdleConnections() {
	transport.closed.Add(1)
}

func (transport *applicationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case constants.HealthPath:
		transport.healthCalls.Add(1)
		return proxyResponse(http.StatusOK, `{"instanceId":"pdu-1","status":"UP"}`), nil
	case constants.MetricsPath:
		transport.metricsCalls.Add(1)
		return proxyResponse(http.StatusOK, `{"instanceId":"pdu-1","weight":1,"activeRequests":0}`), nil
	default:
		transport.proxyCalls.Add(1)
		return proxyResponse(http.StatusCreated, `{"handledBy":"pdu-1"}`), nil
	}
}

func (transport *applicationTransport) CloseIdleConnections() {
	transport.closed.Add(1)
}

func applicationTestConfig() config.Config {
	return config.Config{
		Gateway: config.GatewayConfig{
			Server: config.HTTPServerConfig{
				Address: "127.0.0.1:18080", ReadHeaderTimeout: time.Second,
				IdleTimeout: time.Second, ShutdownTimeout: time.Second, MaxHeaderBytes: 1 << 20,
			},
			UpstreamTimeout: time.Second,
			PublicURL:       "http://localhost:18080",
		},
		PDU: config.PDUConfig{
			Server: config.HTTPServerConfig{
				Address: "127.0.0.1:18081", ReadHeaderTimeout: time.Second,
				IdleTimeout: time.Second, ShutdownTimeout: time.Second, MaxHeaderBytes: 1 << 20,
			},
			Weight: 1,
		},
		Routing: config.RoutingConfig{Mode: constants.RoutingRoundRobin},
		Discovery: config.DiscoveryConfig{
			Hostname: "pdu-session", Port: 8081,
			PollInterval: 5 * time.Millisecond, LookupTimeout: 50 * time.Millisecond,
			HealthInterval: 5 * time.Millisecond, HealthTimeout: 50 * time.Millisecond,
			MetricsInterval: 5 * time.Millisecond, MetricsTimeout: 50 * time.Millisecond,
			StaleTTL: 20 * time.Millisecond, MaxConcurrency: 4,
		},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
