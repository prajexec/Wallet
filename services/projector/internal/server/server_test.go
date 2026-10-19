package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"digitalwallet/services/projector/internal/config"
	"digitalwallet/shared/identifiers"
)

func TestHealthAndReady(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":8080", HealthTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{
		Postgres: func(context.Context) error { return nil },
		NATS:     func(context.Context) error { return nil },
	})
	for _, path := range []string{"/healthz", "/readyz"} {
		recorder := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("X-Request-ID") == "" || recorder.Header().Get("X-Correlation-ID") == "" {
			t.Fatalf("%s missing request headers: %v", path, recorder.Header())
		}
	}
}

func TestStatsEndpoint(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":8080", HealthTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{
		Postgres: func(context.Context) error { return nil },
		NATS:     func(context.Context) error { return nil },
		Stats: func(context.Context) (StatsResponse, error) {
			return StatsResponse{
				Service:            "projector",
				BufferedEventCount: 0,
				OpenGapCount:       0,
				DeadLetterCount:    0,
			}, nil
		},
	})
	recorder := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/internal/v1/stats", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("stats status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
