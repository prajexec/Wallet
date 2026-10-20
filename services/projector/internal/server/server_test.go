package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"digitalwallet/services/projector/internal/config"
	"digitalwallet/services/projector/internal/projection"
	"digitalwallet/shared/identifiers"
)

type fakeReadStore struct {
	balance projection.BalanceRead
	page    projection.EntryPage
	err     error
}

func (f fakeReadStore) GetBalance(context.Context, identifiers.UUID, identifiers.UUID) (projection.BalanceRead, error) {
	return f.balance, f.err
}

func (f fakeReadStore) ListEntries(context.Context, identifiers.UUID, identifiers.UUID, int64, int32) (projection.EntryPage, error) {
	return f.page, f.err
}

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

func TestReadModelEndpointsSupportConsistencyAndPagination(t *testing.T) {
	tenantID := identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f1111")
	walletID := identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f2222")
	server := New(config.Config{HTTPAddr: ":8080", HealthTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{
		Read: fakeReadStore{
			balance: projection.BalanceRead{WalletID: walletID, Currency: "USD", PostedBalanceMinor: 15, AvailableBalanceMinor: 15, ReadModelConsistentThrough: 3, ProjectionSlot: "a", UpdatedAt: time.Unix(1, 0).UTC()},
			page:    projection.EntryPage{Entries: []projection.EntryRead{{EntryVersion: 1, AggregateVersion: 2, Currency: "USD", Side: "CREDIT", AmountMinor: 15}}, ProjectionSlot: "a", ReadModelConsistentThrough: 3},
		},
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/read/wallets/"+walletID.String()+"/balance?min_version=3&wait_ms=0", nil)
	request.Header.Set("X-Tenant-ID", tenantID.String())
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"authoritative":false`) || !strings.Contains(recorder.Body.String(), `"projection_slot":"a"`) {
		t.Fatalf("balance response status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	behind := httptest.NewRequest(http.MethodGet, "/v1/read/wallets/"+walletID.String()+"/balance?min_version=4&wait_ms=0", nil)
	behind.Header.Set("X-Tenant-ID", tenantID.String())
	behindRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(behindRecorder, behind)
	if behindRecorder.Code != http.StatusConflict || behindRecorder.Header().Get("Retry-After") != "1" {
		t.Fatalf("behind response status=%d headers=%v body=%s", behindRecorder.Code, behindRecorder.Header(), behindRecorder.Body.String())
	}

	entries := httptest.NewRequest(http.MethodGet, "/v1/read/wallets/"+walletID.String()+"/entries?after_version=0&limit=10", nil)
	entries.Header.Set("X-Tenant-ID", tenantID.String())
	entriesRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(entriesRecorder, entries)
	if entriesRecorder.Code != http.StatusOK || !strings.Contains(entriesRecorder.Body.String(), `"entries"`) || !strings.Contains(entriesRecorder.Body.String(), `"read_model_consistent_through":3`) {
		t.Fatalf("entries response status=%d body=%s", entriesRecorder.Code, entriesRecorder.Body.String())
	}
}
