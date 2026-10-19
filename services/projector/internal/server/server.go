package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"digitalwallet/services/projector/internal/config"
	"digitalwallet/shared/identifiers"
)

type CheckFunc func(context.Context) error

type StatsResponse struct {
	Service                     string          `json:"service"`
	UnpublishedOutboxCount      int64           `json:"unpublished_outbox_count"`
	OldestUnpublishedAgeSeconds float64         `json:"oldest_unpublished_age_seconds"`
	AttemptsHistogram           map[int32]int64 `json:"attempts_histogram"`
	BufferedEventCount          int64           `json:"buffered_event_count"`
	OpenGapCount                int64           `json:"open_gap_count"`
	DeadLetterCount             int64           `json:"dead_letter_count"`
}

type StatsFunc func(context.Context) (StatsResponse, error)

type Dependencies struct {
	Postgres CheckFunc
	NATS     CheckFunc
	Stats    StatsFunc
}

type Server struct {
	config       config.Config
	logger       *slog.Logger
	ids          identifiers.Generator
	dependencies Dependencies
	http         *http.Server
}

func New(cfg config.Config, logger *slog.Logger, ids identifiers.Generator, dependencies Dependencies) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if ids == nil {
		ids = identifiers.NewGenerator()
	}
	s := &Server{config: cfg, logger: logger, ids: ids, dependencies: dependencies}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /internal/v1/stats", s.stats)
	s.http = &http.Server{Addr: cfg.HTTPAddr, Handler: s.middleware(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	return s
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

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	if s.dependencies.Stats == nil {
		writeJSON(w, http.StatusOK, StatsResponse{
			Service:           "projector",
			AttemptsHistogram: map[int32]int64{},
		})
		return
	}
	stats, err := s.dependencies.Stats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, stats)
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
