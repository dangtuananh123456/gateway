package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dangtuananh123456/gateway/pkg/constants"
	"gopkg.in/yaml.v3"
)

type envLookup func(string) (string, bool)

// LoadDefault reads CONFIG_PATH and ENV_PATH from the process environment.
// Missing variables default to config.yaml and .env in the working directory.
func LoadDefault() (Config, error) {
	configPath := valueOrDefault(os.Getenv("CONFIG_PATH"), constants.DefaultConfigPath)
	envPath := valueOrDefault(os.Getenv("ENV_PATH"), constants.DefaultEnvPath)
	return Load(configPath, envPath)
}

// Load reads strict YAML, overlays an optional dotenv file, overlays process
// environment variables, and validates the final typed configuration.
func Load(configPath, envPath string) (Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("read config file %q: %w", configPath, err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config file %q: %w", configPath, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, fmt.Errorf("decode config file %q: multiple YAML documents are not allowed", configPath)
		}
		return Config{}, fmt.Errorf("decode config file %q: %w", configPath, err)
	}

	fileEnvironment, err := readDotEnv(envPath)
	if err != nil {
		return Config{}, err
	}
	lookup := func(key string) (string, bool) {
		if value, found := os.LookupEnv(key); found {
			return value, true
		}
		value, found := fileEnvironment[key]
		return value, found
	}
	if err := applyEnvironment(&cfg, lookup); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}
	return cfg, nil
}

func readDotEnv(path string) (map[string]string, error) {
	values := make(map[string]string)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open environment file %q: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rawValue, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !validEnvironmentKey(key) {
			return nil, fmt.Errorf("parse environment file %q line %d: expected KEY=VALUE", path, lineNumber)
		}
		value, err := parseDotEnvValue(strings.TrimSpace(rawValue))
		if err != nil {
			return nil, fmt.Errorf("parse environment file %q line %d: %w", path, lineNumber, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read environment file %q: %w", path, err)
	}
	return values, nil
}

func parseDotEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] != '\'' && value[0] != '"' {
		return value, nil
	}
	if len(value) < 2 || value[len(value)-1] != value[0] {
		return "", errors.New("unterminated quoted value")
	}
	if value[0] == '\'' {
		return value[1 : len(value)-1], nil
	}
	unquoted, err := strconv.Unquote(value)
	if err != nil {
		return "", fmt.Errorf("invalid quoted value: %w", err)
	}
	return unquoted, nil
}

func validEnvironmentKey(key string) bool {
	if key == "" || (key[0] < 'A' || key[0] > 'Z') && key[0] != '_' {
		return false
	}
	for index := 1; index < len(key); index++ {
		character := key[index]
		if (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func applyEnvironment(cfg *Config, lookup envLookup) error {
	setters := map[string]func(string) error{
		"GATEWAY_ADDRESS":             stringSetter(&cfg.Gateway.Server.Address),
		"GATEWAY_READ_HEADER_TIMEOUT": durationSetter(&cfg.Gateway.Server.ReadHeaderTimeout),
		"GATEWAY_IDLE_TIMEOUT":        durationSetter(&cfg.Gateway.Server.IdleTimeout),
		"GATEWAY_SHUTDOWN_TIMEOUT":    durationSetter(&cfg.Gateway.Server.ShutdownTimeout),
		"GATEWAY_MAX_HEADER_BYTES":    intSetter(&cfg.Gateway.Server.MaxHeaderBytes),
		"GATEWAY_UPSTREAM_TIMEOUT":    durationSetter(&cfg.Gateway.UpstreamTimeout),
		"GATEWAY_PUBLIC_URL":          stringSetter(&cfg.Gateway.PublicURL),
		"PDU_ADDRESS":                 stringSetter(&cfg.PDU.Server.Address),
		"PDU_READ_HEADER_TIMEOUT":     durationSetter(&cfg.PDU.Server.ReadHeaderTimeout),
		"PDU_IDLE_TIMEOUT":            durationSetter(&cfg.PDU.Server.IdleTimeout),
		"PDU_SHUTDOWN_TIMEOUT":        durationSetter(&cfg.PDU.Server.ShutdownTimeout),
		"PDU_MAX_HEADER_BYTES":        intSetter(&cfg.PDU.Server.MaxHeaderBytes),
		"PDU_INSTANCE_ID":             stringSetter(&cfg.PDU.InstanceID),
		"PDU_WEIGHT":                  intSetter(&cfg.PDU.Weight),
		"PDU_PROCESSING_DELAY":        durationSetter(&cfg.PDU.ProcessingDelay),
		"ROUTING_MODE":                routingModeSetter(&cfg.Routing.Mode),
		"DISCOVERY_HOSTNAME":          stringSetter(&cfg.Discovery.Hostname),
		"DISCOVERY_PORT":              intSetter(&cfg.Discovery.Port),
		"DISCOVERY_POLL_INTERVAL":     durationSetter(&cfg.Discovery.PollInterval),
		"DISCOVERY_LOOKUP_TIMEOUT":    durationSetter(&cfg.Discovery.LookupTimeout),
		"DISCOVERY_HEALTH_INTERVAL":   durationSetter(&cfg.Discovery.HealthInterval),
		"DISCOVERY_HEALTH_TIMEOUT":    durationSetter(&cfg.Discovery.HealthTimeout),
		"DISCOVERY_METRICS_INTERVAL":  durationSetter(&cfg.Discovery.MetricsInterval),
		"DISCOVERY_METRICS_TIMEOUT":   durationSetter(&cfg.Discovery.MetricsTimeout),
		"DISCOVERY_STALE_TTL":         durationSetter(&cfg.Discovery.StaleTTL),
		"DISCOVERY_MAX_CONCURRENCY":   intSetter(&cfg.Discovery.MaxConcurrency),
	}
	for key, setter := range setters {
		if value, found := lookup(key); found {
			if err := setter(value); err != nil {
				return fmt.Errorf("environment variable %s: %w", key, err)
			}
		}
	}
	return nil
}

func stringSetter(destination *string) func(string) error {
	return func(value string) error {
		*destination = value
		return nil
	}
}

func durationSetter(destination *time.Duration) func(string) error {
	return func(value string) error {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("parse duration %q: %w", value, err)
		}
		*destination = parsed
		return nil
	}
}

func intSetter(destination *int) func(string) error {
	return func(value string) error {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse integer %q: %w", value, err)
		}
		*destination = parsed
		return nil
	}
}

func routingModeSetter(destination *constants.RoutingMode) func(string) error {
	return func(value string) error {
		*destination = constants.RoutingMode(value)
		return nil
	}
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
