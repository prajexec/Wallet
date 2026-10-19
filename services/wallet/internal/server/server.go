package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"digitalwallet/services/wallet/internal/config"
	"digitalwallet/services/wallet/internal/repository"
	apierrors "digitalwallet/shared/errors"
	"digitalwallet/shared/identifiers"
	"digitalwallet/shared/money"
)

type contextKey string

const (
	requestIDKey   contextKey = "request_id"
	correlationKey contextKey = "correlation_id"
)

// CheckFunc is a dependency readiness probe.
type CheckFunc func(context.Context) error

// Dependencies are injected so health behavior can be tested without a live
// PostgreSQL or NATS process.
type Dependencies struct {
	Postgres CheckFunc
	NATS     CheckFunc
}

// WalletStore is the HTTP-facing repository seam. The concrete repository is
// passed by the process composition root; tests can provide a fake.
type WalletStore interface {
	CreateWallet(context.Context, identifiers.UUID, string, string, string) (repository.CommandResult, error)
	GetWallet(context.Context, identifiers.UUID, identifiers.UUID) (repository.Wallet, error)
	GetBalance(context.Context, identifiers.UUID, identifiers.UUID) (repository.BalanceResponse, error)
	ListEntries(context.Context, identifiers.UUID, identifiers.UUID, string, int32) (repository.EntryPage, error)
	Deposit(context.Context, identifiers.UUID, identifiers.UUID, string, string, int64, string) (repository.CommandResult, error)
	Withdraw(context.Context, identifiers.UUID, identifiers.UUID, string, string, int64, string) (repository.CommandResult, error)
	GetStats(context.Context) (repository.StatsResponse, error)
}

// Server owns the HTTP lifecycle and wallet endpoints for one wallet instance.
type Server struct {
	config       config.Config
	logger       *slog.Logger
	ids          identifiers.Generator
	dependencies Dependencies
	store        WalletStore
	http         *http.Server
}

// New constructs a wallet HTTP server. A nil logger or generator is replaced
// with safe production defaults. The optional store preserves the health-only
// constructor seam used by foundation tests.
func New(cfg config.Config, logger *slog.Logger, ids identifiers.Generator, dependencies Dependencies, stores ...WalletStore) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if ids == nil {
		ids = identifiers.NewGenerator()
	}
	server := &Server{config: cfg, logger: logger, ids: ids, dependencies: dependencies}
	if len(stores) > 0 {
		server.store = stores[0]
	}
	server.http = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return server
}

// Handler returns the fully wrapped HTTP handler for tests and embedding.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /internal/v1/stats", s.stats)
	mux.HandleFunc("POST /v1/wallets", s.createWallet)
	mux.HandleFunc("GET /v1/wallets/{wallet_id}", s.getWallet)
	mux.HandleFunc("GET /v1/wallets/{wallet_id}/balance", s.getBalance)
	mux.HandleFunc("GET /v1/wallets/{wallet_id}/entries", s.listEntries)
	mux.HandleFunc("POST /v1/wallets/{wallet_id}/deposit", s.deposit)
	mux.HandleFunc("POST /v1/wallets/{wallet_id}/withdraw", s.withdraw)
	return s.withRequestContext(mux)
}

func (s *Server) ListenAndServe() error {
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeHealth(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.config.HealthTimeout)
	defer cancel()
	checks := make(map[string]string, 2)
	statusCode := http.StatusOK
	status := "ok"

	if s.dependencies.Postgres == nil {
		checks["postgres"] = "not_configured"
		statusCode = http.StatusServiceUnavailable
		status = "not_ready"
	} else if err := s.dependencies.Postgres(ctx); err != nil {
		checks["postgres"] = err.Error()
		statusCode = http.StatusServiceUnavailable
		status = "not_ready"
	} else {
		checks["postgres"] = "ok"
	}

	if s.dependencies.NATS == nil {
		checks["nats"] = "not_configured"
		if statusCode == http.StatusOK {
			status = "degraded"
		}
	} else if err := s.dependencies.NATS(ctx); err != nil {
		checks["nats"] = err.Error()
		if statusCode == http.StatusOK {
			status = "degraded"
		}
	} else {
		checks["nats"] = "ok"
	}
	writeHealth(w, statusCode, healthResponse{Status: status, Checks: checks})
}

func (s *Server) withRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			generated, err := s.ids.Generate()
			if err != nil {
				s.logger.Error("generate request id", "error", err)
				writeHealth(w, http.StatusInternalServerError, healthResponse{Status: "not_ready"})
				return
			}
			requestID = generated.String()
		}
		correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
		if correlationID == "" {
			correlationID = requestID
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Correlation-ID", correlationID)
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		ctx = context.WithValue(ctx, correlationKey, correlationID)
		start := time.Now()
		s.logger.InfoContext(ctx, "http request", "method", r.Method, "path", r.URL.Path, "service", s.config.ServiceName, "instance", s.config.InstanceName)
		next.ServeHTTP(w, r.WithContext(ctx))
		s.logger.InfoContext(ctx, "http response", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) createWallet(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExternalReference string `json:"external_reference"`
	}
	if !s.decodeJSON(w, r, &request) {
		return
	}
	tenantID, ok := s.tenantID(w, r)
	if !ok {
		return
	}
	key, ok := s.idempotencyKey(w, r)
	if !ok {
		return
	}
	hash, err := repository.HashRequest(request)
	if err != nil {
		s.writeError(w, r, http.StatusUnprocessableEntity, apierrors.CodeValidation, err, false)
		return
	}
	if s.store == nil {
		s.unavailable(w, r)
		return
	}
	result, err := s.store.CreateWallet(r.Context(), tenantID, key, hash, request.ExternalReference)
	if err != nil {
		s.handleStoreError(w, r, err)
		return
	}
	s.writeCommandResult(w, result)
}

func (s *Server) getWallet(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.tenantID(w, r)
	if !ok {
		return
	}
	walletID, ok := pathUUID(w, r, "wallet_id")
	if !ok {
		return
	}
	if s.store == nil {
		s.unavailable(w, r)
		return
	}
	wallet, err := s.store.GetWallet(r.Context(), tenantID, walletID)
	if err != nil {
		s.handleStoreError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, wallet)
}

func (s *Server) getBalance(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.tenantID(w, r)
	if !ok {
		return
	}
	walletID, ok := pathUUID(w, r, "wallet_id")
	if !ok {
		return
	}
	if s.store == nil {
		s.unavailable(w, r)
		return
	}
	balance, err := s.store.GetBalance(r.Context(), tenantID, walletID)
	if err != nil {
		s.handleStoreError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, balance)
}

func (s *Server) listEntries(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.tenantID(w, r)
	if !ok {
		return
	}
	walletID, ok := pathUUID(w, r, "wallet_id")
	if !ok {
		return
	}
	limit := int32(50)
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			s.writeError(w, r, http.StatusUnprocessableEntity, apierrors.CodeValidation, errors.New("limit must be between 1 and 100"), false)
			return
		}
		limit = int32(parsed)
	}
	if s.store == nil {
		s.unavailable(w, r)
		return
	}
	page, err := s.store.ListEntries(r.Context(), tenantID, walletID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.handleStoreError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, page)
}

func (s *Server) deposit(w http.ResponseWriter, r *http.Request) { s.postMoney(w, r, false) }

func (s *Server) withdraw(w http.ResponseWriter, r *http.Request) { s.postMoney(w, r, true) }

func (s *Server) postMoney(w http.ResponseWriter, r *http.Request, withdrawal bool) {
	var request struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
		Reference   string `json:"reference"`
	}
	if !s.decodeJSON(w, r, &request) {
		return
	}
	tenantID, ok := s.tenantID(w, r)
	if !ok {
		return
	}
	walletID, ok := pathUUID(w, r, "wallet_id")
	if !ok {
		return
	}
	key, ok := s.idempotencyKey(w, r)
	if !ok {
		return
	}
	hash, err := repository.HashRequest(request)
	if err != nil {
		s.writeError(w, r, http.StatusUnprocessableEntity, apierrors.CodeValidation, err, false)
		return
	}
	if s.store == nil {
		s.unavailable(w, r)
		return
	}
	var result repository.CommandResult
	if withdrawal {
		result, err = s.store.Withdraw(r.Context(), tenantID, walletID, key, hash, request.AmountMinor, request.Reference)
	} else {
		result, err = s.store.Deposit(r.Context(), tenantID, walletID, key, hash, request.AmountMinor, request.Reference)
	}
	if err != nil {
		s.handleStoreError(w, r, err)
		return
	}
	s.writeCommandResult(w, result)
}

func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		s.writeError(w, r, http.StatusUnprocessableEntity, apierrors.CodeValidation, err, false)
		return false
	}
	return true
}

func (s *Server) tenantID(w http.ResponseWriter, r *http.Request) (identifiers.UUID, bool) {
	value := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
	parsed, err := identifiers.Parse(value)
	if err != nil || parsed.IsZero() || !parsed.IsV7() {
		s.writeError(w, r, http.StatusUnprocessableEntity, apierrors.CodeValidation, errors.New("X-Tenant-ID must be a UUIDv7"), false)
		return identifiers.UUID{}, false
	}
	return parsed, true
}

func (s *Server) idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if value == "" || len(value) > 200 {
		s.writeError(w, r, http.StatusUnprocessableEntity, apierrors.CodeValidation, errors.New("Idempotency-Key is required"), false)
		return "", false
	}
	return value, true
}

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (identifiers.UUID, bool) {
	parsed, err := identifiers.Parse(r.PathValue(name))
	if err != nil || parsed.IsZero() || !parsed.IsV7() {
		apiErr := apierrors.New(apierrors.CodeValidation, "path identifier must be a UUIDv7", RequestID(r.Context()), false, nil)
		_ = apierrors.WriteJSON(w, http.StatusUnprocessableEntity, apiErr)
		return identifiers.UUID{}, false
	}
	return parsed, true
}

func (s *Server) unavailable(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusServiceUnavailable, "wallet_unavailable", errors.New("wallet repository is not configured"), true)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		s.writeJSON(w, http.StatusOK, repository.StatsResponse{
			Service:           "wallet",
			AttemptsHistogram: map[int32]int64{},
		})
		return
	}
	stats, err := s.store.GetStats(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", err, false)
		return
	}
	s.writeJSON(w, http.StatusOK, stats)
}

func (s *Server) writeCommandResult(w http.ResponseWriter, result repository.CommandResult) {
	if result.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.StatusCode)
	_, _ = w.Write(result.Body)
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code string, err error, retryable bool) {
	apiErr := apierrors.New(code, err.Error(), RequestID(r.Context()), retryable, nil)
	_ = apierrors.WriteJSON(w, status, apiErr)
}

func (s *Server) handleStoreError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	retryable := true
	switch {
	case errors.Is(err, repository.ErrNotFound):
		status, code, retryable = http.StatusNotFound, "not_found", false
	case errors.Is(err, repository.ErrIdempotencyConflict):
		status, code, retryable = http.StatusUnprocessableEntity, "idempotency_key_reused", false
	case errors.Is(err, repository.ErrIdempotencyProgress):
		status, code, retryable = http.StatusConflict, "request_in_progress", true
	case errors.Is(err, repository.ErrInsufficientFunds):
		status, code, retryable = http.StatusUnprocessableEntity, apierrors.CodeInsufficientFunds, false
	case errors.Is(err, repository.ErrWalletClosed):
		status, code, retryable = http.StatusConflict, "wallet_closed", false
	case errors.Is(err, money.ErrInvalidCurrency), errors.Is(err, money.ErrNonPositive), errors.Is(err, money.ErrOverflow):
		status, code, retryable = http.StatusUnprocessableEntity, apierrors.CodeValidation, false
	}
	s.writeError(w, r, status, code, err, retryable)
}

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

func writeHealth(w http.ResponseWriter, status int, response healthResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func CorrelationID(ctx context.Context) string {
	value, _ := ctx.Value(correlationKey).(string)
	return value
}

func ValidateConfig(cfg config.Config) error {
	if strings.TrimSpace(cfg.HTTPAddr) == "" {
		return fmt.Errorf("HTTP_ADDR must not be empty")
	}
	return nil
}
