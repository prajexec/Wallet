package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"digitalwallet/services/transfer/internal/config"
	"digitalwallet/services/transfer/internal/repository"
	"digitalwallet/shared/identifiers"
)

type fakeTransferStore struct {
	createFn func(ctx context.Context, tenantID identifiers.UUID, key, requestHash string, input repository.Transfer) (repository.Transfer, bool, error)
	getFn    func(ctx context.Context, tenantID, transferID identifiers.UUID) (repository.Transfer, error)
}

func (f fakeTransferStore) Create(ctx context.Context, tenantID identifiers.UUID, key, requestHash string, input repository.Transfer) (repository.Transfer, bool, error) {
	if f.createFn != nil {
		return f.createFn(ctx, tenantID, key, requestHash, input)
	}
	return repository.Transfer{}, false, nil
}

func (f fakeTransferStore) Get(ctx context.Context, tenantID, transferID identifiers.UUID) (repository.Transfer, error) {
	if f.getFn != nil {
		return f.getFn(ctx, tenantID, transferID)
	}
	return repository.Transfer{}, nil
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

func TestTransferValidation(t *testing.T) {
	ids := identifiers.NewDeterministicGenerator()
	tenantID, _ := ids.Generate()
	sourceID, _ := ids.Generate()
	destID, _ := ids.Generate()

	server := New(config.Config{HTTPAddr: ":8080"}, slog.New(slog.NewTextHandler(io.Discard, nil)), ids, Dependencies{}, fakeTransferStore{})

	cases := []struct {
		name       string
		tenant     string
		key        string
		body       string
		wantStatus int
	}{
		{
			name:       "missing tenant header",
			tenant:     "",
			key:        "key-1",
			body:       `{"source_wallet_id":"` + sourceID.String() + `","destination_wallet_id":"` + destID.String() + `","amount_minor":500,"currency":"USD"}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "missing idempotency key",
			tenant:     tenantID.String(),
			key:        "",
			body:       `{"source_wallet_id":"` + sourceID.String() + `","destination_wallet_id":"` + destID.String() + `","amount_minor":500,"currency":"USD"}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "malformed body",
			tenant:     tenantID.String(),
			key:        "key-1",
			body:       `{"invalid_json`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "same source and destination wallet",
			tenant:     tenantID.String(),
			key:        "key-1",
			body:       `{"source_wallet_id":"` + sourceID.String() + `","destination_wallet_id":"` + sourceID.String() + `","amount_minor":500,"currency":"USD"}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "non-USD currency",
			tenant:     tenantID.String(),
			key:        "key-1",
			body:       `{"source_wallet_id":"` + sourceID.String() + `","destination_wallet_id":"` + destID.String() + `","amount_minor":500,"currency":"EUR"}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "zero amount",
			tenant:     tenantID.String(),
			key:        "key-1",
			body:       `{"source_wallet_id":"` + sourceID.String() + `","destination_wallet_id":"` + destID.String() + `","amount_minor":0,"currency":"USD"}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/transfers", strings.NewReader(tc.body))
			if tc.tenant != "" {
				req.Header.Set("X-Tenant-ID", tc.tenant)
			}
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rec := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestTransferCreationAndReplay(t *testing.T) {
	ids := identifiers.NewDeterministicGenerator()
	tenantID, _ := ids.Generate()
	transferID, _ := ids.Generate()
	sourceID, _ := ids.Generate()
	destID, _ := ids.Generate()

	var transferStarted atomic.Bool
	store := fakeTransferStore{
		createFn: func(ctx context.Context, tenant identifiers.UUID, key, hash string, in repository.Transfer) (repository.Transfer, bool, error) {
			if key == "replay-key" {
				return repository.Transfer{
					TenantID:            tenant,
					TransferID:          transferID,
					SourceWalletID:      sourceID,
					DestinationWalletID: destID,
					AmountMinor:         500,
					Currency:            "USD",
					State:               "COMPLETED",
					Version:             2,
				}, true, nil
			}
			return repository.Transfer{
				TenantID:            tenant,
				TransferID:          transferID,
				SourceWalletID:      sourceID,
				DestinationWalletID: destID,
				AmountMinor:         500,
				Currency:            "USD",
				State:               "CREATED",
				Version:             1,
			}, false, nil
		},
	}

	server := New(config.Config{HTTPAddr: ":8080"}, slog.New(slog.NewTextHandler(io.Discard, nil)), ids, Dependencies{
		StartTransfer: func(ctx context.Context, tenant, id identifiers.UUID) {
			transferStarted.Store(true)
		},
	}, store)

	// 1. Initial creation
	body := `{"source_wallet_id":"` + sourceID.String() + `","destination_wallet_id":"` + destID.String() + `","amount_minor":500,"currency":"USD"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/transfers", strings.NewReader(body))
	req.Header.Set("X-Tenant-ID", tenantID.String())
	req.Header.Set("Idempotency-Key", "initial-key")
	rec := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("initial create status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Idempotency-Replayed") == "true" {
		t.Fatalf("unexpected Idempotency-Replayed header on initial create")
	}
	time.Sleep(10 * time.Millisecond)
	if !transferStarted.Load() {
		t.Fatalf("expected StartTransfer to be called")
	}

	// 2. Replayed creation
	req = httptest.NewRequest(http.MethodPost, "/v1/transfers", strings.NewReader(body))
	req.Header.Set("X-Tenant-ID", tenantID.String())
	req.Header.Set("Idempotency-Key", "replay-key")
	rec = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("replay create status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("expected Idempotency-Replayed = true")
	}
}

func TestTransferIdempotencyConflict(t *testing.T) {
	ids := identifiers.NewDeterministicGenerator()
	tenantID, _ := ids.Generate()
	sourceID, _ := ids.Generate()
	destID, _ := ids.Generate()

	store := fakeTransferStore{
		createFn: func(ctx context.Context, tenant identifiers.UUID, key, hash string, in repository.Transfer) (repository.Transfer, bool, error) {
			return repository.Transfer{}, false, repository.ErrIdempotencyConflict
		},
	}

	server := New(config.Config{HTTPAddr: ":8080"}, slog.New(slog.NewTextHandler(io.Discard, nil)), ids, Dependencies{}, store)

	body := `{"source_wallet_id":"` + sourceID.String() + `","destination_wallet_id":"` + destID.String() + `","amount_minor":500,"currency":"USD"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/transfers", strings.NewReader(body))
	req.Header.Set("X-Tenant-ID", tenantID.String())
	req.Header.Set("Idempotency-Key", "conflict-key")
	rec := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409; body = %s", rec.Code, rec.Body.String())
	}
}

func TestGetTransfer(t *testing.T) {
	ids := identifiers.NewDeterministicGenerator()
	tenantID, _ := ids.Generate()
	transferID, _ := ids.Generate()
	missingID, _ := ids.Generate()

	store := fakeTransferStore{
		getFn: func(ctx context.Context, tenant, id identifiers.UUID) (repository.Transfer, error) {
			if id == missingID {
				return repository.Transfer{}, repository.ErrNotFound
			}
			return repository.Transfer{
				TenantID:   tenant,
				TransferID: id,
				State:      "COMPLETED",
			}, nil
		},
	}

	server := New(config.Config{HTTPAddr: ":8080"}, slog.New(slog.NewTextHandler(io.Discard, nil)), ids, Dependencies{}, store)

	// 1. Found transfer
	req := httptest.NewRequest(http.MethodGet, "/v1/transfers/"+transferID.String(), nil)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	rec := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var out repository.Transfer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.State != "COMPLETED" {
		t.Fatalf("state = %s, want COMPLETED", out.State)
	}

	// 2. Not found
	req = httptest.NewRequest(http.MethodGet, "/v1/transfers/"+missingID.String(), nil)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	rec = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}
