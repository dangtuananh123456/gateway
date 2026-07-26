package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testJWTSecret = "0123456789abcdef0123456789abcdef"

func TestLoadAndValidateExampleConfig(t *testing.T) {
	cfg, err := load(exampleConfigPath(), environment(map[string]string{
		envJWTSecret: testJWTSecret,
	}))
	if err != nil {
		t.Fatalf("load example config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate example config: %v", err)
	}

	if cfg.Server.Address != ":8080" {
		t.Errorf("Server.Address = %q, want %q", cfg.Server.Address, ":8080")
	}
	if cfg.Server.ReadHeaderTimeout != 2*time.Second {
		t.Errorf(
			"Server.ReadHeaderTimeout = %v, want %v",
			cfg.Server.ReadHeaderTimeout,
			2*time.Second,
		)
	}
	if cfg.Auth.Secret != testJWTSecret {
		t.Error("Auth.Secret was not loaded from the environment")
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("len(Routes) = %d, want 2", len(cfg.Routes))
	}
	if cfg.Routes[0].Upstream != "http://mock-users:8081" {
		t.Errorf(
			"Routes[0].Upstream = %q, want %q",
			cfg.Routes[0].Upstream,
			"http://mock-users:8081",
		)
	}
	if cfg.Routes[1].Upstream != "http://mock-orders:8082" {
		t.Errorf(
			"Routes[1].Upstream = %q, want %q",
			cfg.Routes[1].Upstream,
			"http://mock-orders:8082",
		)
	}
}

func TestLoadAppliesEnvironmentOverrides(t *testing.T) {
	cfg, err := load(exampleConfigPath(), environment(map[string]string{
		envJWTSecret:                  testJWTSecret,
		envServerAddress:              ":9090",
		envServerReadHeaderTimeout:    "5s",
		envServerMaxHeaderBytes:       "2048",
		envAuthIssuer:                 "overridden-issuer",
		envRateLimitRequestsPerSecond: "42.5",
		envRateLimitBurst:             "84",
		envRateLimitOperationTimeout:  "25ms",
		envRedisAddress:               "localhost:6379",
		envRedisPoolSize:              "50",
		envRedisMinIdleConnections:    "5",
		envRedisPassword:              "redis-secret",
		envPprofEnabled:               "true",
		envLogLevel:                   "debug",
	}))
	if err != nil {
		t.Fatalf("load config with overrides: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate overridden config: %v", err)
	}

	if cfg.Server.Address != ":9090" {
		t.Errorf("Server.Address = %q, want %q", cfg.Server.Address, ":9090")
	}
	if cfg.Server.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("Server.ReadHeaderTimeout = %v, want 5s", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.Server.MaxHeaderBytes != 2048 {
		t.Errorf("Server.MaxHeaderBytes = %d, want 2048", cfg.Server.MaxHeaderBytes)
	}
	if cfg.Auth.Issuer != "overridden-issuer" {
		t.Errorf("Auth.Issuer = %q, want overridden value", cfg.Auth.Issuer)
	}
	if cfg.RateLimit.RequestsPerSecond != 42.5 {
		t.Errorf(
			"RateLimit.RequestsPerSecond = %v, want 42.5",
			cfg.RateLimit.RequestsPerSecond,
		)
	}
	if cfg.RateLimit.Burst != 84 {
		t.Errorf("RateLimit.Burst = %d, want 84", cfg.RateLimit.Burst)
	}
	if cfg.RateLimit.OperationTimeout != 25*time.Millisecond {
		t.Errorf(
			"RateLimit.OperationTimeout = %v, want 25ms",
			cfg.RateLimit.OperationTimeout,
		)
	}
	if cfg.Redis.Address != "localhost:6379" {
		t.Errorf("Redis.Address = %q, want %q", cfg.Redis.Address, "localhost:6379")
	}
	if cfg.Redis.PoolSize != 50 || cfg.Redis.MinIdleConnections != 5 {
		t.Errorf(
			"Redis pool = (%d, %d), want (50, 5)",
			cfg.Redis.PoolSize,
			cfg.Redis.MinIdleConnections,
		)
	}
	if cfg.Redis.Password != "redis-secret" {
		t.Error("Redis.Password was not loaded from the environment")
	}
	if !cfg.Observability.PprofEnabled {
		t.Error("Observability.PprofEnabled = false, want true")
	}
	if cfg.Observability.LogLevel != "debug" {
		t.Errorf("Observability.LogLevel = %q, want %q", cfg.Observability.LogLevel, "debug")
	}
}

func TestLoadFilesUsesProcessEnvironmentBeforeDotEnv(t *testing.T) {
	envPath := writeEnvironment(t, `
GATEWAY_JWT_SECRET=env-file-secret
GATEWAY_SERVER_ADDRESS=:9090
GATEWAY_REDIS_ADDRESS=localhost:6379
`)

	cfg, err := loadFiles(
		exampleConfigPath(),
		[]string{envPath},
		environment(map[string]string{
			envJWTSecret:     testJWTSecret,
			envServerAddress: ":7070",
		}),
	)
	if err != nil {
		t.Fatalf("load config and environment file: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}

	if cfg.Server.Address != ":7070" {
		t.Errorf("Server.Address = %q, want process override %q", cfg.Server.Address, ":7070")
	}
	if cfg.Auth.Secret != testJWTSecret {
		t.Error("Auth.Secret did not use the process environment override")
	}
	if cfg.Redis.Address != "localhost:6379" {
		t.Errorf(
			"Redis.Address = %q, want .env value %q",
			cfg.Redis.Address,
			"localhost:6379",
		)
	}
}

func TestLoadFilesFailsValidation(t *testing.T) {
	envPath := writeEnvironment(t, `
GATEWAY_JWT_SECRET=secret
GATEWAY_SERVER_ADDRESS=invalid-address
`)

	_, err := loadFiles(exampleConfigPath(), []string{envPath}, environment(nil))
	if err == nil {
		t.Fatal("loadFiles() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "validate config") ||
		!strings.Contains(err.Error(), "server.address") {
		t.Errorf(
			"loadFiles() error = %q, want validation error for server.address",
			err,
		)
	}
}

func TestReadEnvironmentFile(t *testing.T) {
	path := writeEnvironment(t, `
# comment
PLAIN=value
EXPORTED=before
export EXPORTED_VALUE="hello world"
SINGLE_QUOTED='secret # value'
INLINE_COMMENT=value # ignored
EMPTY=
`)

	values, err := readEnvironmentFile(path)
	if err != nil {
		t.Fatalf("read environment file: %v", err)
	}

	expected := map[string]string{
		"PLAIN":          "value",
		"EXPORTED":       "before",
		"EXPORTED_VALUE": "hello world",
		"SINGLE_QUOTED":  "secret # value",
		"INLINE_COMMENT": "value",
		"EMPTY":          "",
	}
	for key, want := range expected {
		if got := values[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestReadEnvironmentFileRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "missing equals",
			content: "INVALID",
			wantErr: "expected KEY=VALUE",
		},
		{
			name:    "invalid key",
			content: "1INVALID=value",
			wantErr: "invalid key",
		},
		{
			name:    "unterminated quote",
			content: `VALUE="unterminated`,
			wantErr: "unterminated",
		},
		{
			name:    "duplicate key",
			content: "VALUE=first\nVALUE=second",
			wantErr: "duplicate key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeEnvironment(t, tt.content)

			_, err := readEnvironmentFile(path)
			if err == nil {
				t.Fatal("readEnvironmentFile() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf(
					"readEnvironmentFile() error = %q, want it to contain %q",
					err,
					tt.wantErr,
				)
			}
		})
	}
}

func TestLoadRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		content string
		env     map[string]string
		wantErr string
	}{
		{
			name: "unknown YAML field",
			content: `
server:
  address: ":8080"
  unknown: true
`,
			wantErr: "field unknown not found",
		},
		{
			name:    "malformed YAML",
			content: "server: [",
			wantErr: "decode config",
		},
		{
			name: "multiple YAML documents",
			content: `
server:
  address: ":8080"
---
server:
  address: ":9090"
`,
			wantErr: "multiple YAML documents",
		},
		{
			name:    "invalid environment value",
			content: "server:\n  address: \":8080\"\n",
			env: map[string]string{
				envRedisPoolSize: "not-a-number",
			},
			wantErr: envRedisPoolSize,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.content)

			_, err := load(path, environment(tt.env))
			if err == nil {
				t.Fatal("load() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("load() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")

	_, err := load(path, environment(nil))
	if err == nil {
		t.Fatal("load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "open config") {
		t.Errorf("load() error = %q, want it to contain %q", err, "open config")
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	cfg, err := load(exampleConfigPath(), environment(map[string]string{
		envJWTSecret: testJWTSecret,
	}))
	if err != nil {
		t.Fatalf("load example config: %v", err)
	}

	cfg.Server.Address = "invalid-address"
	cfg.Server.UpstreamTimeout = 0
	cfg.Auth.Algorithm = "none"
	cfg.RateLimit.RequestsPerSecond = math.NaN()
	cfg.Redis.MinIdleConnections = cfg.Redis.PoolSize + 1
	cfg.Routes[0].Upstream = "ftp://mock-users:21"
	cfg.Routes[1].Prefix = cfg.Routes[0].Prefix
	cfg.Observability.LogLevel = "verbose"

	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want error")
	}

	for _, expected := range []string{
		"server.address",
		"server.upstream_timeout",
		"auth.algorithm",
		"rate_limit.requests_per_second",
		"redis.min_idle_connections",
		"routes[0].upstream",
		"duplicates",
		"observability.log_level",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("Validate() error = %q, want it to contain %q", err, expected)
		}
	}
}

func exampleConfigPath() string {
	return filepath.Join("..", "..", "configs", "config.yaml")
}

func environment(values map[string]string) envLookup {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	return path
}

func writeEnvironment(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatalf("write test environment: %v", err)
	}

	return path
}
