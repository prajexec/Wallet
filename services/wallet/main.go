package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"digitalwallet/services/wallet/internal/config"
	"digitalwallet/services/wallet/internal/repository"
	"digitalwallet/services/wallet/internal/server"
	"digitalwallet/shared/identifiers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

func main() {
	logger := newLogger(slog.LevelInfo)
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger = newLogger(parseLogLevel(cfg.LogLevel))

	database, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		logger.Error("open PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	if err := database.Ping(context.Background()); err != nil {
		logger.Error("ping PostgreSQL", "error", err)
		os.Exit(1)
	}

	natsConnection, err := nats.Connect(cfg.NATSURL, nats.Name(cfg.ServiceName+"/"+cfg.InstanceName))
	if err != nil {
		logger.Error("connect NATS", "error", err)
		os.Exit(1)
	}
	defer natsConnection.Close()

	ids := identifiers.NewGenerator()
	store := repository.New(database, ids, cfg.InstanceName)
	httpServer := server.New(cfg, logger, ids, server.Dependencies{
		Postgres: database.Ping,
		NATS: func(context.Context) error {
			return natsConnection.FlushTimeout(cfg.HealthTimeout)
		},
	}, store)

	rootContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("HTTP server stopped", "error", err)
			os.Exit(1)
		}
	case <-rootContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("shutdown HTTP server", "error", err)
			os.Exit(1)
		}
	}
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
