// Package config loads and validates Gateway and PDU Session configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RoutingMode identifies the routing algorithm selected at startup.
type RoutingMode string

const (
	RoutingRoundRobin RoutingMode = "round_robin"
	RoutingWeighted   RoutingMode = "weighted"
	RoutingLoad       RoutingMode = "load"
)

// Config contains all configuration shared by the two Project 2 binaries.
type Config struct {
	Gateway   GatewayConfig   `yaml:"gateway"`
	PDU       PDUConfig       `yaml:"pdu"`
	Routing   RoutingConfig   `yaml:"routing"`
	Discovery DiscoveryConfig `yaml:"discovery"`
}

// HTTPServerConfig controls an HTTP server lifecycle and request limits.
type HTTPServerConfig struct {
	Address           string        `yaml:"address"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	MaxHeaderBytes    int           `yaml:"max_header_bytes"`
}

// GatewayConfig contains settings used only by the public Gateway.
type GatewayConfig struct {
	Server          HTTPServerConfig `yaml:"server"`
	UpstreamTimeout time.Duration    `yaml:"upstream_timeout"`
	PublicURL       string           `yaml:"public_url"`
}

// PDUConfig contains settings used by each PDU Session replica.
type PDUConfig struct {
	Server          HTTPServerConfig `yaml:"server"`
	InstanceID      string           `yaml:"instance_id"`
	Weight          int              `yaml:"weight"`
	ProcessingDelay time.Duration    `yaml:"processing_delay"`
}

// RoutingConfig selects one routing mode for the Gateway process.
type RoutingConfig struct {
	Mode RoutingMode `yaml:"mode"`
}

// DiscoveryConfig controls DNS discovery and backend state polling.
type DiscoveryConfig struct {
	Hostname        string        `yaml:"hostname"`
	Port            int           `yaml:"port"`
	PollInterval    time.Duration `yaml:"poll_interval"`
	LookupTimeout   time.Duration `yaml:"lookup_timeout"`
	HealthInterval  time.Duration `yaml:"health_interval"`
	HealthTimeout   time.Duration `yaml:"health_timeout"`
	MetricsInterval time.Duration `yaml:"metrics_interval"`
	MetricsTimeout  time.Duration `yaml:"metrics_timeout"`
	StaleTTL        time.Duration `yaml:"stale_ttl"`
	MaxConcurrency  int           `yaml:"max_concurrency"`
}

// Validate rejects invalid configuration before either service starts.
func (cfg Config) Validate() error {
	var validationErrors []error
	validationErrors = append(validationErrors, validateServer("gateway.server", cfg.Gateway.Server)...)
	validationErrors = append(validationErrors, validateServer("pdu.server", cfg.PDU.Server)...)

	if cfg.Gateway.UpstreamTimeout <= 0 {
		validationErrors = append(validationErrors, errors.New("gateway.upstream_timeout must be greater than zero"))
	}
	if err := validatePublicURL(cfg.Gateway.PublicURL); err != nil {
		validationErrors = append(validationErrors, err)
	}
	if cfg.PDU.Weight <= 0 {
		validationErrors = append(validationErrors, errors.New("pdu.weight must be greater than zero"))
	}
	if cfg.PDU.ProcessingDelay < 0 {
		validationErrors = append(validationErrors, errors.New("pdu.processing_delay must not be negative"))
	}
	if cfg.PDU.InstanceID != strings.TrimSpace(cfg.PDU.InstanceID) {
		validationErrors = append(validationErrors, errors.New("pdu.instance_id must not have surrounding whitespace"))
	}

	switch cfg.Routing.Mode {
	case RoutingRoundRobin, RoutingWeighted, RoutingLoad:
	default:
		validationErrors = append(validationErrors, fmt.Errorf(
			"routing.mode must be one of %q, %q, or %q",
			RoutingRoundRobin, RoutingWeighted, RoutingLoad,
		))
	}

	validationErrors = append(validationErrors, validateDiscovery(cfg.Discovery)...)
	return errors.Join(validationErrors...)
}

func validateServer(name string, cfg HTTPServerConfig) []error {
	var validationErrors []error
	if err := validateListenAddress(cfg.Address); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("%s.address: %w", name, err))
	}
	for field, value := range map[string]time.Duration{
		"read_header_timeout": cfg.ReadHeaderTimeout,
		"idle_timeout":        cfg.IdleTimeout,
		"shutdown_timeout":    cfg.ShutdownTimeout,
	} {
		if value <= 0 {
			validationErrors = append(validationErrors, fmt.Errorf("%s.%s must be greater than zero", name, field))
		}
	}
	if cfg.MaxHeaderBytes <= 0 {
		validationErrors = append(validationErrors, fmt.Errorf("%s.max_header_bytes must be greater than zero", name))
	}
	return validationErrors
}

func validateListenAddress(address string) error {
	if strings.TrimSpace(address) != address || address == "" {
		return errors.New("must be a non-empty host:port without surrounding whitespace")
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must be a valid host:port: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func validatePublicURL(rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("gateway.public_url must be a valid absolute URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.Host == "" {
		return errors.New("gateway.public_url must use http and include a host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("gateway.public_url must not include user info, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return errors.New("gateway.public_url path must be empty or /")
	}
	return nil
}

func validateDiscovery(cfg DiscoveryConfig) []error {
	var validationErrors []error
	if err := validateHostname(cfg.Hostname); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("discovery.hostname: %w", err))
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		validationErrors = append(validationErrors, errors.New("discovery.port must be between 1 and 65535"))
	}
	for field, value := range map[string]time.Duration{
		"poll_interval":    cfg.PollInterval,
		"lookup_timeout":   cfg.LookupTimeout,
		"health_interval":  cfg.HealthInterval,
		"health_timeout":   cfg.HealthTimeout,
		"metrics_interval": cfg.MetricsInterval,
		"metrics_timeout":  cfg.MetricsTimeout,
		"stale_ttl":        cfg.StaleTTL,
	} {
		if value <= 0 {
			validationErrors = append(validationErrors, fmt.Errorf("discovery.%s must be greater than zero", field))
		}
	}
	minimumTTL := max(cfg.PollInterval, cfg.HealthInterval, cfg.MetricsInterval)
	if cfg.StaleTTL > 0 && cfg.StaleTTL < minimumTTL {
		validationErrors = append(validationErrors, errors.New("discovery.stale_ttl must be at least the longest polling interval"))
	}
	if cfg.MaxConcurrency <= 0 {
		validationErrors = append(validationErrors, errors.New("discovery.max_concurrency must be greater than zero"))
	}
	return validationErrors
}

func validateHostname(hostname string) error {
	if hostname == "" || hostname != strings.TrimSpace(hostname) {
		return errors.New("must be non-empty and have no surrounding whitespace")
	}
	if strings.ContainsAny(hostname, "/:@") {
		return errors.New("must contain only a hostname or IP address, without scheme or port")
	}
	if net.ParseIP(hostname) != nil {
		return nil
	}
	if len(hostname) > 253 {
		return errors.New("DNS name is longer than 253 characters")
	}
	for _, label := range strings.Split(strings.TrimSuffix(hostname, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("must be a valid DNS hostname or IP address")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '-' {
				return errors.New("must be a valid DNS hostname or IP address")
			}
		}
	}
	return nil
}
