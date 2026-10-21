package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"digitalwallet/services/transfer/internal/config"
	"digitalwallet/services/transfer/internal/reconciliation"
	"digitalwallet/services/transfer/internal/repository"
	"digitalwallet/services/transfer/internal/saga"
	"digitalwallet/services/transfer/internal/server"
	"digitalwallet/services/transfer/internal/walletclient"
	"digitalwallet/shared/identifiers"
	_ "github.com/jackc/pgx/v5/stdlib"
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
	ids := identifiers.NewGenerator()
	store := repository.New(database, ids)
	sagaService := saga.New(store, walletclient.New(cfg.WalletSourceURL, cfg.WalletInternalToken, walletclient.TimeoutClient(cfg.StepTimeout)), walletclient.New(cfg.WalletDestinationURL, cfg.WalletInternalToken, walletclient.TimeoutClient(cfg.StepTimeout)), cfg.MaxAttempts, cfg.StepTimeout)
	reconciliationService := reconciliation.New(store, sagaService, walletclient.New(cfg.WalletSourceURL, cfg.WalletInternalToken, walletclient.TimeoutClient(cfg.StepTimeout)), walletclient.New(cfg.WalletDestinationURL, cfg.WalletInternalToken, walletclient.TimeoutClient(cfg.StepTimeout)))
	httpServer := server.New(cfg, logger, ids, server.Dependencies{
		Postgres: database.PingContext,
		NATS:     func(context.Context) error { return natsConnection.FlushTimeout(cfg.HealthTimeout) },
		StartTransfer: func(ctx context.Context, tenantID, transferID identifiers.UUID) {
			if err := sagaService.Run(ctx, tenantID, transferID); err != nil {
				logger.Error("transfer saga stopped", "transfer_id", transferID, "error", err)
			}
		},
	}, store)
	rootContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		ticker := time.NewTicker(cfg.ReconciliationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-rootContext.Done():
				return
			case <-ticker.C:
				if err := reconciliationService.Sweep(rootContext, cfg.ReconciliationOwner); err != nil {
					logger.Error("reconciliation sweep failed", "error", err)
				}
			}
		}
	}()
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
