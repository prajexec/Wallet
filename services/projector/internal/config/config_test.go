package config

import (
	"strings"
	"testing"
)

func TestLoadFromValidation(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL": "postgres://projector:secret@postgres:5432/read?sslmode=disable",
		"NATS_URL":     "nats://nats:4222",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := LoadFrom(lookup)
	if err != nil {
		t.Fatalf("valid config error = %v", err)
	}
	if cfg.StreamName != "WALLET_EVENTS" || cfg.ConsumerName != "projector" || cfg.HTTPAddr != ":8080" || cfg.InternalHTTPAddr != ":8085" || cfg.InternalToken != "internal-dev-token" || cfg.GapWindow <= 0 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	values["INTERNAL_HTTP_ADDR"] = " "
	if _, err := LoadFrom(lookup); err == nil || !strings.Contains(err.Error(), "INTERNAL_HTTP_ADDR must not be empty") {
		t.Fatalf("empty internal http addr error = %v", err)
	}
	delete(values, "INTERNAL_HTTP_ADDR")
	values["INTERNAL_TOKEN"] = " "
	if _, err := LoadFrom(lookup); err == nil || !strings.Contains(err.Error(), "INTERNAL_TOKEN must not be empty") {
		t.Fatalf("empty internal token error = %v", err)
	}
	delete(values, "INTERNAL_TOKEN")
	values["HEALTH_TIMEOUT"] = "invalid"
	if _, err := LoadFrom(lookup); err == nil || !strings.Contains(err.Error(), "HEALTH_TIMEOUT must be greater than zero") {
		t.Fatalf("invalid timeout error = %v", err)
	}
	delete(values, "HEALTH_TIMEOUT")
	values["NATS_STREAM"] = ""
	if _, err := LoadFrom(lookup); err == nil || !strings.Contains(err.Error(), "NATS_STREAM must not be empty") {
		t.Fatalf("invalid stream name error = %v", err)
	}
	values["NATS_STREAM"] = "WALLET_EVENTS"
	values["ORDERING_GAP_WINDOW"] = "invalid"
	if _, err := LoadFrom(lookup); err == nil || !strings.Contains(err.Error(), "ORDERING_GAP_WINDOW must be greater than zero") {
		t.Fatalf("invalid gap window error = %v", err)
	}
}
