package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// LookupFunc is injectable so configuration validation can be tested without
// mutating the process environment.
type LookupFunc func(string) (string, bool)

// Config is the wallet process configuration. The source and destination
// instances use the same binary with different INSTANCE_NAME and database
// credentials/URLs.
type Config struct {
	ServiceName     string
	InstanceName    string
	HTTPAddr        string
	DatabaseURL     string
	NATSURL         string
	ShutdownTimeout time.Duration
	HealthTimeout   time.Duration
	LogLevel        string
}

// Load reads and validates the process environment.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom validates a configuration source. Required values are explicit so
// a container fails at startup instead of running with an unusable dependency.
func LoadFrom(lookup LookupFunc) (Config, error) {
	if lookup == nil {
		return Config{}, fmt.Errorf("configuration lookup is nil")
	}

	config := Config{
		ServiceName:     valueOr(lookup, "SERVICE_NAME", "wallet"),
		InstanceName:    valueOr(lookup, "INSTANCE_NAME", "wallet-source"),
		HTTPAddr:        valueOr(lookup, "HTTP_ADDR", ":8080"),
		ShutdownTimeout: durationOr(lookup, "SHUTDOWN_TIMEOUT", 10*time.Second),
		HealthTimeout:   durationOr(lookup, "HEALTH_TIMEOUT", 2*time.Second),
		LogLevel:        strings.ToLower(valueOr(lookup, "LOG_LEVEL", "info")),
	}
	config.DatabaseURL, _ = lookup("DATABASE_URL")
	config.NATSURL, _ = lookup("NATS_URL")

	var problems []string
	if strings.TrimSpace(config.ServiceName) == "" {
		problems = append(problems, "SERVICE_NAME must not be empty")
	}
	if strings.TrimSpace(config.InstanceName) == "" {
		problems = append(problems, "INSTANCE_NAME must not be empty")
	}
	if strings.TrimSpace(config.HTTPAddr) == "" {
		problems = append(problems, "HTTP_ADDR must not be empty")
	}
	if err := validateURL("DATABASE_URL", config.DatabaseURL, map[string]bool{"postgres": true, "postgresql": true}); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateURL("NATS_URL", config.NATSURL, map[string]bool{"nats": true, "tls": true}); err != nil {
		problems = append(problems, err.Error())
	}
	if config.ShutdownTimeout <= 0 {
		problems = append(problems, "SHUTDOWN_TIMEOUT must be greater than zero")
	}
	if config.HealthTimeout <= 0 {
		problems = append(problems, "HEALTH_TIMEOUT must be greater than zero")
	}
	if !map[string]bool{"debug": true, "info": true, "warn": true, "error": true}[config.LogLevel] {
		problems = append(problems, "LOG_LEVEL must be one of debug, info, warn, error")
	}
	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid wallet configuration: %s", strings.Join(problems, "; "))
	}
	return config, nil
}

func valueOr(lookup LookupFunc, key, fallback string) string {
	if value, ok := lookup(key); ok {
		return value
	}
	return fallback
}

func durationOr(lookup LookupFunc, key string, fallback time.Duration) time.Duration {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0
	}
	return parsed
}

func validateURL(name, value string, schemes map[string]bool) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || !schemes[parsed.Scheme] {
		return fmt.Errorf("%s must be a valid URL with scheme %s", name, strings.Join(sortedKeys(schemes), " or "))
	}
	return nil
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	if len(keys) == 2 && keys[0] > keys[1] {
		keys[0], keys[1] = keys[1], keys[0]
	}
	return keys
}

// ParseBool is kept local to make future boolean service settings use the same
// clear environment error style without exposing a broad shared config API.
func ParseBool(value string) (bool, error) {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid boolean %q: %w", value, err)
	}
	return parsed, nil
}
