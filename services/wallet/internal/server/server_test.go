package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"digitalwallet/services/wallet/internal/config"
	"digitalwallet/shared/identifiers"
)

func TestHealthEndpointsAndRequestHeaders(t *testing.T) {
	cfg := config.Config{ServiceName: "wallet", InstanceName: "wallet-source", HTTPAddr: ":8080", HealthTimeout: time.Second}
	server := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{
		Postgres: func(context.Context) error { return nil },
		NATS:     func(context.Context) error { return nil },
	})

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("X-Request-ID") == "" || recorder.Header().Get("X-Correlation-ID") == "" {
		t.Fatalf("health response = %d, headers = %v", recorder.Code, recorder.Header())
	}

	request = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready response = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestReadyzReportsDependencyFailure(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":8080", HealthTimeout: time.Second}
	server := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{
		Postgres: func(context.Context) error { return errors.New("database offline") },
		NATS:     func(context.Context) error { return nil },
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready response = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestRequestHeadersArePropagated(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":8080", HealthTimeout: time.Second}
	server := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{
		Postgres: func(context.Context) error { return nil },
		NATS:     func(context.Context) error { return nil },
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "request-123")
	request.Header.Set("X-Correlation-ID", "correlation-456")
	server.Handler().ServeHTTP(recorder, request)
	if got := recorder.Header().Get("X-Request-ID"); got != "request-123" {
		t.Fatalf("request id = %q", got)
	}
	if got := recorder.Header().Get("X-Correlation-ID"); got != "correlation-456" {
		t.Fatalf("correlation id = %q", got)
	}
}
