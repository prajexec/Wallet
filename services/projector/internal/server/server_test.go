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
	server := New(config.Config{
		HTTPAddr:         ":8080",
		InternalHTTPAddr: ":8085",
		InternalToken:    "test-projector-token",
		HealthTimeout:    time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{
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

	// 1. Public listener must return 404 for /internal/*
	publicRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(publicRec, httptest.NewRequest(http.MethodGet, "/internal/v1/stats", nil))
	if publicRec.Code != http.StatusNotFound {
		t.Fatalf("public listener /internal/v1/stats status = %d, want 404; body = %s", publicRec.Code, publicRec.Body.String())
	}

	// 2. Internal listener rejects request with missing token
	noTokenRec := httptest.NewRecorder()
	server.InternalHandler().ServeHTTP(noTokenRec, httptest.NewRequest(http.MethodGet, "/internal/v1/stats", nil))
	if noTokenRec.Code != http.StatusUnauthorized {
		t.Fatalf("internal listener missing token status = %d, want 401; body = %s", noTokenRec.Code, noTokenRec.Body.String())
	}

	// 3. Internal listener rejects request with wrong token
	wrongTokenReq := httptest.NewRequest(http.MethodGet, "/internal/v1/stats", nil)
	wrongTokenReq.Header.Set("X-Internal-Token", "wrong-token")
	wrongTokenRec := httptest.NewRecorder()
	server.InternalHandler().ServeHTTP(wrongTokenRec, wrongTokenReq)
	if wrongTokenRec.Code != http.StatusUnauthorized {
		t.Fatalf("internal listener wrong token status = %d, want 401; body = %s", wrongTokenRec.Code, wrongTokenRec.Body.String())
	}

	// 4. Internal listener accepts request with correct token
	validReq := httptest.NewRequest(http.MethodGet, "/internal/v1/stats", nil)
	validReq.Header.Set("X-Internal-Token", "test-projector-token")
	validRec := httptest.NewRecorder()
	server.InternalHandler().ServeHTTP(validRec, validReq)
	if validRec.Code != http.StatusOK {
		t.Fatalf("internal listener valid token status = %d, want 200; body = %s", validRec.Code, validRec.Body.String())
	}
}
