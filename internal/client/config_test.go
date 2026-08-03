package client

import (
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnvironmentDefaultsAndOverrides(t *testing.T) {
	for _, name := range []string{
		"CLIENT_ADDRESS", "CLIENT_GATEWAY_URL", "CLIENT_REQUEST_TIMEOUT",
		"CLIENT_SHUTDOWN_TIMEOUT", "CLIENT_READ_HEADER_TIMEOUT", "CLIENT_IDLE_TIMEOUT",
		"CLIENT_MAX_HEADER_BYTES",
	} {
		t.Setenv(name, "")
	}
	cfg, err := ConfigFromEnvironment()
	if err != nil {
		t.Fatalf("ConfigFromEnvironment() error = %v", err)
	}
	if cfg.Address != defaultAddress || cfg.GatewayURL != defaultGatewayURL ||
		cfg.RequestTimeout != defaultRequestTimeout {
		t.Errorf("default config = %+v", cfg)
	}

	t.Setenv("CLIENT_ADDRESS", "127.0.0.1:19090")
	t.Setenv("CLIENT_GATEWAY_URL", "http://gateway:8080")
	t.Setenv("CLIENT_REQUEST_TIMEOUT", "750ms")
	t.Setenv("CLIENT_SHUTDOWN_TIMEOUT", "2s")
	t.Setenv("CLIENT_READ_HEADER_TIMEOUT", "1s")
	t.Setenv("CLIENT_IDLE_TIMEOUT", "30s")
	t.Setenv("CLIENT_MAX_HEADER_BYTES", "4096")
	cfg, err = ConfigFromEnvironment()
	if err != nil {
		t.Fatalf("overridden ConfigFromEnvironment() error = %v", err)
	}
	if cfg.Address != "127.0.0.1:19090" || cfg.GatewayURL != "http://gateway:8080" ||
		cfg.RequestTimeout != 750*time.Millisecond || cfg.MaxHeaderBytes != 4096 {
		t.Errorf("overridden config = %+v", cfg)
	}
}

func TestConfigFromEnvironmentRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name, variable, value string
	}{
		{name: "duration", variable: "CLIENT_REQUEST_TIMEOUT", value: "soon"},
		{name: "header bytes", variable: "CLIENT_MAX_HEADER_BYTES", value: "many"},
		{name: "Gateway URL", variable: "CLIENT_GATEWAY_URL", value: "https://gateway"},
		{name: "listen port", variable: "CLIENT_ADDRESS", value: "127.0.0.1:0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, name := range []string{
				"CLIENT_ADDRESS", "CLIENT_GATEWAY_URL", "CLIENT_REQUEST_TIMEOUT",
				"CLIENT_SHUTDOWN_TIMEOUT", "CLIENT_READ_HEADER_TIMEOUT", "CLIENT_IDLE_TIMEOUT",
				"CLIENT_MAX_HEADER_BYTES",
			} {
				t.Setenv(name, "")
			}
			t.Setenv(test.variable, test.value)
			if _, err := ConfigFromEnvironment(); err == nil {
				t.Fatal("ConfigFromEnvironment() error = nil, want validation error")
			}
		})
	}
}

func TestConfigValidateCollectsErrors(t *testing.T) {
	err := (Config{}).Validate()
	if err == nil {
		t.Fatal("Validate() error = nil")
	}
	for _, detail := range []string{"address", "Gateway URL", "request timeout", "max header bytes"} {
		if !strings.Contains(err.Error(), detail) {
			t.Errorf("Validate() error %q does not contain %q", err, detail)
		}
	}
}
