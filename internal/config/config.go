package config

import "time"

// Config contains all runtime settings required by the gateway.
type Config struct {
	Server        ServerConfig        `json:"server" yaml:"server"`
	Auth          AuthConfig          `json:"auth" yaml:"auth"`
	RateLimit     RateLimitConfig     `json:"rate_limit" yaml:"rate_limit"`
	Redis         RedisConfig         `json:"redis" yaml:"redis"`
	Routes        []RouteConfig       `json:"routes" yaml:"routes"`
	Observability ObservabilityConfig `json:"observability" yaml:"observability"`
}

// ServerConfig controls the public HTTP server and upstream request lifetime.
type ServerConfig struct {
	Address           string        `json:"address" yaml:"address"`
	ReadHeaderTimeout time.Duration `json:"read_header_timeout" yaml:"read_header_timeout"`
	IdleTimeout       time.Duration `json:"idle_timeout" yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `json:"shutdown_timeout" yaml:"shutdown_timeout"`
	UpstreamTimeout   time.Duration `json:"upstream_timeout" yaml:"upstream_timeout"`
	MaxHeaderBytes    int           `json:"max_header_bytes" yaml:"max_header_bytes"`
}

// AuthConfig controls JWT signature and registered-claim verification.
type AuthConfig struct {
	Issuer    string `json:"issuer" yaml:"issuer"`
	Audience  string `json:"audience" yaml:"audience"`
	Algorithm string `json:"algorithm" yaml:"algorithm"`
	Secret    string `json:"-" yaml:"-"`
}

// RateLimitConfig controls the distributed per-client Token Bucket.
type RateLimitConfig struct {
	RequestsPerSecond float64       `json:"requests_per_second" yaml:"requests_per_second"`
	Burst             int64         `json:"burst" yaml:"burst"`
	KeyPrefix         string        `json:"key_prefix" yaml:"key_prefix"`
	StateTTL          time.Duration `json:"state_ttl" yaml:"state_ttl"`
	OperationTimeout  time.Duration `json:"operation_timeout" yaml:"operation_timeout"`
}

// RedisConfig controls the Redis connection pool used by the rate limiter.
type RedisConfig struct {
	Address            string        `json:"address" yaml:"address"`
	Username           string        `json:"-" yaml:"-"`
	Password           string        `json:"-" yaml:"-"`
	Database           int           `json:"database" yaml:"database"`
	PoolSize           int           `json:"pool_size" yaml:"pool_size"`
	MinIdleConnections int           `json:"min_idle_connections" yaml:"min_idle_connections"`
	DialTimeout        time.Duration `json:"dial_timeout" yaml:"dial_timeout"`
	ReadTimeout        time.Duration `json:"read_timeout" yaml:"read_timeout"`
	WriteTimeout       time.Duration `json:"write_timeout" yaml:"write_timeout"`
}

// RouteConfig maps an inbound path prefix to one upstream service.
type RouteConfig struct {
	Prefix      string `json:"prefix" yaml:"prefix"`
	Upstream    string `json:"upstream" yaml:"upstream"`
	StripPrefix string `json:"strip_prefix" yaml:"strip_prefix"`
}

// ObservabilityConfig controls structured logging and optional profiling.
type ObservabilityConfig struct {
	LogLevel     string `json:"log_level" yaml:"log_level"`
	PprofEnabled bool   `json:"pprof_enabled" yaml:"pprof_enabled"`
}
