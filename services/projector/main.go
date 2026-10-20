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
	"digitalwallet/services/projector/internal/projection"
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
	consumerRunners := make([]*consumer.Runner, 0, 2)
	gapSweepers := make([]*consumer.GapSweeper, 0, 2)
	for _, slot := range []string{"a", "b"} {
		consumerName := cfg.ConsumerName + "-balance-" + slot
		if _, err := consumer.BootstrapConsumerWithPrefix(jetStream, cfg.StreamName, consumerName, cfg.SubjectPrefix); err != nil {
			logger.Error("bootstrap projector consumer", "consumer", consumerName, "error", err)
			os.Exit(1)
		}
		pullSub, err := jetStream.PullSubscribe(cfg.SubjectPrefix+".>", consumerName, nats.Bind(cfg.StreamName, consumerName))
		if err != nil {
			logger.Error("subscribe projector pull consumer", "consumer", consumerName, "error", err)
			os.Exit(1)
		}
		handler, err := projection.NewHandler(slot)
		if err != nil {
			logger.Error("create projection handler", "slot", slot, "error", err)
			os.Exit(1)
		}
		consumerRunners = append(consumerRunners, consumer.NewRunner(database, pullSub, handler, consumer.RunnerConfig{
			StreamName: cfg.StreamName, ConsumerName: consumerName, BatchSize: 10,
			FetchTimeout: time.Second, MaxDeliver: 5, Logger: logger,
		}))
		gapSweepers = append(gapSweepers, consumer.NewGapSweeper(database, consumer.SweeperConfig{
			ConsumerName: consumerName, GapWindow: cfg.GapWindow, PollInterval: time.Second, Logger: logger,
		}))
	}

	httpServer := server.New(cfg, logger, identifiers.NewGenerator(), server.Dependencies{
		Postgres: database.PingContext,
		NATS:     func(context.Context) error { return natsConnection.FlushTimeout(cfg.HealthTimeout) },
		Stats: func(ctx context.Context) (server.StatsResponse, error) {
			s, err := consumer.QueryStats(ctx, database, cfg.ConsumerName+"-balance-a")
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
		Read: projection.NewStore(database),
	})
	rootContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	for _, runner := range consumerRunners {
		runner := runner
		go func() {
			if err := runner.Run(rootContext); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("consumer runner error", "error", err)
			}
		}()
	}
	for _, sweeper := range gapSweepers {
		sweeper := sweeper
		go func() {
			if err := sweeper.Run(rootContext); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("gap sweeper error", "error", err)
			}
		}()
	}

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
		for _, runner := range consumerRunners {
			_ = runner.Stop(shutdownContext)
		}
		for _, sweeper := range gapSweepers {
			_ = sweeper.Stop(shutdownContext)
		}
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
