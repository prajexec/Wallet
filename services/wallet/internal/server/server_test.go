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
	"digitalwallet/services/wallet/internal/repository"
	"digitalwallet/shared/identifiers"
)

type fakeWalletStore struct {
	getWalletErr error
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
