package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultConfigPath is the local gateway YAML path.
	DefaultConfigPath = "config.yaml"
	// DefaultEnvPath is the local environment file path.
	DefaultEnvPath = ".env"

	envServerAddress           = "GATEWAY_SERVER_ADDRESS"
	envServerReadHeaderTimeout = "GATEWAY_SERVER_READ_HEADER_TIMEOUT"
	envServerIdleTimeout       = "GATEWAY_SERVER_IDLE_TIMEOUT"
	envServerShutdownTimeout   = "GATEWAY_SERVER_SHUTDOWN_TIMEOUT"
	envServerUpstreamTimeout   = "GATEWAY_SERVER_UPSTREAM_TIMEOUT"
	envServerMaxHeaderBytes    = "GATEWAY_SERVER_MAX_HEADER_BYTES"

	envAuthIssuer    = "GATEWAY_AUTH_ISSUER"
	envAuthAudience  = "GATEWAY_AUTH_AUDIENCE"
	envAuthAlgorithm = "GATEWAY_AUTH_ALGORITHM"
	envJWTSecret     = "GATEWAY_JWT_SECRET"

	envRateLimitRequestsPerSecond = "GATEWAY_RATE_LIMIT_REQUESTS_PER_SECOND"
	envRateLimitBurst             = "GATEWAY_RATE_LIMIT_BURST"
	envRateLimitKeyPrefix         = "GATEWAY_RATE_LIMIT_KEY_PREFIX"
	envRateLimitStateTTL          = "GATEWAY_RATE_LIMIT_STATE_TTL"
	envRateLimitOperationTimeout  = "GATEWAY_RATE_LIMIT_OPERATION_TIMEOUT"

	envRedisAddress            = "GATEWAY_REDIS_ADDRESS"
	envRedisUsername           = "GATEWAY_REDIS_USERNAME"
	envRedisPassword           = "GATEWAY_REDIS_PASSWORD"
	envRedisDatabase           = "GATEWAY_REDIS_DATABASE"
	envRedisPoolSize           = "GATEWAY_REDIS_POOL_SIZE"
	envRedisMinIdleConnections = "GATEWAY_REDIS_MIN_IDLE_CONNECTIONS"
	envRedisDialTimeout        = "GATEWAY_REDIS_DIAL_TIMEOUT"
	envRedisReadTimeout        = "GATEWAY_REDIS_READ_TIMEOUT"
	envRedisWriteTimeout       = "GATEWAY_REDIS_WRITE_TIMEOUT"

	envLogLevel     = "GATEWAY_LOG_LEVEL"
	envPprofEnabled = "GATEWAY_PPROF_ENABLED"
)

type envLookup func(string) (string, bool)

// Load reads one strict YAML document and applies .env and process overrides.
func Load(configPath string, envPaths ...string) (Config, error) {
	return loadFiles(configPath, envPaths, systemEnvironment)
}

// LoadDefault loads config.yaml and .env from the current working directory.
func LoadDefault() (Config, error) {
	return Load(DefaultConfigPath, DefaultEnvPath)
}

func loadFiles(configPath string, envPaths []string, processEnvironment envLookup) (Config, error) {
	fileEnvironment, err := readEnvironmentFiles(envPaths...)
	if err != nil {
		return Config{}, err
	}

	lookup := func(key string) (string, bool) {
		if value, ok := processEnvironment(key); ok {
			return value, true
		}

		value, ok := fileEnvironment[key]
		return value, ok
	}

	cfg, err := load(configPath, lookup)
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

func load(path string, lookup envLookup) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := requireSingleDocument(decoder); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := applyEnvironment(&cfg, lookup); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func requireSingleDocument(decoder *yaml.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return err
	default:
		return errors.New("multiple YAML documents are not supported")
	}
}

func applyEnvironment(cfg *Config, lookup envLookup) error {
	stringOverrides := []struct {
		key    string
		target *string
	}{
		{envServerAddress, &cfg.Server.Address},
		{envAuthIssuer, &cfg.Auth.Issuer},
		{envAuthAudience, &cfg.Auth.Audience},
		{envAuthAlgorithm, &cfg.Auth.Algorithm},
		{envJWTSecret, &cfg.Auth.Secret},
		{envRateLimitKeyPrefix, &cfg.RateLimit.KeyPrefix},
		{envRedisAddress, &cfg.Redis.Address},
		{envRedisUsername, &cfg.Redis.Username},
		{envRedisPassword, &cfg.Redis.Password},
		{envLogLevel, &cfg.Observability.LogLevel},
	}
	for _, item := range stringOverrides {
		if value, ok := lookup(item.key); ok {
			*item.target = value
		}
	}

	overrides := []func() error{
		func() error {
			return override(lookup, envServerReadHeaderTimeout, &cfg.Server.ReadHeaderTimeout, time.ParseDuration)
		},
		func() error {
			return override(lookup, envServerIdleTimeout, &cfg.Server.IdleTimeout, time.ParseDuration)
		},
		func() error {
			return override(lookup, envServerShutdownTimeout, &cfg.Server.ShutdownTimeout, time.ParseDuration)
		},
		func() error {
			return override(lookup, envServerUpstreamTimeout, &cfg.Server.UpstreamTimeout, time.ParseDuration)
		},
		func() error {
			return override(lookup, envServerMaxHeaderBytes, &cfg.Server.MaxHeaderBytes, parseInt)
		},
		func() error {
			return override(
				lookup,
				envRateLimitRequestsPerSecond,
				&cfg.RateLimit.RequestsPerSecond,
				parseFloat64,
			)
		},
		func() error {
			return override(lookup, envRateLimitBurst, &cfg.RateLimit.Burst, parseInt64)
		},
		func() error {
			return override(lookup, envRateLimitStateTTL, &cfg.RateLimit.StateTTL, time.ParseDuration)
		},
		func() error {
			return override(
				lookup,
				envRateLimitOperationTimeout,
				&cfg.RateLimit.OperationTimeout,
				time.ParseDuration,
			)
		},
		func() error {
			return override(lookup, envRedisDatabase, &cfg.Redis.Database, parseInt)
		},
		func() error {
			return override(lookup, envRedisPoolSize, &cfg.Redis.PoolSize, parseInt)
		},
		func() error {
			return override(
				lookup,
				envRedisMinIdleConnections,
				&cfg.Redis.MinIdleConnections,
				parseInt,
			)
		},
		func() error {
			return override(lookup, envRedisDialTimeout, &cfg.Redis.DialTimeout, time.ParseDuration)
		},
		func() error {
			return override(lookup, envRedisReadTimeout, &cfg.Redis.ReadTimeout, time.ParseDuration)
		},
		func() error {
			return override(lookup, envRedisWriteTimeout, &cfg.Redis.WriteTimeout, time.ParseDuration)
		},
		func() error {
			return override(lookup, envPprofEnabled, &cfg.Observability.PprofEnabled, strconv.ParseBool)
		},
	}

	for _, apply := range overrides {
		if err := apply(); err != nil {
			return err
		}
	}

	return nil
}

func override[T any](
	lookup envLookup,
	key string,
	target *T,
	parse func(string) (T, error),
) error {
	value, ok := lookup(key)
	if !ok {
		return nil
	}

	parsed, err := parse(value)
	if err != nil {
		return fmt.Errorf("parse environment variable %s: %w", key, err)
	}
	*target = parsed

	return nil
}

func parseInt(value string) (int, error) {
	return strconv.Atoi(value)
}

func parseInt64(value string) (int64, error) {
	return strconv.ParseInt(value, 10, 64)
}

func parseFloat64(value string) (float64, error) {
	return strconv.ParseFloat(value, 64)
}
