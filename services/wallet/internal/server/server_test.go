package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
func (f fakeWalletStore) Reserve(context.Context, identifiers.UUID, identifiers.UUID, identifiers.UUID, repository.TransferMoneyCommand, string) (repository.CommandResult, error) {
	return repository.CommandResult{StatusCode: http.StatusCreated, Body: []byte(`{"status":"RESERVED"}`)}, nil
}
func (f fakeWalletStore) CreatePendingCredit(context.Context, identifiers.UUID, identifiers.UUID, identifiers.UUID, repository.TransferMoneyCommand, string) (repository.CommandResult, error) {
	return repository.CommandResult{StatusCode: http.StatusCreated, Body: []byte(`{"status":"PENDING"}`)}, nil
}
func (f fakeWalletStore) Capture(context.Context, identifiers.UUID, identifiers.UUID, identifiers.UUID, repository.TransferMoneyCommand, string) (repository.CommandResult, error) {
	return repository.CommandResult{StatusCode: http.StatusCreated, Body: []byte(`{"status":"CAPTURED"}`)}, nil
}
func (f fakeWalletStore) Finalize(context.Context, identifiers.UUID, identifiers.UUID, identifiers.UUID, repository.TransferMoneyCommand, string) (repository.CommandResult, error) {
	return repository.CommandResult{StatusCode: http.StatusCreated, Body: []byte(`{"status":"FINALIZED"}`)}, nil
}
func (f fakeWalletStore) Release(context.Context, identifiers.UUID, identifiers.UUID, identifiers.UUID, repository.TransferMoneyCommand, string) (repository.CommandResult, error) {
	return repository.CommandResult{StatusCode: http.StatusCreated, Body: []byte(`{"status":"RELEASED"}`)}, nil
}
func (f fakeWalletStore) Compensate(context.Context, identifiers.UUID, identifiers.UUID, identifiers.UUID, repository.TransferMoneyCommand, string) (repository.CommandResult, error) {
	return repository.CommandResult{StatusCode: http.StatusCreated, Body: []byte(`{"status":"COMPENSATED"}`)}, nil
}
func (f fakeWalletStore) GetOperation(context.Context, identifiers.UUID, identifiers.UUID) (repository.OperationResponse, error) {
	return repository.OperationResponse{}, nil
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

func TestInternalTransferEndpoints(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:         ":8080",
		InternalHTTPAddr: ":8085",
		InternalToken:    "test-secret-token",
		HealthTimeout:    time.Second,
	}
	tenantID := identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f1111")
	walletID := identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f2222")
	opID := identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f3333")
	server := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), identifiers.NewDeterministicGenerator(), Dependencies{}, fakeWalletStore{})

	actions := []string{"reservations", "pending-credits", "captures", "finalizations", "releases", "compensations"}
	for _, action := range actions {
		path := "/internal/v1/wallets/" + walletID.String() + "/" + action

		// Public listener must return 404
		pubReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"transfer_id":"0192f4b7-6d92-7a4d-8cb5-1b3f9d8f4444","amount_minor":100,"currency":"USD"}`))
		pubRec := httptest.NewRecorder()
		server.Handler().ServeHTTP(pubRec, pubReq)
		if pubRec.Code != http.StatusNotFound {
			t.Errorf("action %s on public listener got %d, want 404", action, pubRec.Code)
		}

		// Internal listener without token returns 401
		noTokReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"transfer_id":"0192f4b7-6d92-7a4d-8cb5-1b3f9d8f4444","amount_minor":100,"currency":"USD"}`))
		noTokRec := httptest.NewRecorder()
		server.InternalHandler().ServeHTTP(noTokRec, noTokReq)
		if noTokRec.Code != http.StatusUnauthorized {
			t.Errorf("action %s without token got %d, want 401", action, noTokRec.Code)
		}

		// Internal listener without X-Operation-ID returns 422
		noOpReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"transfer_id":"0192f4b7-6d92-7a4d-8cb5-1b3f9d8f4444","amount_minor":100,"currency":"USD"}`))
		noOpReq.Header.Set("X-Internal-Token", cfg.InternalToken)
		noOpReq.Header.Set("X-Tenant-ID", tenantID.String())
		noOpRec := httptest.NewRecorder()
		server.InternalHandler().ServeHTTP(noOpRec, noOpReq)
		if noOpRec.Code != http.StatusUnprocessableEntity {
			t.Errorf("action %s without X-Operation-ID got %d, want 422", action, noOpRec.Code)
		}

		// Valid request returns 201
		validReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"transfer_id":"0192f4b7-6d92-7a4d-8cb5-1b3f9d8f4444","amount_minor":100,"currency":"USD"}`))
		validReq.Header.Set("X-Internal-Token", cfg.InternalToken)
		validReq.Header.Set("X-Tenant-ID", tenantID.String())
		validReq.Header.Set("X-Operation-ID", opID.String())
		validRec := httptest.NewRecorder()
		server.InternalHandler().ServeHTTP(validRec, validReq)
		if validRec.Code != http.StatusCreated {
			t.Errorf("action %s valid request got %d, want 201; body = %s", action, validRec.Code, validRec.Body.String())
		}
	}

	// Test GET /internal/v1/operations/{operation_id}
	getOpReq := httptest.NewRequest(http.MethodGet, "/internal/v1/operations/"+opID.String(), nil)
	getOpReq.Header.Set("X-Internal-Token", cfg.InternalToken)
	getOpReq.Header.Set("X-Tenant-ID", tenantID.String())
	getOpRec := httptest.NewRecorder()
	server.InternalHandler().ServeHTTP(getOpRec, getOpReq)
	if getOpRec.Code != http.StatusOK {
		t.Errorf("get operation got %d, want 200; body = %s", getOpRec.Code, getOpRec.Body.String())
	}
}
