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
	if _, err := LoadFrom(lookup); err != nil {
		t.Fatalf("valid config error = %v", err)
	}
	values["HEALTH_TIMEOUT"] = "invalid"
	if _, err := LoadFrom(lookup); err == nil || !strings.Contains(err.Error(), "HEALTH_TIMEOUT must be greater than zero") {
		t.Fatalf("invalid timeout error = %v", err)
	}
}
