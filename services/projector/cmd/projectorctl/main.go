package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"digitalwallet/services/projector/internal/projection"
	"digitalwallet/shared/identifiers"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: projectorctl rebuild|status|promote|rollback|retire|wipe-active")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("READ_DATABASE_URL")
	}
	if dsn == "" {
		return errors.New("DATABASE_URL or READ_DATABASE_URL is required")
	}
	consumerName := os.Getenv("NATS_CONSUMER")
	if consumerName == "" {
		consumerName = "projector"
	}
	token := os.Getenv("WALLET_VERIFY_TOKEN")
	if token == "" {
		token = os.Getenv("INTERNAL_TOKEN")
	}
	verificationTimeout := 30 * time.Second
	if value := os.Getenv("PROJECTION_VERIFICATION_TIMEOUT"); value != "" {
		parsed, parseErr := time.ParseDuration(value)
		if parseErr != nil {
			return parseErr
		}
		verificationTimeout = parsed
	}
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	coordinator := &projection.Coordinator{DB: database, ReplayConsumerName: consumerName + "-balance-a", WalletVerifyToken: token, WalletVerifyURLs: split(os.Getenv("WALLET_VERIFY_URLS")), VerificationTimeout: verificationTimeout}

	switch args[0] {
	case "rebuild":
		flags := flag.NewFlagSet("rebuild", flag.ContinueOnError)
		auto := flags.Bool("auto-promote", false, "promote after verification")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		result, err := coordinator.Rebuild(ctx, *auto)
		if err != nil {
			return err
		}
		return printJSON(result)
	case "status":
		var result struct {
			RebuildID  string    `json:"rebuild_id"`
			State      string    `json:"state"`
			TargetSlot string    `json:"target_slot"`
			StartedAt  time.Time `json:"started_at"`
			UpdatedAt  time.Time `json:"updated_at"`
		}
		err := database.QueryRowContext(ctx, `SELECT rebuild_id, state, target_slot, started_at, updated_at FROM projection_rebuilds ORDER BY started_at DESC LIMIT 1`).Scan(&result.RebuildID, &result.State, &result.TargetSlot, &result.StartedAt, &result.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return printJSON(map[string]any{"state": "NONE"})
		}
		if err != nil {
			return err
		}
		return printJSON(result)
	case "promote":
		flags := flag.NewFlagSet("promote", flag.ContinueOnError)
		id := flags.String("rebuild-id", "", "rebuild UUID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return errors.New("--rebuild-id is required")
		}
		rebuildID, err := identifiers.Parse(*id)
		if err != nil {
			return err
		}
		return coordinator.Promote(ctx, rebuildID)
	case "rollback":
		return coordinator.Rollback(ctx)
	case "retire":
		return coordinator.RetireInactive(ctx)
	case "wipe-active":
		return coordinator.WipeActive(ctx)
	default:
		return fmt.Errorf("unknown projectorctl command %q", args[0])
	}
}

func split(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
func printJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
