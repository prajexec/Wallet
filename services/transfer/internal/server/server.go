package server

import (
	"context"
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

type Server struct {
	config       config.Config
	logger       *slog.Logger
	ids          identifiers.Generator
	dependencies Dependencies
	store        Store
	http         *http.Server
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
	return s
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
	if err := s.http.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

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
