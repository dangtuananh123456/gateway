// Package client provides a browser UI and an h2c bridge for testing the Gateway.
package client

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddress           = "127.0.0.1:8090"
	defaultGatewayURL        = "http://localhost:18080"
	defaultRequestTimeout    = 5 * time.Second
	defaultShutdownTimeout   = 5 * time.Second
	defaultReadHeaderTimeout = 2 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultMaxHeaderBytes    = 1 << 20
)

// Config controls the local test UI and its fixed Gateway target.
type Config struct {
	Address           string
	GatewayURL        string
	RequestTimeout    time.Duration
	ShutdownTimeout   time.Duration
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
}

// ConfigFromEnvironment loads optional CLIENT_* overrides and validates them.
func ConfigFromEnvironment() (Config, error) {
	cfg := Config{
		Address:           valueOrDefault(os.Getenv("CLIENT_ADDRESS"), defaultAddress),
		GatewayURL:        valueOrDefault(os.Getenv("CLIENT_GATEWAY_URL"), defaultGatewayURL),
		RequestTimeout:    defaultRequestTimeout,
		ShutdownTimeout:   defaultShutdownTimeout,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		IdleTimeout:       defaultIdleTimeout,
		MaxHeaderBytes:    defaultMaxHeaderBytes,
	}
	var err error
	if cfg.RequestTimeout, err = durationEnvironment("CLIENT_REQUEST_TIMEOUT", cfg.RequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = durationEnvironment("CLIENT_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ReadHeaderTimeout, err = durationEnvironment("CLIENT_READ_HEADER_TIMEOUT", cfg.ReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.IdleTimeout, err = durationEnvironment("CLIENT_IDLE_TIMEOUT", cfg.IdleTimeout); err != nil {
		return Config{}, err
	}
	if raw := os.Getenv("CLIENT_MAX_HEADER_BYTES"); raw != "" {
		cfg.MaxHeaderBytes, err = strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("CLIENT_MAX_HEADER_BYTES: %w", err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects unsafe or incomplete client configuration.
func (cfg Config) Validate() error {
	var validationErrors []error
	if cfg.Address == "" || cfg.Address != strings.TrimSpace(cfg.Address) {
		validationErrors = append(validationErrors, errors.New("client address must be non-empty without surrounding whitespace"))
	} else if _, port, err := net.SplitHostPort(cfg.Address); err != nil || port == "" {
		validationErrors = append(validationErrors, errors.New("client address must be a valid host:port"))
	} else if portNumber, err := strconv.Atoi(port); err != nil || portNumber < 1 || portNumber > 65535 {
		validationErrors = append(validationErrors, errors.New("client address port must be between 1 and 65535"))
	}
	parsedGateway, err := url.Parse(cfg.GatewayURL)
	if err != nil || parsedGateway.Scheme != "http" || parsedGateway.Host == "" ||
		parsedGateway.User != nil || parsedGateway.RawQuery != "" || parsedGateway.Fragment != "" ||
		(parsedGateway.Path != "" && parsedGateway.Path != "/") {
		validationErrors = append(validationErrors, errors.New("client Gateway URL must be an absolute http URL without path, query, user info, or fragment"))
	}
	for name, value := range map[string]time.Duration{
		"request timeout":     cfg.RequestTimeout,
		"shutdown timeout":    cfg.ShutdownTimeout,
		"read header timeout": cfg.ReadHeaderTimeout,
		"idle timeout":        cfg.IdleTimeout,
	} {
		if value <= 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client %s must be greater than zero", name))
		}
	}
	if cfg.MaxHeaderBytes <= 0 {
		validationErrors = append(validationErrors, errors.New("client max header bytes must be greater than zero"))
	}
	return errors.Join(validationErrors...)
}

func durationEnvironment(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return value, nil
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
