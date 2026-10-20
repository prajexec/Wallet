package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type LookupFunc func(string) (string, bool)

type Config struct {
	ServiceName         string
	InstanceName        string
	HTTPAddr            string
	InternalHTTPAddr    string
	InternalToken       string
	DatabaseURL         string
	NATSURL             string
	ShutdownTimeout     time.Duration
	HealthTimeout       time.Duration
	LogLevel            string
	StreamName          string
	ConsumerName        string
	GapWindow           time.Duration
	SubjectPrefix       string
	WalletVerifyURLs    string
	WalletVerifyToken   string
	VerificationTimeout time.Duration
}

func Load() (Config, error) { return LoadFrom(os.LookupEnv) }

func LoadFrom(lookup LookupFunc) (Config, error) {
	if lookup == nil {
		return Config{}, fmt.Errorf("configuration lookup is nil")
	}
	cfg := Config{
		ServiceName:         valueOr(lookup, "SERVICE_NAME", "projector"),
		InstanceName:        valueOr(lookup, "INSTANCE_NAME", "projector"),
		HTTPAddr:            valueOr(lookup, "HTTP_ADDR", ":8080"),
		InternalHTTPAddr:    valueOr(lookup, "INTERNAL_HTTP_ADDR", ":8085"),
		InternalToken:       valueOr(lookup, "INTERNAL_TOKEN", "internal-dev-token"),
		DatabaseURL:         valueOr(lookup, "DATABASE_URL", ""),
		NATSURL:             valueOr(lookup, "NATS_URL", ""),
		ShutdownTimeout:     durationOr(lookup, "SHUTDOWN_TIMEOUT", 10*time.Second),
		HealthTimeout:       durationOr(lookup, "HEALTH_TIMEOUT", 2*time.Second),
		LogLevel:            strings.ToLower(valueOr(lookup, "LOG_LEVEL", "info")),
		StreamName:          valueOr(lookup, "NATS_STREAM", "WALLET_EVENTS"),
		ConsumerName:        valueOr(lookup, "NATS_CONSUMER", "projector"),
		GapWindow:           durationOr(lookup, "ORDERING_GAP_WINDOW", 30*time.Second),
		SubjectPrefix:       valueOr(lookup, "NATS_SUBJECT_PREFIX", "events"),
		WalletVerifyURLs:    valueOr(lookup, "WALLET_VERIFY_URLS", ""),
		WalletVerifyToken:   valueOr(lookup, "WALLET_VERIFY_TOKEN", valueOr(lookup, "INTERNAL_TOKEN", "internal-dev-token")),
		VerificationTimeout: durationOr(lookup, "PROJECTION_VERIFICATION_TIMEOUT", 30*time.Second),
	}
	var problems []string
	if strings.TrimSpace(cfg.ServiceName) == "" {
		problems = append(problems, "SERVICE_NAME must not be empty")
	}
	if strings.TrimSpace(cfg.InstanceName) == "" {
		problems = append(problems, "INSTANCE_NAME must not be empty")
	}
	if strings.TrimSpace(cfg.HTTPAddr) == "" {
		problems = append(problems, "HTTP_ADDR must not be empty")
	}
	if strings.TrimSpace(cfg.InternalHTTPAddr) == "" {
		problems = append(problems, "INTERNAL_HTTP_ADDR must not be empty")
	}
	if strings.TrimSpace(cfg.InternalToken) == "" {
		problems = append(problems, "INTERNAL_TOKEN must not be empty")
	}
	if strings.TrimSpace(cfg.StreamName) == "" {
		problems = append(problems, "NATS_STREAM must not be empty")
	}
	if strings.TrimSpace(cfg.ConsumerName) == "" {
		problems = append(problems, "NATS_CONSUMER must not be empty")
	}
	if cfg.GapWindow <= 0 {
		problems = append(problems, "ORDERING_GAP_WINDOW must be greater than zero")
	}
	if strings.TrimSpace(cfg.WalletVerifyToken) == "" {
		problems = append(problems, "WALLET_VERIFY_TOKEN must not be empty")
	}
	if cfg.VerificationTimeout <= 0 {
		problems = append(problems, "PROJECTION_VERIFICATION_TIMEOUT must be greater than zero")
	}
	if err := validateURL("DATABASE_URL", cfg.DatabaseURL, "postgres", "postgresql"); err != nil {
		problems = append(problems, err.Error())
	}
	if err := validateURL("NATS_URL", cfg.NATSURL, "nats", "tls"); err != nil {
		problems = append(problems, err.Error())
	}
	if cfg.ShutdownTimeout <= 0 {
		problems = append(problems, "SHUTDOWN_TIMEOUT must be greater than zero")
	}
	if cfg.HealthTimeout <= 0 {
		problems = append(problems, "HEALTH_TIMEOUT must be greater than zero")
	}
	if !map[string]bool{"debug": true, "info": true, "warn": true, "error": true}[cfg.LogLevel] {
		problems = append(problems, "LOG_LEVEL must be one of debug, info, warn, error")
	}
	if len(problems) != 0 {
		return Config{}, fmt.Errorf("invalid projector configuration: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
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

func validateURL(name, value string, schemes ...string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || !contains(schemes, parsed.Scheme) {
		return fmt.Errorf("%s must be a valid URL with scheme %s", name, strings.Join(schemes, " or "))
	}
	return nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
