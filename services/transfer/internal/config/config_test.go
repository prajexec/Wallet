package config

import (
	"strings"
	"testing"
)

func TestLoadFromValidation(t *testing.T) {
	base := map[string]string{
		"DATABASE_URL": "postgres://transfer:secret@postgres:5432/transfer?sslmode=disable",
		"NATS_URL":     "nats://nats:4222",
	}
	lookup := func(values map[string]string) LookupFunc {
		return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	}
	if _, err := LoadFrom(lookup(base)); err != nil {
		t.Fatalf("valid config error = %v", err)
	}
	delete(base, "DATABASE_URL")
	if _, err := LoadFrom(lookup(base)); err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("missing database error = %v", err)
	}
}
