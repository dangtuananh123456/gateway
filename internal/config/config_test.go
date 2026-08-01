package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/pkg/constants"
)

const validYAML = `gateway:
  server:
    address: ":8080"
    read_header_timeout: 2s
    idle_timeout: 60s
    shutdown_timeout: 10s
    max_header_bytes: 1048576
  upstream_timeout: 3s
  public_url: "http://localhost:8080"
pdu:
  server:
    address: ":8081"
    read_header_timeout: 2s
    idle_timeout: 60s
    shutdown_timeout: 10s
    max_header_bytes: 1048576
  instance_id: ""
  weight: 1
  processing_delay: 0s
routing:
  mode: round_robin
discovery:
  hostname: pdu-session
  port: 8081
  poll_interval: 5s
  lookup_timeout: 1s
  health_interval: 5s
  health_timeout: 1s
  metrics_interval: 250ms
  metrics_timeout: 100ms
  stale_ttl: 15s
  max_concurrency: 20
`

func TestLoad(t *testing.T) {
	t.Setenv("ROUTING_MODE", "load")
	directory := t.TempDir()
	configPath := writeTestFile(t, directory, "config.yaml", validYAML)
	envPath := writeTestFile(t, directory, ".env", strings.Join([]string{
		"# local overrides",
		"GATEWAY_ADDRESS=':9090'",
		"PDU_WEIGHT=3",
		"ROUTING_MODE=weighted",
		"DISCOVERY_METRICS_TIMEOUT=\"150ms\"",
	}, "\n"))

	cfg, err := Load(configPath, envPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Gateway.Server.Address != ":9090" {
		t.Errorf("gateway address = %q, want :9090 from dotenv", cfg.Gateway.Server.Address)
	}
	if cfg.PDU.Weight != 3 {
		t.Errorf("PDU weight = %d, want 3 from dotenv", cfg.PDU.Weight)
	}
	if cfg.Routing.Mode != constants.RoutingLoad {
		t.Errorf("routing mode = %q, want load from process environment", cfg.Routing.Mode)
	}
	if cfg.Discovery.MetricsTimeout != 150*time.Millisecond {
		t.Errorf("metrics timeout = %s, want 150ms", cfg.Discovery.MetricsTimeout)
	}
}

func TestLoadAllowsMissingDotEnv(t *testing.T) {
	directory := t.TempDir()
	configPath := writeTestFile(t, directory, "config.yaml", validYAML)
	if _, err := Load(configPath, filepath.Join(directory, "missing.env")); err != nil {
		t.Fatalf("Load() with missing optional dotenv error = %v", err)
	}
}

func TestLoadRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		dotenv      string
		wantInError string
	}{
		{name: "unknown YAML field", yaml: validYAML + "unknown: true\n", wantInError: "field unknown not found"},
		{name: "multiple documents", yaml: validYAML + "---\nrouting: {}\n", wantInError: "multiple YAML documents"},
		{name: "invalid dotenv", yaml: validYAML, dotenv: "not an assignment", wantInError: "expected KEY=VALUE"},
		{name: "invalid environment duration", yaml: validYAML, dotenv: "GATEWAY_UPSTREAM_TIMEOUT=soon", wantInError: "GATEWAY_UPSTREAM_TIMEOUT"},
		{name: "validation failure", yaml: strings.Replace(validYAML, "mode: round_robin", "mode: random", 1), wantInError: "routing.mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			configPath := writeTestFile(t, directory, "config.yaml", test.yaml)
			envPath := filepath.Join(directory, ".env")
			if test.dotenv != "" {
				writeTestFile(t, directory, ".env", test.dotenv)
			}
			_, err := Load(configPath, envPath)
			if err == nil || !strings.Contains(err.Error(), test.wantInError) {
				t.Fatalf("Load() error = %v, want error containing %q", err, test.wantInError)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := validConfig()
	tests := []struct {
		name        string
		mutate      func(*Config)
		wantInError string
	}{
		{name: "listen address", mutate: func(cfg *Config) { cfg.Gateway.Server.Address = "8080" }, wantInError: "gateway.server.address"},
		{name: "server timeout", mutate: func(cfg *Config) { cfg.PDU.Server.IdleTimeout = 0 }, wantInError: "pdu.server.idle_timeout"},
		{name: "upstream timeout", mutate: func(cfg *Config) { cfg.Gateway.UpstreamTimeout = -time.Second }, wantInError: "gateway.upstream_timeout"},
		{name: "public URL", mutate: func(cfg *Config) { cfg.Gateway.PublicURL = "https://gateway" }, wantInError: "gateway.public_url"},
		{name: "PDU weight", mutate: func(cfg *Config) { cfg.PDU.Weight = 0 }, wantInError: "pdu.weight"},
		{name: "processing delay", mutate: func(cfg *Config) { cfg.PDU.ProcessingDelay = -time.Millisecond }, wantInError: "pdu.processing_delay"},
		{name: "routing mode", mutate: func(cfg *Config) { cfg.Routing.Mode = "random" }, wantInError: "routing.mode"},
		{name: "discovery hostname", mutate: func(cfg *Config) { cfg.Discovery.Hostname = "http://pdu" }, wantInError: "discovery.hostname"},
		{name: "discovery port", mutate: func(cfg *Config) { cfg.Discovery.Port = 70000 }, wantInError: "discovery.port"},
		{name: "poll duration", mutate: func(cfg *Config) { cfg.Discovery.PollInterval = 0 }, wantInError: "discovery.poll_interval"},
		{name: "stale TTL", mutate: func(cfg *Config) { cfg.Discovery.StaleTTL = time.Second }, wantInError: "discovery.stale_ttl"},
		{name: "max concurrency", mutate: func(cfg *Config) { cfg.Discovery.MaxConcurrency = 0 }, wantInError: "discovery.max_concurrency"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.wantInError) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, test.wantInError)
			}
		})
	}
}

func TestLoadMissingConfigFailsFast(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), "")
	if err == nil || !strings.Contains(err.Error(), "read config file") {
		t.Fatalf("Load() error = %v, want read config file error", err)
	}
}

func validConfig() Config {
	return Config{
		Gateway: GatewayConfig{
			Server:          HTTPServerConfig{Address: ":8080", ReadHeaderTimeout: 2 * time.Second, IdleTimeout: time.Minute, ShutdownTimeout: 10 * time.Second, MaxHeaderBytes: 1 << 20},
			UpstreamTimeout: 3 * time.Second,
			PublicURL:       "http://localhost:8080",
		},
		PDU: PDUConfig{
			Server: HTTPServerConfig{Address: ":8081", ReadHeaderTimeout: 2 * time.Second, IdleTimeout: time.Minute, ShutdownTimeout: 10 * time.Second, MaxHeaderBytes: 1 << 20},
			Weight: 1,
		},
		Routing: RoutingConfig{Mode: constants.RoutingRoundRobin},
		Discovery: DiscoveryConfig{
			Hostname: "pdu-session", Port: 8081, PollInterval: 5 * time.Second,
			LookupTimeout: time.Second, HealthInterval: 5 * time.Second, HealthTimeout: time.Second,
			MetricsInterval: 250 * time.Millisecond, MetricsTimeout: 100 * time.Millisecond,
			StaleTTL: 15 * time.Second, MaxConcurrency: 20,
		},
	}
}

func writeTestFile(t *testing.T, directory, name, contents string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	return path
}
