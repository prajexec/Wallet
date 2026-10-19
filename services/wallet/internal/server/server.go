package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"digitalwallet/services/wallet/internal/config"
	"digitalwallet/shared/identifiers"
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

// Server owns the HTTP lifecycle and health endpoints for a wallet instance.
type Server struct {
	config       config.Config
	logger       *slog.Logger
	ids          identifiers.Generator
	dependencies Dependencies
	http         *http.Server
}

// New constructs a wallet HTTP server. A nil logger or generator is replaced
// with safe production defaults.
func New(cfg config.Config, logger *slog.Logger, ids identifiers.Generator, dependencies Dependencies) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if ids == nil {
		ids = identifiers.NewGenerator()
	}
	server := &Server{
		config:       cfg,
		logger:       logger,
		ids:          ids,
		dependencies: dependencies,
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
	return s.withRequestContext(mux)
}

// ListenAndServe starts the HTTP listener.
func (s *Server) ListenAndServe() error {
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops accepting new requests.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeHealth(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.config.HealthTimeout)
	defer cancel()
	checks := make(map[string]string, 2)
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
	responseStatus := "ok"
	if status != http.StatusOK {
		responseStatus = "not_ready"
	}
	writeHealth(w, status, healthResponse{Status: responseStatus, Checks: checks})
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

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

func writeHealth(w http.ResponseWriter, status int, response healthResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}

// RequestID extracts the middleware request identifier for a handler.
func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

// CorrelationID extracts the middleware correlation identifier for a handler.
func CorrelationID(ctx context.Context) string {
	value, _ := ctx.Value(correlationKey).(string)
	return value
}

// ValidateConfig is a small helper used by main packages and tests to keep a
// clear error when a zero config is passed to a server directly.
func ValidateConfig(cfg config.Config) error {
	if strings.TrimSpace(cfg.HTTPAddr) == "" {
		return fmt.Errorf("HTTP_ADDR must not be empty")
	}
	return nil
}
