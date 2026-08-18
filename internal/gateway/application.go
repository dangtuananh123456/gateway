package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/dangtuananh123456/gateway/internal/config"
	"github.com/dangtuananh123456/gateway/internal/discovery"
	"github.com/dangtuananh123456/gateway/internal/registry"
	"github.com/dangtuananh123456/gateway/internal/requestlog"
	"github.com/dangtuananh123456/gateway/internal/routing"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// SharedTransport is an Application-owned HTTP pool. Production uses separate
// instances for data-plane proxy traffic and control-plane discovery probes.
type SharedTransport interface {
	http.RoundTripper
	CloseIdleConnections()
}

// ServerRunner starts the public Gateway server and blocks until it stops.
type ServerRunner func(context.Context, ServerConfig, http.Handler) error

// ApplicationDependencies are replaceable infrastructure boundaries for tests.
type ApplicationDependencies struct {
	Lookup             discovery.IPAddressLookup
	Transport          SharedTransport
	DiscoveryTransport SharedTransport
	Serve              ServerRunner
}

// Application owns the fully composed Gateway process lifecycle.
type Application struct {
	scheduler  *discovery.Scheduler
	collector  *discovery.Collector
	transports []SharedTransport
	server     ServerRunner
	serverCfg  ServerConfig
	handler    http.Handler
}

// NewApplication creates a production Gateway application.
func NewApplication(cfg config.Config, logger *slog.Logger) (*Application, error) {
	transport := NewSharedTransport()
	discoveryTransport := NewDiscoveryTransport()
	application, err := NewApplicationWithDependencies(cfg, logger, ApplicationDependencies{
		Lookup:             net.DefaultResolver,
		Transport:          transport,
		DiscoveryTransport: discoveryTransport,
		Serve:              Run,
	})
	if err != nil {
		transport.CloseIdleConnections()
		discoveryTransport.CloseIdleConnections()
		return nil, err
	}
	return application, nil
}

// NewApplicationWithDependencies composes the Gateway with injected infrastructure.
// A successfully created Application owns Transport for its Run lifecycle.
func NewApplicationWithDependencies(
	cfg config.Config,
	logger *slog.Logger,
	dependencies ApplicationDependencies,
) (*Application, error) {
	if logger == nil {
		return nil, errors.New("create Gateway application: logger must not be nil")
	}
	if dependencies.Lookup == nil {
		return nil, errors.New("create Gateway application: DNS lookup must not be nil")
	}
	if dependencies.Transport == nil {
		return nil, errors.New("create Gateway application: transport must not be nil")
	}
	discoveryTransport := dependencies.DiscoveryTransport
	transports := []SharedTransport{dependencies.Transport}
	if discoveryTransport == nil {
		// Backward-compatible test/integration fallback. Production always
		// supplies a dedicated control-plane transport.
		discoveryTransport = dependencies.Transport
	} else {
		transports = append(transports, discoveryTransport)
	}
	if dependencies.Serve == nil {
		return nil, errors.New("create Gateway application: server runner must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("create Gateway application: validate config: %w", err)
	}

	candidates := registry.New()
	resolver, err := discovery.NewDNSResolver(
		dependencies.Lookup,
		cfg.Discovery.Hostname,
		cfg.Discovery.Port,
		cfg.Discovery.LookupTimeout,
	)
	if err != nil {
		return nil, err
	}
	reportDiscoveryError := func(err error) {
		logger.Warn("Gateway discovery error", "error", err)
	}
	scheduler, err := discovery.NewScheduler(resolver, candidates, discovery.SchedulerConfig{
		PollInterval: cfg.Discovery.PollInterval,
		RoundTimeout: cfg.Discovery.LookupTimeout,
		StaleTTL:     cfg.Discovery.StaleTTL,
		OnError:      reportDiscoveryError,
	})
	if err != nil {
		return nil, err
	}
	collector, err := discovery.NewCollector(candidates, discoveryTransport, discovery.CollectorConfig{
		HealthInterval:  cfg.Discovery.HealthInterval,
		HealthTimeout:   cfg.Discovery.HealthTimeout,
		MetricsInterval: cfg.Discovery.MetricsInterval,
		MetricsTimeout:  cfg.Discovery.MetricsTimeout,
		MaxConcurrency:  cfg.Discovery.MaxConcurrency,
		OnError:         reportDiscoveryError,
	})
	if err != nil {
		return nil, err
	}
	factory, err := routing.NewFactory(
		routing.NewRoundRobin(),
		routing.NewSmoothWeighted(),
		routing.NewLoadBased(),
	)
	if err != nil {
		return nil, err
	}
	selector, err := factory.Create(cfg.Routing.Mode)
	if err != nil {
		return nil, err
	}
	proxy, err := NewProxy(candidates, selector, dependencies.Transport, cfg.Gateway.UpstreamTimeout)
	if err != nil {
		return nil, err
	}
	apiHandler, err := NewAPIHandler(candidates, cfg.Routing.Mode, proxy)
	if err != nil {
		return nil, err
	}

	return &Application{
		scheduler:  scheduler,
		collector:  collector,
		transports: transports,
		server:     dependencies.Serve,
		serverCfg:  serverConfigFrom(cfg.Gateway.Server),
		handler: requestlog.Wrap(
			cfg.Logging.Enabled && cfg.Logging.AccessLogEnabled,
			logger,
			"gateway",
			apiHandler,
		),
	}, nil
}

// NewSharedTransport creates the h2c data-plane pool shared by all proxied PDU
// requests. HTTP/2 multiplexing keeps the connection count bounded while the
// non-strict setting lets the transport open another connection if every
// existing connection has reached the peer's stream limit.
func NewSharedTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.Protocols = h2cOnlyProtocols()
	transport.DisableCompression = true
	transport.MaxIdleConns = constants.UpstreamMaxIdleConnections
	transport.MaxIdleConnsPerHost = constants.UpstreamMaxIdleConnectionsPerHost
	transport.MaxConnsPerHost = constants.UpstreamMaxConnectionsPerHost
	transport.HTTP2 = &http.HTTP2Config{StrictMaxConcurrentRequests: false}
	return transport
}

// NewDiscoveryTransport reserves a small h2c pool for health and metrics
// probes so control-plane liveness cannot queue behind proxied traffic.
func NewDiscoveryTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.Protocols = h2cOnlyProtocols()
	transport.DisableCompression = true
	transport.MaxIdleConns = constants.DiscoveryMaxIdleConnections
	transport.MaxIdleConnsPerHost = constants.DiscoveryMaxIdleConnectionsPerHost
	transport.MaxConnsPerHost = constants.DiscoveryMaxConnectionsPerHost
	transport.HTTP2 = &http.HTTP2Config{StrictMaxConcurrentRequests: false}
	return transport
}

// Run starts all components with one root context and waits for every component.
func (application *Application) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("run Gateway application: context must not be nil")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		for _, transport := range application.transports {
			transport.CloseIdleConnections()
		}
	}()

	type componentResult struct {
		name string
		err  error
	}
	results := make(chan componentResult, 3)
	var components sync.WaitGroup
	start := func(name string, run func(context.Context) error) {
		components.Add(1)
		go func() {
			defer components.Done()
			results <- componentResult{name: name, err: run(runCtx)}
		}()
	}
	start("DNS scheduler", application.scheduler.Run)
	start("health and metrics collector", application.collector.Run)
	start("HTTP server", func(componentCtx context.Context) error {
		return application.server(
			componentCtx,
			application.serverCfg,
			application.handler,
		)
	})

	var lifecycleErrors []error
	unexpectedStop := false
	for range 3 {
		result := <-results
		if result.err != nil && !errors.Is(result.err, context.Canceled) {
			lifecycleErrors = append(lifecycleErrors, fmt.Errorf("%s: %w", result.name, result.err))
		}
		if ctx.Err() == nil && !unexpectedStop {
			unexpectedStop = true
			if result.err == nil {
				lifecycleErrors = append(lifecycleErrors, fmt.Errorf("%s stopped unexpectedly", result.name))
			}
			cancel()
		}
	}
	components.Wait()
	return errors.Join(lifecycleErrors...)
}

func serverConfigFrom(cfg config.HTTPServerConfig) ServerConfig {
	return ServerConfig{
		Address:           cfg.Address,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ShutdownTimeout:   cfg.ShutdownTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
}
