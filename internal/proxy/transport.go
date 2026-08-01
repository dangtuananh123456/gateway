package proxy

import (
	"fmt"
	"net"
	"net/http"
	"time"
)

// TransportConfig controls the shared upstream connection pool.
type TransportConfig struct {
	DialTimeout               time.Duration
	KeepAlive                 time.Duration
	MaxIdleConnections        int
	MaxIdleConnectionsPerHost int
	MaxConnectionsPerHost     int
	IdleConnectionTimeout     time.Duration
	TLSHandshakeTimeout       time.Duration
	ResponseHeaderTimeout     time.Duration
	ExpectContinueTimeout     time.Duration
	MaxResponseHeaderBytes    int64
}

// DefaultTransportConfig returns Phase 1 transport settings sized for high concurrency.
func DefaultTransportConfig(responseHeaderTimeout time.Duration) TransportConfig {
	return TransportConfig{
		DialTimeout:               time.Second,
		KeepAlive:                 30 * time.Second,
		MaxIdleConnections:        10_000,
		MaxIdleConnectionsPerHost: 5_000,
		MaxConnectionsPerHost:     0,
		IdleConnectionTimeout:     90 * time.Second,
		TLSHandshakeTimeout:       2 * time.Second,
		ResponseHeaderTimeout:     responseHeaderTimeout,
		ExpectContinueTimeout:     time.Second,
		MaxResponseHeaderBytes:    1 << 20,
	}
}

// NewTransport creates one reusable transport for every configured upstream.
func NewTransport(cfg TransportConfig) (*http.Transport, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	dialer := &net.Dialer{
		Timeout:   cfg.DialTimeout,
		KeepAlive: cfg.KeepAlive,
	}

	return &http.Transport{
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		DisableCompression:     true,
		MaxIdleConns:           cfg.MaxIdleConnections,
		MaxIdleConnsPerHost:    cfg.MaxIdleConnectionsPerHost,
		MaxConnsPerHost:        cfg.MaxConnectionsPerHost,
		IdleConnTimeout:        cfg.IdleConnectionTimeout,
		TLSHandshakeTimeout:    cfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout:  cfg.ResponseHeaderTimeout,
		ExpectContinueTimeout:  cfg.ExpectContinueTimeout,
		MaxResponseHeaderBytes: cfg.MaxResponseHeaderBytes,
	}, nil
}

func (cfg TransportConfig) validate() error {
	for field, value := range map[string]time.Duration{
		"dial_timeout":            cfg.DialTimeout,
		"keep_alive":              cfg.KeepAlive,
		"idle_connection_timeout": cfg.IdleConnectionTimeout,
		"tls_handshake_timeout":   cfg.TLSHandshakeTimeout,
		"response_header_timeout": cfg.ResponseHeaderTimeout,
		"expect_continue_timeout": cfg.ExpectContinueTimeout,
	} {
		if value <= 0 {
			return fmt.Errorf("proxy transport %s must be greater than zero", field)
		}
	}
	if cfg.MaxIdleConnections <= 0 {
		return fmt.Errorf("proxy transport max_idle_connections must be greater than zero")
	}
	if cfg.MaxIdleConnectionsPerHost <= 0 {
		return fmt.Errorf("proxy transport max_idle_connections_host must be greater than zero")
	}
	if cfg.MaxIdleConnectionsPerHost > cfg.MaxIdleConnections {
		return fmt.Errorf(
			"proxy transport max_idle_connections_host must not exceed max_idle_connections",
		)
	}
	if cfg.MaxConnectionsPerHost < 0 {
		return fmt.Errorf("proxy transport max_connections_host must not be negative")
	}
	if cfg.MaxResponseHeaderBytes <= 0 {
		return fmt.Errorf("proxy transport max_response_header_bytes must be greater than zero")
	}

	return nil
}
