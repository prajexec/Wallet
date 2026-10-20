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

	"digitalwallet/services/projector/internal/config"
	"digitalwallet/services/projector/internal/consumer"
	"digitalwallet/services/projector/internal/server"
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
	jetStream, err := natsConnection.JetStream()
	if err != nil {
		logger.Error("create JetStream context", "error", err)
		os.Exit(1)
	}
	if _, err := consumer.BootstrapConsumer(jetStream, cfg.StreamName, cfg.ConsumerName); err != nil {
		logger.Error("bootstrap projector consumer", "error", err)
		os.Exit(1)
	}

	pullSub, err := jetStream.PullSubscribe("events.>", cfg.ConsumerName, nats.Bind(cfg.StreamName, cfg.ConsumerName))
	if err != nil {
		logger.Error("subscribe projector pull consumer", "error", err)
		os.Exit(1)
	}

	consumerRunner := consumer.NewRunner(database, pullSub, &consumer.AppliedEventsLogHandler{ConsumerName: cfg.ConsumerName}, consumer.RunnerConfig{
		StreamName:   cfg.StreamName,
		ConsumerName: cfg.ConsumerName,
		BatchSize:    10,
		FetchTimeout: time.Second,
		MaxDeliver:   5,
		Logger:       logger,
	})

	gapSweeper := consumer.NewGapSweeper(database, consumer.SweeperConfig{
		ConsumerName: cfg.ConsumerName,
		GapWindow:    cfg.GapWindow,
		PollInterval: time.Second,
		Logger:       logger,
	})

	httpServer := server.New(cfg, logger, identifiers.NewGenerator(), server.Dependencies{
		Postgres: database.PingContext,
		NATS:     func(context.Context) error { return natsConnection.FlushTimeout(cfg.HealthTimeout) },
		Stats: func(ctx context.Context) (server.StatsResponse, error) {
			s, err := consumer.QueryStats(ctx, database, cfg.ConsumerName)
			if err != nil {
				return server.StatsResponse{}, err
			}
			return server.StatsResponse{
				Service:                     s.Service,
				UnpublishedOutboxCount:      s.UnpublishedOutboxCount,
				OldestUnpublishedAgeSeconds: s.OldestUnpublishedAgeSeconds,
				AttemptsHistogram:           s.AttemptsHistogram,
				BufferedEventCount:          s.BufferedEventCount,
				OpenGapCount:                s.OpenGapCount,
				DeadLetterCount:             s.DeadLetterCount,
			}, nil
		},
	})
	rootContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := consumerRunner.Run(rootContext); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("consumer runner error", "error", err)
		}
	}()

	go func() {
		if err := gapSweeper.Run(rootContext); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("gap sweeper error", "error", err)
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
		_ = consumerRunner.Stop(shutdownContext)
		_ = gapSweeper.Stop(shutdownContext)
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
