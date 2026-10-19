package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"digitalwallet/services/projector/internal/config"
	"digitalwallet/services/projector/internal/server"
	"digitalwallet/shared/identifiers"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	database, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		logger.Error("open PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	natsConnection, err := nats.Connect(cfg.NATSURL, nats.Name(cfg.ServiceName+"/"+cfg.InstanceName))
	if err != nil {
		logger.Error("connect NATS", "error", err)
		os.Exit(1)
	}
	defer natsConnection.Close()
	httpServer := server.New(cfg, logger, identifiers.NewGenerator(), server.Dependencies{
		Postgres: database.PingContext,
		NATS:     func(context.Context) error { return natsConnection.FlushTimeout(cfg.HealthTimeout) },
	})
	rootContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
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
