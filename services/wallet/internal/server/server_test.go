package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"digitalwallet/services/wallet/internal/config"
	"digitalwallet/services/wallet/internal/repository"
	"digitalwallet/shared/identifiers"
)

type fakeWalletStore struct {
	getWalletErr error
	verifyPage   repository.WalletVerificationPage
}

func (f fakeWalletStore) CreateWallet(context.Context, identifiers.UUID, string, string, string) (repository.CommandResult, error) {
	return repository.CommandResult{}, nil
}
func (f fakeWalletStore) GetWallet(context.Context, identifiers.UUID, identifiers.UUID) (repository.Wallet, error) {
	return repository.Wallet{}, f.getWalletErr
}
func (f fakeWalletStore) GetBalance(context.Context, identifiers.UUID, identifiers.UUID) (repository.BalanceResponse, error) {
	return repository.BalanceResponse{}, nil
}
func (f fakeWalletStore) ListEntries(context.Context, identifiers.UUID, identifiers.UUID, string, int32) (repository.EntryPage, error) {
	return repository.EntryPage{}, nil
}
func (f fakeWalletStore) Deposit(context.Context, identifiers.UUID, identifiers.UUID, string, string, int64, string) (repository.CommandResult, error) {
	return repository.CommandResult{}, nil
}
func (f fakeWalletStore) Withdraw(context.Context, identifiers.UUID, identifiers.UUID, string, string, int64, string) (repository.CommandResult, error) {
	return repository.CommandResult{}, nil
}
func (f fakeWalletStore) GetStats(context.Context) (repository.StatsResponse, error) {
	return repository.StatsResponse{
		Service:           "wallet",
		AttemptsHistogram: map[int32]int64{},
	}, nil
}
func (f fakeWalletStore) VerifyWallets(context.Context, identifiers.UUID, identifiers.UUID, int32) (repository.WalletVerificationPage, error) {
	if f.verifyPage.Wallets == nil {
		return repository.WalletVerificationPage{Wallets: []repository.WalletVerificationRow{}}, nil
	}
	return f.verifyPage, nil
}

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

func TestReadyzReportsNATSDegradedButReady(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":8080", HealthTimeout: time.Second}
	server := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{
		Postgres: func(context.Context) error { return nil },
		NATS:     func(context.Context) error { return errors.New("nats offline") },
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready response = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response healthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "degraded" || response.Checks["postgres"] != "ok" || response.Checks["nats"] != "nats offline" {
		t.Fatalf("ready response = %+v", response)
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

func TestTenantScopedWalletNotFoundReturns404(t *testing.T) {
	cfg := config.Config{HTTPAddr: ":8080", HealthTimeout: time.Second}
	server := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{}, fakeWalletStore{getWalletErr: repository.ErrNotFound})
	tenantID := identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f1111")
	walletID := identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f2222")
	request := httptest.NewRequest(http.MethodGet, "/v1/wallets/"+walletID.String(), nil)
	request.Header.Set("X-Tenant-ID", tenantID.String())
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestStatsEndpoint(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:         ":8080",
		InternalHTTPAddr: ":8085",
		InternalToken:    "test-secret-token",
		HealthTimeout:    time.Second,
	}
	server := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{}, fakeWalletStore{})

	// 1. Public listener must return 404 for /internal/*
	publicReq := httptest.NewRequest(http.MethodGet, "/internal/v1/stats", nil)
	publicRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(publicRec, publicReq)
	if publicRec.Code != http.StatusNotFound {
		t.Fatalf("public listener /internal/v1/stats status = %d, want 404; body = %s", publicRec.Code, publicRec.Body.String())
	}

	// 2. Internal listener rejects request with missing token
	noTokenReq := httptest.NewRequest(http.MethodGet, "/internal/v1/stats", nil)
	noTokenRec := httptest.NewRecorder()
	server.InternalHandler().ServeHTTP(noTokenRec, noTokenReq)
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
	validReq.Header.Set("X-Internal-Token", "test-secret-token")
	validRec := httptest.NewRecorder()
	server.InternalHandler().ServeHTTP(validRec, validReq)
	if validRec.Code != http.StatusOK {
		t.Fatalf("internal listener valid token status = %d, want 200; body = %s", validRec.Code, validRec.Body.String())
	}
	var resp repository.StatsResponse
	if err := json.Unmarshal(validRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal stats response: %v", err)
	}
	if resp.Service != "wallet" {
		t.Fatalf("service = %q, want wallet", resp.Service)
	}
}

func TestVerifyWalletsEndpointRequiresPairedCursorAndReturnsPage(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:         ":8080",
		InternalHTTPAddr: ":8085",
		InternalToken:    "test-secret-token",
		HealthTimeout:    time.Second,
	}
	tenantID := identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f1111")
	walletID := identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f2222")
	server := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{}, fakeWalletStore{
		verifyPage: repository.WalletVerificationPage{Wallets: []repository.WalletVerificationRow{{TenantID: tenantID, WalletID: walletID, Currency: "USD", PostedBalanceMinor: 12, AsOfVersion: 2}}},
	})

	missingCursorPart := httptest.NewRequest(http.MethodGet, "/internal/v1/wallets/verify?after_tenant_id="+tenantID.String(), nil)
	missingCursorPart.Header.Set("X-Internal-Token", cfg.InternalToken)
	missingCursorRec := httptest.NewRecorder()
	server.InternalHandler().ServeHTTP(missingCursorRec, missingCursorPart)
	if missingCursorRec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unpaired cursor status = %d, want 422", missingCursorRec.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/internal/v1/wallets/verify?limit=1", nil)
	request.Header.Set("X-Internal-Token", cfg.InternalToken)
	recorder := httptest.NewRecorder()
	server.InternalHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("verify status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var page repository.WalletVerificationPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Wallets) != 1 || page.Wallets[0].PostedBalanceMinor != 12 || page.Wallets[0].AsOfVersion != 2 {
		t.Fatalf("verification page = %+v", page)
	}
}
