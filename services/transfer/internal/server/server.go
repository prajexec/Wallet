package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"digitalwallet/services/transfer/internal/config"
	"digitalwallet/services/transfer/internal/repository"
	"digitalwallet/shared/identifiers"
)

type contextKey string

const requestIDKey contextKey = "request_id"

type CheckFunc func(context.Context) error

type Dependencies struct {
	Postgres      CheckFunc
	NATS          CheckFunc
	StartTransfer func(context.Context, identifiers.UUID, identifiers.UUID)
}

type Store interface {
	Create(context.Context, identifiers.UUID, string, string, repository.Transfer) (repository.Transfer, bool, error)
	Get(context.Context, identifiers.UUID, identifiers.UUID) (repository.Transfer, error)
}

type ReconciliationStore interface {
	ListActiveExceptions(context.Context, identifiers.UUID, int) ([]repository.ReconciliationException, error)
	GetException(context.Context, identifiers.UUID, identifiers.UUID) (repository.ReconciliationException, error)
	ClaimException(context.Context, identifiers.UUID, identifiers.UUID, string) (repository.ReconciliationException, error)
	ResolveException(context.Context, identifiers.UUID, identifiers.UUID, string, string, repository.ResolutionAction, repository.ResolutionEvidence) (repository.ReconciliationException, error)
	AcceptException(context.Context, identifiers.UUID, identifiers.UUID, string, string, repository.ResolutionEvidence) (repository.ReconciliationException, error)
	ListExceptionNotes(context.Context, identifiers.UUID, identifiers.UUID) ([]repository.ExceptionNote, error)
}

type Server struct {
	config       config.Config
	logger       *slog.Logger
	ids          identifiers.Generator
	dependencies Dependencies
	store        Store
	http         *http.Server
	internalHTTP *http.Server
}

func New(cfg config.Config, logger *slog.Logger, ids identifiers.Generator, dependencies Dependencies, stores ...Store) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if ids == nil {
		ids = identifiers.NewGenerator()
	}
	s := &Server{config: cfg, logger: logger, ids: ids, dependencies: dependencies}
	if len(stores) > 0 {
		s.store = stores[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("POST /v1/transfers", s.createTransfer)
	mux.HandleFunc("GET /v1/transfers/{transfer_id}", s.getTransfer)
	s.http = &http.Server{Addr: cfg.HTTPAddr, Handler: s.middleware(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	s.internalHTTP = &http.Server{Addr: cfg.InternalHTTPAddr, Handler: s.internalHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	return s
}

func (s *Server) internalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/v1/stats", s.stats)
	mux.HandleFunc("GET /internal/v1/reconciliation/exceptions", s.listExceptions)
	mux.HandleFunc("POST /internal/v1/reconciliation/exceptions/{exception_id}/investigate", s.investigateException)
	mux.HandleFunc("POST /internal/v1/reconciliation/exceptions/{exception_id}/resolve", s.resolveException)
	mux.HandleFunc("POST /internal/v1/reconciliation/exceptions/{exception_id}/accept", s.acceptException)
	mux.HandleFunc("GET /internal/v1/reconciliation/exceptions/{exception_id}/notes", s.exceptionNotes)
	return s.requireInternalToken(s.middleware(mux))
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusOK, repository.TransferStatsResponse{Service: "transfer", AttemptsHistogram: map[int32]int64{}})
		return
	}
	type statsProvider interface {
		GetStats(context.Context) (repository.TransferStatsResponse, error)
	}
	if sp, ok := s.store.(statsProvider); ok {
		stats, err := sp.GetStats(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, stats)
		return
	}
	writeJSON(w, http.StatusOK, repository.TransferStatsResponse{Service: "transfer", AttemptsHistogram: map[int32]int64{}})
}

func (s *Server) requireInternalToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Token")), []byte(s.config.InternalToken)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid or missing internal token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) reconciliationStore(w http.ResponseWriter) (ReconciliationStore, bool) {
	store, ok := s.store.(ReconciliationStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "reconciliation repository is not configured"})
	}
	return store, ok
}

func tenantFromHeader(w http.ResponseWriter, r *http.Request) (identifiers.UUID, bool) {
	tenant, err := identifiers.Parse(strings.TrimSpace(r.Header.Get("X-Tenant-ID")))
	if err != nil || tenant.IsZero() || !tenant.IsV7() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "X-Tenant-ID must be UUIDv7"})
		return identifiers.UUID{}, false
	}
	return tenant, true
}

func exceptionID(w http.ResponseWriter, r *http.Request) (identifiers.UUID, bool) {
	id, err := identifiers.Parse(r.PathValue("exception_id"))
	if err != nil || id.IsZero() || !id.IsV7() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "exception_id must be UUIDv7"})
		return identifiers.UUID{}, false
	}
	return id, true
}

func operatorID(w http.ResponseWriter, r *http.Request) (string, bool) {
	value := strings.TrimSpace(r.Header.Get("X-Operator-ID"))
	if value == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "X-Operator-ID is required"})
		return "", false
	}
	return value, true
}

func (s *Server) listExceptions(w http.ResponseWriter, r *http.Request) {
	store, ok := s.reconciliationStore(w)
	if !ok {
		return
	}
	tenant, ok := tenantFromHeader(w, r)
	if !ok {
		return
	}
	items, err := store.ListActiveExceptions(r.Context(), tenant, 100)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) investigateException(w http.ResponseWriter, r *http.Request) {
	store, ok := s.reconciliationStore(w)
	if !ok {
		return
	}
	tenant, ok := tenantFromHeader(w, r)
	if !ok {
		return
	}
	id, ok := exceptionID(w, r)
	if !ok {
		return
	}
	owner, ok := operatorID(w, r)
	if !ok {
		return
	}
	result, err := store.ClaimException(r.Context(), tenant, id, owner)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) resolveException(w http.ResponseWriter, r *http.Request) {
	store, ok := s.reconciliationStore(w)
	if !ok {
		return
	}
	tenant, ok := tenantFromHeader(w, r)
	if !ok {
		return
	}
	id, ok := exceptionID(w, r)
	if !ok {
		return
	}
	operator, ok := operatorID(w, r)
	if !ok {
		return
	}
	var request struct {
		Action   repository.ResolutionAction   `json:"action"`
		Evidence repository.ResolutionEvidence `json:"evidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}
	result, err := store.ResolveException(r.Context(), tenant, id, operator, operator, request.Action, request.Evidence)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) acceptException(w http.ResponseWriter, r *http.Request) {
	store, ok := s.reconciliationStore(w)
	if !ok {
		return
	}
	tenant, ok := tenantFromHeader(w, r)
	if !ok {
		return
	}
	id, ok := exceptionID(w, r)
	if !ok {
		return
	}
	operator, ok := operatorID(w, r)
	if !ok {
		return
	}
	var request struct {
		Evidence repository.ResolutionEvidence `json:"evidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}
	result, err := store.AcceptException(r.Context(), tenant, id, operator, operator, request.Evidence)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) exceptionNotes(w http.ResponseWriter, r *http.Request) {
	store, ok := s.reconciliationStore(w)
	if !ok {
		return
	}
	tenant, ok := tenantFromHeader(w, r)
	if !ok {
		return
	}
	id, ok := exceptionID(w, r)
	if !ok {
		return
	}
	result, err := store.ListExceptionNotes(r.Context(), tenant, id)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, repository.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, repository.ErrConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, repository.ErrInvalidResolution) {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func (s *Server) createTransfer(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "transfer repository is not configured"})
		return
	}
	tenant, err := identifiers.Parse(r.Header.Get("X-Tenant-ID"))
	if err != nil || tenant.IsZero() || !tenant.IsV7() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "X-Tenant-ID must be UUIDv7"})
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "Idempotency-Key is required"})
		return
	}
	var req struct {
		SourceWalletID      identifiers.UUID `json:"source_wallet_id"`
		DestinationWalletID identifiers.UUID `json:"destination_wallet_id"`
		AmountMinor         int64            `json:"amount_minor"`
		Currency            string           `json:"currency"`
		Reference           string           `json:"reference"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}
	if req.SourceWalletID.IsZero() || req.DestinationWalletID.IsZero() || req.SourceWalletID == req.DestinationWalletID || req.AmountMinor <= 0 || req.Currency != "USD" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "invalid transfer"})
		return
	}
	hash, err := repository.HashRequest(req)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}
	t, replayed, err := s.store.Create(r.Context(), tenant, key, hash, repository.Transfer{SourceWalletID: req.SourceWalletID, DestinationWalletID: req.DestinationWalletID, AmountMinor: req.AmountMinor, Currency: req.Currency, Reference: req.Reference})
	if err != nil {
		if errors.Is(err, repository.ErrIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "idempotency_conflict"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !replayed && s.dependencies.StartTransfer != nil {
		go s.dependencies.StartTransfer(context.Background(), tenant, t.TransferID)
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusAccepted, t)
}

func (s *Server) getTransfer(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "transfer repository is not configured"})
		return
	}
	tenant, err := identifiers.Parse(r.Header.Get("X-Tenant-ID"))
	if err != nil || tenant.IsZero() || !tenant.IsV7() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "X-Tenant-ID must be UUIDv7"})
		return
	}
	id, err := identifiers.Parse(r.PathValue("transfer_id"))
	if err != nil || id.IsZero() || !id.IsV7() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "transfer_id must be UUIDv7"})
		return
	}
	t, err := s.store.Get(r.Context(), tenant, id)
	if errors.Is(err, repository.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) ListenAndServe() error {
	errCh := make(chan error, 2)
	go func() {
		err := s.http.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	go func() {
		err := s.internalHTTP.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	var first error
	for range 2 {
		if err := <-errCh; err != nil && first == nil {
			first = err
			_ = s.http.Close()
			_ = s.internalHTTP.Close()
		}
	}
	return first
}

func (s *Server) Shutdown(ctx context.Context) error {
	return errors.Join(s.http.Shutdown(ctx), s.internalHTTP.Shutdown(ctx))
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), s.config.HealthTimeout)
	defer cancel()
	checks := map[string]string{}
	status := http.StatusOK
	for name, check := range map[string]CheckFunc{"postgres": s.dependencies.Postgres, "nats": s.dependencies.NATS} {
		if check == nil {
			checks[name] = "not_configured"
			status = http.StatusServiceUnavailable
			continue
		}
		if err := check(ctx); err != nil {
			checks[name] = err.Error()
			status = http.StatusServiceUnavailable
			continue
		}
		checks[name] = "ok"
	}
	result := "ok"
	if status != http.StatusOK {
		result = "not_ready"
	}
	writeJSON(w, status, map[string]any{"status": result, "checks": checks})
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestID := strings.TrimSpace(request.Header.Get("X-Request-ID"))
		if requestID == "" {
			id, err := s.ids.Generate()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "not_ready"})
				return
			}
			requestID = id.String()
		}
		correlationID := request.Header.Get("X-Correlation-ID")
		if strings.TrimSpace(correlationID) == "" {
			correlationID = requestID
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Correlation-ID", correlationID)
		started := time.Now()
		s.logger.Info("http request", "method", request.Method, "path", request.URL.Path, "request_id", requestID, "correlation_id", correlationID, "service", s.config.ServiceName, "instance", s.config.InstanceName)
		next.ServeHTTP(w, request)
		s.logger.Info("http response", "method", request.Method, "path", request.URL.Path, "duration_ms", time.Since(started).Milliseconds(), "request_id", requestID)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
