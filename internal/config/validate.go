package config

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const supportedJWTAlgorithm = "HS256"

// Validate checks whether the configuration is safe to use at runtime.
func (cfg Config) Validate() error {
	var validation validationErrors

	validateServer(cfg.Server, &validation)
	validateAuth(cfg.Auth, &validation)
	validateRateLimit(cfg.RateLimit, &validation)
	validateRedis(cfg.Redis, &validation)
	validateRoutes(cfg.Routes, &validation)
	validateObservability(cfg.Observability, &validation)

	return validation.err()
}

func validateServer(cfg ServerConfig, validation *validationErrors) {
	validateHostPort("server.address", cfg.Address, true, validation)
	validatePositiveDuration("server.read_header_timeout", cfg.ReadHeaderTimeout, validation)
	validatePositiveDuration("server.idle_timeout", cfg.IdleTimeout, validation)
	validatePositiveDuration("server.shutdown_timeout", cfg.ShutdownTimeout, validation)
	validatePositiveDuration("server.upstream_timeout", cfg.UpstreamTimeout, validation)

	if cfg.MaxHeaderBytes <= 0 {
		validation.add("server.max_header_bytes must be greater than zero")
	}
}

func validateAuth(cfg AuthConfig, validation *validationErrors) {
	if strings.TrimSpace(cfg.Issuer) == "" {
		validation.add("auth.issuer must not be empty")
	}
	if strings.TrimSpace(cfg.Audience) == "" {
		validation.add("auth.audience must not be empty")
	}
	if cfg.Algorithm != supportedJWTAlgorithm {
		validation.addf(
			"auth.algorithm must be %q, got %q",
			supportedJWTAlgorithm,
			cfg.Algorithm,
		)
	}
	if strings.TrimSpace(cfg.Secret) == "" {
		validation.add("auth secret must be provided through GATEWAY_JWT_SECRET")
	}
}

func validateRateLimit(cfg RateLimitConfig, validation *validationErrors) {
	if cfg.RequestsPerSecond <= 0 ||
		math.IsNaN(cfg.RequestsPerSecond) ||
		math.IsInf(cfg.RequestsPerSecond, 0) {
		validation.add("rate_limit.requests_per_second must be a finite number greater than zero")
	}
	if cfg.Burst <= 0 {
		validation.add("rate_limit.burst must be greater than zero")
	}
	if strings.TrimSpace(cfg.KeyPrefix) == "" {
		validation.add("rate_limit.key_prefix must not be empty")
	}

	validatePositiveDuration("rate_limit.state_ttl", cfg.StateTTL, validation)
	validatePositiveDuration("rate_limit.operation_timeout", cfg.OperationTimeout, validation)
}

func validateRedis(cfg RedisConfig, validation *validationErrors) {
	validateHostPort("redis.address", cfg.Address, false, validation)

	if cfg.Database < 0 {
		validation.add("redis.database must not be negative")
	}
	if cfg.PoolSize <= 0 {
		validation.add("redis.pool_size must be greater than zero")
	}
	if cfg.MinIdleConnections < 0 {
		validation.add("redis.min_idle_connections must not be negative")
	}
	if cfg.PoolSize > 0 && cfg.MinIdleConnections > cfg.PoolSize {
		validation.add("redis.min_idle_connections must not exceed redis.pool_size")
	}

	validatePositiveDuration("redis.dial_timeout", cfg.DialTimeout, validation)
	validatePositiveDuration("redis.read_timeout", cfg.ReadTimeout, validation)
	validatePositiveDuration("redis.write_timeout", cfg.WriteTimeout, validation)
}

func validateRoutes(routes []RouteConfig, validation *validationErrors) {
	if len(routes) == 0 {
		validation.add("routes must contain at least one route")
		return
	}

	prefixes := make(map[string]int, len(routes))
	for index, route := range routes {
		field := fmt.Sprintf("routes[%d]", index)

		if !isCleanAbsolutePath(route.Prefix) {
			validation.addf("%s.prefix must be a clean absolute path", field)
		} else if previous, exists := prefixes[route.Prefix]; exists {
			validation.addf(
				"%s.prefix duplicates routes[%d].prefix %q",
				field,
				previous,
				route.Prefix,
			)
		} else {
			prefixes[route.Prefix] = index
		}

		validateUpstream(field+".upstream", route.Upstream, validation)

		if route.StripPrefix == "" {
			continue
		}
		if !isCleanAbsolutePath(route.StripPrefix) {
			validation.addf("%s.strip_prefix must be empty or a clean absolute path", field)
			continue
		}
		if isCleanAbsolutePath(route.Prefix) &&
			!hasPathPrefix(route.Prefix, route.StripPrefix) {
			validation.addf("%s.strip_prefix must be a path prefix of %s.prefix", field, field)
		}
	}
}

func validateObservability(cfg ObservabilityConfig, validation *validationErrors) {
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		validation.add(`observability.log_level must be one of "debug", "info", "warn", or "error"`)
	}
}

func validateHostPort(
	field string,
	value string,
	allowEmptyHost bool,
	validation *validationErrors,
) {
	if value == "" {
		validation.addf("%s must not be empty", field)
		return
	}
	if strings.TrimSpace(value) != value {
		validation.addf("%s must not contain surrounding whitespace", field)
		return
	}

	host, portValue, err := net.SplitHostPort(value)
	if err != nil {
		validation.addf("%s must use host:port format: %v", field, err)
		return
	}
	if !allowEmptyHost && strings.TrimSpace(host) == "" {
		validation.addf("%s host must not be empty", field)
	}

	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		validation.addf("%s port must be between 1 and 65535", field)
	}
}

func validatePositiveDuration(
	field string,
	value time.Duration,
	validation *validationErrors,
) {
	if value <= 0 {
		validation.addf("%s must be greater than zero", field)
	}
}

func validateUpstream(field string, value string, validation *validationErrors) {
	upstream, err := url.Parse(value)
	if err != nil {
		validation.addf("%s must be a valid URL: %v", field, err)
		return
	}
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		validation.addf("%s scheme must be http or https", field)
	}
	if upstream.Host == "" {
		validation.addf("%s must contain a host", field)
	}
	if upstream.User != nil {
		validation.addf("%s must not contain user information", field)
	}
	if upstream.RawQuery != "" {
		validation.addf("%s must not contain a query string", field)
	}
	if upstream.Fragment != "" {
		validation.addf("%s must not contain a fragment", field)
	}
}

func isCleanAbsolutePath(value string) bool {
	return strings.HasPrefix(value, "/") &&
		!strings.ContainsAny(value, "?#") &&
		path.Clean(value) == value
}

func hasPathPrefix(value string, prefix string) bool {
	return prefix == "/" ||
		value == prefix ||
		strings.HasPrefix(value, prefix+"/")
}

type validationErrors struct {
	items []error
}

func (validation *validationErrors) add(message string) {
	validation.items = append(validation.items, errors.New(message))
}

func (validation *validationErrors) addf(format string, args ...any) {
	validation.items = append(validation.items, fmt.Errorf(format, args...))
}

func (validation *validationErrors) err() error {
	return errors.Join(validation.items...)
}
