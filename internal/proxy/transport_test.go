package proxy

import (
	"testing"
	"time"
)

func TestNewTransportUsesConfiguredPoolAndTimeouts(t *testing.T) {
	cfg := DefaultTransportConfig(3 * time.Second)

	transport, err := NewTransport(cfg)
	if err != nil {
		t.Fatalf("NewTransport(): %v", err)
	}
	defer transport.CloseIdleConnections()

	if transport.DialContext == nil {
		t.Error("DialContext = nil, want configured dialer")
	}
	if !transport.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 = false, want true")
	}
	if !transport.DisableCompression {
		t.Error("DisableCompression = false, want true")
	}
	if transport.MaxIdleConns != cfg.MaxIdleConnections {
		t.Errorf("MaxIdleConns = %d, want %d", transport.MaxIdleConns, cfg.MaxIdleConnections)
	}
	if transport.MaxIdleConnsPerHost != cfg.MaxIdleConnectionsPerHost {
		t.Errorf(
			"MaxIdleConnsPerHost = %d, want %d",
			transport.MaxIdleConnsPerHost,
			cfg.MaxIdleConnectionsPerHost,
		)
	}
	if transport.MaxConnsPerHost != cfg.MaxConnectionsPerHost {
		t.Errorf(
			"MaxConnsPerHost = %d, want %d",
			transport.MaxConnsPerHost,
			cfg.MaxConnectionsPerHost,
		)
	}
	if transport.ResponseHeaderTimeout != 3*time.Second {
		t.Errorf(
			"ResponseHeaderTimeout = %v, want 3s",
			transport.ResponseHeaderTimeout,
		)
	}
	if transport.MaxResponseHeaderBytes != cfg.MaxResponseHeaderBytes {
		t.Errorf(
			"MaxResponseHeaderBytes = %d, want %d",
			transport.MaxResponseHeaderBytes,
			cfg.MaxResponseHeaderBytes,
		)
	}
}

func TestNewTransportRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TransportConfig)
	}{
		{
			name: "zero dial timeout",
			mutate: func(cfg *TransportConfig) {
				cfg.DialTimeout = 0
			},
		},
		{
			name: "zero max idle connections",
			mutate: func(cfg *TransportConfig) {
				cfg.MaxIdleConnections = 0
			},
		},
		{
			name: "per-host idle exceeds global",
			mutate: func(cfg *TransportConfig) {
				cfg.MaxIdleConnectionsPerHost = cfg.MaxIdleConnections + 1
			},
		},
		{
			name: "negative max connections per host",
			mutate: func(cfg *TransportConfig) {
				cfg.MaxConnectionsPerHost = -1
			},
		},
		{
			name: "zero response header limit",
			mutate: func(cfg *TransportConfig) {
				cfg.MaxResponseHeaderBytes = 0
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultTransportConfig(3 * time.Second)
			tt.mutate(&cfg)

			if _, err := NewTransport(cfg); err == nil {
				t.Fatal("NewTransport() error = nil, want error")
			}
		})
	}
}
