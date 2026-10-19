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
	if cfg.StreamName != "WALLET_EVENTS" || cfg.ConsumerName != "projector" || cfg.GapWindow <= 0 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
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
