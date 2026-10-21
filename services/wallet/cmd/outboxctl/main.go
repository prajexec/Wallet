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
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "retry") {
		return errors.New("usage: outboxctl inspect|retry")
	}
	flags := flag.NewFlagSet("outboxctl", flag.ContinueOnError)
	dsn := flags.String("database-url", os.Getenv("DATABASE_URL"), "PostgreSQL DSN")
	limit := flags.Int("limit", 100, "row limit")
	id := flags.String("id", "", "outbox row UUIDv7")
	tenant := flags.String("tenant", "", "tenant UUIDv7")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("--database-url or DATABASE_URL is required")
	}
	if *limit < 1 || *limit > 1000 {
		return errors.New("--limit must be between 1 and 1000")
	}
	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if args[0] == "retry" {
		if *id == "" || *tenant == "" {
			return errors.New("retry requires --id and --tenant")
		}
		outboxID, err := identifiers.Parse(*id)
		if err != nil || outboxID.IsZero() || !outboxID.IsV7() {
			return errors.New("--id must be a UUIDv7")
		}
		tenantID, err := identifiers.Parse(*tenant)
		if err != nil || tenantID.IsZero() || !tenantID.IsV7() {
			return errors.New("--tenant must be a UUIDv7")
		}
		if _, err := db.ExecContext(ctx, `UPDATE outbox_messages SET locked_until=NULL,next_attempt_at=$3,last_error=NULL WHERE tenant_id=$1 AND id=$2 AND published_at IS NULL`, tenantID.String(), outboxID.String(), time.Now().UTC()); err != nil {
			return err
		}
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT tenant_id,id,event_id,created_at,published_at,attempts,next_attempt_at,locked_until,last_error FROM outbox_messages ORDER BY created_at LIMIT $1`, *limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	var result []map[string]any
	for rows.Next() {
		var tenant, id, event string
		var created, next time.Time
		var published, locked *time.Time
		var attempts int
		var last sql.NullString
		if err := rows.Scan(&tenant, &id, &event, &created, &published, &attempts, &next, &locked, &last); err != nil {
			return err
		}
		overdue := published == nil && next.Before(time.Now().UTC())
		result = append(result, map[string]any{"tenant_id": tenant, "id": id, "event_id": event, "created_at": created, "published_at": published, "attempts": attempts, "next_attempt_at": next, "locked_until": locked, "last_error": last.String, "stale": overdue || (locked != nil && locked.Before(time.Now().UTC()))})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	body, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(body))
	return nil
}
