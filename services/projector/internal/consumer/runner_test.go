package consumer

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"
)

func getTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("READ_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://projector_role:projector_dev_password@127.0.0.1:5432/read?sslmode=disable"
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("cannot open test database: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("cannot ping test database (postgres not available): %v", err)
	}
	return db
}

type fakeFetcher struct {
	msgs []*nats.Msg
}

func (f *fakeFetcher) Fetch(batch int, opts ...nats.PullOpt) ([]*nats.Msg, error) {
	if len(f.msgs) == 0 {
		return nil, nats.ErrTimeout
	}
	take := batch
	if take > len(f.msgs) {
		take = len(f.msgs)
	}
	ret := f.msgs[:take]
	f.msgs = f.msgs[take:]
	return ret, nil
}

func makeEnvelope(t *testing.T, tenantID, aggregateID identifiers.UUID, version int64) events.Envelope {
	t.Helper()
	ids := identifiers.NewGenerator()
	eventID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	correlationID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env, err := events.NewEnvelope(
		eventID, "wallet.deposit_posted", 1, time.Now().UTC(),
		tenantID, "wallet", aggregateID, version,
		"wallet-source", correlationID, nil,
		map[string]any{"amount": 1000}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func envelopeToMsg(t *testing.T, env events.Envelope) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	msg := nats.NewMsg("events." + env.TenantID.String() + ".wallet.deposit_posted")
	msg.Data = data
	return msg
}

func TestRunner_InOrderAndDuplicate(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	aggregateID, _ := ids.Generate()
	randID, _ := ids.Generate()
	consumerName := "test_runner_" + randID.String()

	env1 := makeEnvelope(t, tenantID, aggregateID, 1)
	msg1 := envelopeToMsg(t, env1)

	fetcher := &fakeFetcher{msgs: []*nats.Msg{msg1}}
	runner := NewRunner(db, fetcher, nil, RunnerConfig{
		ConsumerName: consumerName,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Process v1
	runner.processOne(ctx, msg1)

	// Check cursor advanced to 1
	var lastVersion int64
	err := db.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&lastVersion)
	if err != nil {
		t.Fatalf("scan cursor: %v", err)
	}
	if lastVersion != 1 {
		t.Fatalf("cursor = %d, want 1", lastVersion)
	}

	// Check applied_events_log has 1 entry
	var count int
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM applied_events_log WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("applied count = %d, want 1, err = %v", count, err)
	}

	// 2. Process v1 duplicate
	runner.processOne(ctx, msg1)
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM applied_events_log WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("applied count after duplicate = %d, want 1, err = %v", count, err)
	}
}

func TestRunner_OutOfOrderBufferingAndDrain(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	aggregateID, _ := ids.Generate()
	randID, _ := ids.Generate()
	consumerName := "test_drain_" + randID.String()

	env1 := makeEnvelope(t, tenantID, aggregateID, 1)
	env2 := makeEnvelope(t, tenantID, aggregateID, 2)
	env3 := makeEnvelope(t, tenantID, aggregateID, 3)

	msg1 := envelopeToMsg(t, env1)
	msg2 := envelopeToMsg(t, env2)
	msg3 := envelopeToMsg(t, env3)

	runner := NewRunner(db, &fakeFetcher{}, nil, RunnerConfig{
		ConsumerName: consumerName,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Deliver v3, then v2
	runner.processOne(ctx, msg3)
	runner.processOne(ctx, msg2)

	// Both v2 and v3 should be buffered; cursor still 0
	var cursor int64
	err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(last_version), 0) FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&cursor)
	if err != nil || cursor != 0 {
		t.Fatalf("cursor = %d, want 0, err = %v", cursor, err)
	}

	var bufCount int
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM buffered_events WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&bufCount)
	if err != nil || bufCount != 2 {
		t.Fatalf("buffered count = %d, want 2, err = %v", bufCount, err)
	}

	// Now deliver missing v1 -> should apply v1, then drain v2 and v3, leaving buffer empty and cursor=3
	runner.processOne(ctx, msg1)

	err = db.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&cursor)
	if err != nil || cursor != 3 {
		t.Fatalf("cursor = %d, want 3, err = %v", cursor, err)
	}

	err = db.QueryRowContext(ctx, "SELECT count(*) FROM buffered_events WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&bufCount)
	if err != nil || bufCount != 0 {
		t.Fatalf("buffered count after drain = %d, want 0, err = %v", bufCount, err)
	}

	var appliedCount int
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM applied_events_log WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&appliedCount)
	if err != nil || appliedCount != 3 {
		t.Fatalf("applied count = %d, want 3, err = %v", appliedCount, err)
	}
}

func TestRunner_PoisonMessage(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ids := identifiers.NewGenerator()
	randID, _ := ids.Generate()
	consumerName := "test_poison_" + randID.String()

	runner := NewRunner(db, &fakeFetcher{}, nil, RunnerConfig{
		ConsumerName: consumerName,
		MaxDeliver:   3,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	badMsg := nats.NewMsg("events.invalid")
	badMsg.Data = []byte("not valid json at all")

	// Trigger poison handling by passing MaxDeliver
	evID, _ := ids.Generate()
	aggID, _ := ids.Generate()
	runner.handlePoison(ctx, badMsg, evID, aggID, 1, badMsg.Data, "malformed payload", 3)

	var count int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM dead_letter_events WHERE consumer_name = $1", consumerName).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("dead letter count = %d, want 1, err = %v", count, err)
	}
}

func TestSweeper_GapDetected(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	aggregateID, _ := ids.Generate()
	randID, _ := ids.Generate()
	consumerName := "test_sweep_" + randID.String()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Insert a buffered event for version 2 with first_seen_at 1 minute in the past
	past := time.Now().UTC().Add(-time.Minute)
	env2 := makeEnvelope(t, tenantID, aggregateID, 2)
	payload, _ := json.Marshal(env2)
	const insertBuf = `
INSERT INTO buffered_events (tenant_id, consumer_name, aggregate_id, aggregate_version, event_id, aggregate_type, event_type, payload, first_seen_at, last_seen_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9);
`
	_, err := db.ExecContext(ctx, insertBuf, tenantID, consumerName, aggregateID, 2, env2.EventID, "wallet", "wallet.deposit_posted", payload, past)
	if err != nil {
		t.Fatalf("insert buffered event: %v", err)
	}

	sweeper := NewGapSweeper(db, SweeperConfig{
		ConsumerName: consumerName,
		GapWindow:    10 * time.Second, // 1 minute > 10 seconds
	})

	if err := sweeper.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	// Should have recorded gap for expected_version = 1 with status 'OPEN'
	var status string
	err = db.QueryRowContext(ctx, "SELECT status FROM ordering_gaps WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3 AND expected_version = 1",
		tenantID, consumerName, aggregateID).Scan(&status)
	if err != nil || status != "OPEN" {
		t.Fatalf("gap status = %q, want OPEN, err = %v", status, err)
	}
}
