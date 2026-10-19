package config

import (
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) LookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://wallet:secret@postgres:5432/wallet_source?sslmode=disable",
		"NATS_URL":     "nats://nats:4222",
	}
}

func TestLoadFrom(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr string
	}{
		{name: "valid defaults"},
		{name: "missing database", mutate: func(values map[string]string) { delete(values, "DATABASE_URL") }, wantErr: "DATABASE_URL is required"},
		{name: "bad nats URL", mutate: func(values map[string]string) { values["NATS_URL"] = "http://nats:4222" }, wantErr: "NATS_URL must be a valid URL"},
		{name: "bad timeout", mutate: func(values map[string]string) { values["SHUTDOWN_TIMEOUT"] = "nope" }, wantErr: "SHUTDOWN_TIMEOUT must be greater than zero"},
		{name: "bad outbox batch", mutate: func(values map[string]string) { values["OUTBOX_BATCH_SIZE"] = "0" }, wantErr: "OUTBOX_BATCH_SIZE must be greater than zero"},
		{name: "bad outbox publish timeout", mutate: func(values map[string]string) { values["OUTBOX_PUBLISH_TIMEOUT"] = "nope" }, wantErr: "OUTBOX_PUBLISH_TIMEOUT must be greater than zero"},
		{name: "lease shorter than publish timeout", mutate: func(values map[string]string) { values["OUTBOX_LEASE"] = "1s" }, wantErr: "OUTBOX_LEASE must be greater than"},
		{name: "bad log level", mutate: func(values map[string]string) { values["LOG_LEVEL"] = "trace" }, wantErr: "LOG_LEVEL must be one of"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := validEnv()
			if test.mutate != nil {
				test.mutate(values)
			}
			got, err := LoadFrom(env(values))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("LoadFrom() error = %v", err)
				}
				if got.ServiceName != "wallet" || got.InstanceName != "wallet-source" || got.HealthTimeout != 2*time.Second || got.OutboxBatchSize != 50 || got.OutboxPollInterval != 250*time.Millisecond || got.OutboxLease != 5*time.Second || got.OutboxPublish != 2*time.Second {
					t.Fatalf("unexpected defaults: %+v", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("LoadFrom() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}
