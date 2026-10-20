package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"digitalwallet/services/projector/internal/consumer"
	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nats-io/nats.go"
)

var (
	testReadDB *sql.DB
)

func TestMain(m *testing.M) {
	readDSN := os.Getenv("READ_TEST_DATABASE_URL")
	if readDSN == "" {
		_, _ = fmt.Fprintln(os.Stderr, "projector integration tests skipped: READ_TEST_DATABASE_URL is not set")
		os.Exit(m.Run())
	}

	db, err := sql.Open("pgx", readDSN)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "projector integration tests skipped: open read test database: %v\n", err)
		os.Exit(m.Run())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		_, _ = fmt.Fprintf(os.Stderr, "projector integration tests skipped: ping read test database: %v\n", err)
		os.Exit(m.Run())
	}
	testReadDB = db

	code := m.Run()
	db.Close()
	os.Exit(code)
}

func testStreamName() string {
	if s := os.Getenv("NATS_TEST_STREAM"); s != "" {
		return s
	}
	return "WALLET_EVENTS"
}

func testSubjectPrefix() string {
	if p := os.Getenv("NATS_TEST_SUBJECT_PREFIX"); p != "" {
		return p
	}
	return "events"
}

func cleanTenantReadData(t *testing.T, tenantID identifiers.UUID) {
	t.Helper()
	dsn := os.Getenv("READ_TEST_MIGRATOR_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/read_test?sslmode=disable"
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = db.ExecContext(ctx, "DELETE FROM applied_events_log WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM aggregate_cursors WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM buffered_events WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM ordering_gaps WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM dead_letter_events WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM inbox_messages WHERE tenant_id = $1", tenantID)
}

func cleanTenantWalletData(t *testing.T, tenantID identifiers.UUID) {
	t.Helper()
	dsn := os.Getenv("WALLET_TEST_MIGRATOR_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/wallet_source_test?sslmode=disable"
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = db.ExecContext(ctx, "DELETE FROM outbox_messages WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM events WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM journal_postings WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM journal_transactions WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM accounts WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM wallets WHERE tenant_id = $1", tenantID)
	_, _ = db.ExecContext(ctx, "DELETE FROM idempotency_records WHERE tenant_id = $1", tenantID)
}

func getNATSConnection(t *testing.T) (*nats.Conn, nats.JetStreamContext) {
	t.Helper()
	natsURL := os.Getenv("NATS_TEST_URL")
	if natsURL == "" {
		natsURL = "nats://127.0.0.1:4222"
	}
	nc, err := nats.Connect(natsURL, nats.Timeout(2*time.Second))
	if err != nil {
		t.Skipf("cannot connect to NATS (%s): %v", natsURL, err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		t.Skipf("cannot create JetStream context: %v", err)
	}
	stream := testStreamName()
	prefix := testSubjectPrefix()
	streamCfg := consumer.DefaultStreamConfigWithPrefix(stream, prefix)
	_, err = js.StreamInfo(stream)
	if errors.Is(err, nats.ErrStreamNotFound) {
		_, err = js.AddStream(&streamCfg)
	}
	if err != nil && !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
		nc.Close()
		t.Fatalf("ensure stream: %v", err)
	}
	return nc, js
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
	eventType := "wallet.deposit_posted"
	if version == 1 {
		eventType = "wallet.created"
	}
	env, err := events.NewEnvelope(
		eventID, eventType, 1, time.Now().UTC(),
		tenantID, "wallet", aggregateID, version,
		"wallet-source", correlationID, nil,
		map[string]any{"amount": 5000, "currency": "USD"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func publishEnvelope(t *testing.T, js nats.JetStreamContext, env events.Envelope) *nats.PubAck {
	t.Helper()
	prefix := testSubjectPrefix()
	subject, err := events.SubjectWithPrefix(prefix, env.TenantID, env.AggregateType, env.EventType)
	if err != nil {
		t.Fatalf("subject for envelope: %v", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	msg := nats.NewMsg(subject)
	msg.Header.Set(events.NatsMsgIDHeader, env.EventID.String())
	msg.Data = data
	ack, err := js.PublishMsg(msg, nats.MsgId(env.EventID.String()))
	if err != nil {
		t.Fatalf("publish envelope: %v", err)
	}
	return ack
}

// Scenario 4: Consumer crashes before ACK -> redelivery causes no double effect.
func TestConsumer_CrashBeforeAck_RedeliveryNoDoubleEffect(t *testing.T) {
	if testReadDB == nil {
		t.Skip("read test database not configured")
	}
	nc, js := getNATSConnection(t)
	defer nc.Close()

	stream := testStreamName()
	prefix := testSubjectPrefix()
	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	t.Cleanup(func() { cleanTenantReadData(t, tenantID) })
	aggregateID, _ := ids.Generate()
	randID, _ := ids.Generate()
	consumerName := "crash_ack_" + randID.String()
	t.Cleanup(func() { _ = js.DeleteConsumer(stream, consumerName) })

	filterSubject := prefix + "." + tenantID.String() + ".>"
	// 1. Create consumer with short AckWait so redelivery is prompt
	consumerConfig := consumer.DefaultConsumerConfigWithPrefix(stream, consumerName, prefix)
	consumerConfig.FilterSubject = filterSubject
	consumerConfig.AckWait = time.Second
	consumerConfig.MaxDeliver = 5
	if _, err := js.AddConsumer(stream, &consumerConfig); err != nil {
		t.Fatalf("add consumer: %v", err)
	}

	// 2. Publish version 1
	env1 := makeEnvelope(t, tenantID, aggregateID, 1)
	publishEnvelope(t, js, env1)

	// 3. Run runner with failpoint: stops immediately after DB commit before msg.Ack()
	sub1, err := js.PullSubscribe(filterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}

	var crashed atomic.Bool
	runner1 := consumer.NewRunnerForTesting(testReadDB, sub1, nil, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    1,
		FetchTimeout: 500 * time.Millisecond,
	}, func(env events.Envelope) error {
		crashed.Store(true)
		return errors.New("simulated consumer crash right before msg.Ack()")
	})

	r1Ctx, r1Cancel := context.WithCancel(context.Background())
	go func() { _ = runner1.Run(r1Ctx) }()

	// Wait for crash hook to trigger
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && !crashed.Load() {
		time.Sleep(50 * time.Millisecond)
	}
	r1Cancel()
	_ = runner1.Stop(context.Background())
	if !crashed.Load() {
		t.Fatal("failpoint before Ack was not reached")
	}

	// Verify DB transaction committed: cursor is 1 and applied_events_log has 1 row
	ctx := context.Background()
	var cursor int64
	err = testReadDB.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&cursor)
	if err != nil || cursor != 1 {
		t.Fatalf("cursor after crash = %d, want 1, err = %v", cursor, err)
	}
	var appliedCount int
	err = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM applied_events_log WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&appliedCount)
	if err != nil || appliedCount != 1 {
		t.Fatalf("applied count after crash = %d, want 1, err = %v", appliedCount, err)
	}

	// 4. Wait for JetStream AckWait (1s) to expire so message is redelivered
	time.Sleep(1200 * time.Millisecond)

	// 5. Start normal runner on the same consumer to handle the redelivery
	sub2, err := js.PullSubscribe(filterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe 2: %v", err)
	}

	runner2 := consumer.NewRunner(testReadDB, sub2, nil, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    1,
		FetchTimeout: 500 * time.Millisecond,
	})

	r2Ctx, r2Cancel := context.WithCancel(context.Background())
	defer r2Cancel()
	go func() { _ = runner2.Run(r2Ctx) }()

	// Wait for redelivery to be processed and ACKed
	time.Sleep(800 * time.Millisecond)

	// Verify NO second applied effect occurred: applied_events_log count is STILL 1
	err = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM applied_events_log WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&appliedCount)
	if err != nil || appliedCount != 1 {
		t.Fatalf("applied count after redelivery = %d, want 1 (deduplicated), err = %v", appliedCount, err)
	}

	err = testReadDB.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&cursor)
	if err != nil || cursor != 1 {
		t.Fatalf("cursor after redelivery = %d, want 1, err = %v", cursor, err)
	}
}

// Scenario 5: Deliver versions 3, 1, and 2 out of order; verify durable buffering and applied order 1, 2, 3.
func TestConsumer_OutOfOrderBufferingAndDrain(t *testing.T) {
	if testReadDB == nil {
		t.Skip("read test database not configured")
	}
	nc, js := getNATSConnection(t)
	defer nc.Close()

	stream := testStreamName()
	prefix := testSubjectPrefix()
	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	t.Cleanup(func() { cleanTenantReadData(t, tenantID) })
	aggregateID, _ := ids.Generate()
	randID, _ := ids.Generate()
	consumerName := "ooo_" + randID.String()
	t.Cleanup(func() { _ = js.DeleteConsumer(stream, consumerName) })

	filterSubject := prefix + "." + tenantID.String() + ".>"
	cfg := consumer.DefaultConsumerConfigWithPrefix(stream, consumerName, prefix)
	cfg.FilterSubject = filterSubject
	if _, err := js.AddConsumer(stream, &cfg); err != nil {
		t.Fatalf("add consumer: %v", err)
	}
	sub, err := js.PullSubscribe(filterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}

	env1 := makeEnvelope(t, tenantID, aggregateID, 1)
	env2 := makeEnvelope(t, tenantID, aggregateID, 2)
	env3 := makeEnvelope(t, tenantID, aggregateID, 3)

	runner := consumer.NewRunner(testReadDB, sub, nil, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    1,
		FetchTimeout: 200 * time.Millisecond,
	})

	rCtx, rCancel := context.WithCancel(context.Background())
	defer rCancel()
	go func() { _ = runner.Run(rCtx) }()

	// Deliver v3 first -> should be buffered
	publishEnvelope(t, js, env3)
	time.Sleep(300 * time.Millisecond)

	ctx := context.Background()
	var bufCount int
	_ = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM buffered_events WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&bufCount)
	if bufCount != 1 {
		t.Fatalf("buffered count after v3 = %d, want 1", bufCount)
	}

	// Deliver v1 next -> cursor advances to 1
	publishEnvelope(t, js, env1)
	time.Sleep(300 * time.Millisecond)

	var cursor int64
	_ = testReadDB.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&cursor)
	if cursor != 1 {
		t.Fatalf("cursor after v1 = %d, want 1", cursor)
	}

	// Deliver missing v2 -> applies v2 and drains buffered v3 -> cursor becomes 3
	publishEnvelope(t, js, env2)

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		_ = testReadDB.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
			tenantID, consumerName, aggregateID).Scan(&cursor)
		if cursor == 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cursor != 3 {
		t.Fatalf("final cursor = %d, want 3", cursor)
	}

	// Buffer must be empty
	_ = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM buffered_events WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&bufCount)
	if bufCount != 0 {
		t.Fatalf("final buffered count = %d, want 0", bufCount)
	}

	// applied_events_log must contain versions 1, 2, 3
	rows, err := testReadDB.QueryContext(ctx, "SELECT aggregate_version FROM applied_events_log WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3 ORDER BY aggregate_version ASC",
		tenantID, consumerName, aggregateID)
	if err != nil {
		t.Fatalf("query applied: %v", err)
	}
	defer rows.Close()

	var appliedVersions []int64
	for rows.Next() {
		var v int64
		_ = rows.Scan(&v)
		appliedVersions = append(appliedVersions, v)
	}
	if len(appliedVersions) != 3 || appliedVersions[0] != 1 || appliedVersions[1] != 2 || appliedVersions[2] != 3 {
		t.Fatalf("applied versions = %v, want [1 2 3]", appliedVersions)
	}
}

// Scenario 6: Leave a missing predecessor past the gap window; verify ordering_gaps,
// then fill the gap and verify buffered drain.
func TestConsumer_GapDetectionAndResolutionOnMissingPredecessor(t *testing.T) {
	if testReadDB == nil {
		t.Skip("read test database not configured")
	}
	nc, js := getNATSConnection(t)
	defer nc.Close()

	stream := testStreamName()
	prefix := testSubjectPrefix()
	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	t.Cleanup(func() { cleanTenantReadData(t, tenantID) })
	aggregateID, _ := ids.Generate()
	randID, _ := ids.Generate()
	consumerName := "gap_" + randID.String()
	t.Cleanup(func() { _ = js.DeleteConsumer(stream, consumerName) })

	filterSubject := prefix + "." + tenantID.String() + ".>"
	cfg := consumer.DefaultConsumerConfigWithPrefix(stream, consumerName, prefix)
	cfg.FilterSubject = filterSubject
	if _, err := js.AddConsumer(stream, &cfg); err != nil {
		t.Fatalf("add consumer: %v", err)
	}
	sub, err := js.PullSubscribe(filterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}

	runner := consumer.NewRunner(testReadDB, sub, nil, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    1,
		FetchTimeout: 200 * time.Millisecond,
	})

	rCtx, rCancel := context.WithCancel(context.Background())
	defer rCancel()
	go func() { _ = runner.Run(rCtx) }()

	// 1. Deliver version 2 only (v1 omitted) -> buffered
	env2 := makeEnvelope(t, tenantID, aggregateID, 2)
	publishEnvelope(t, js, env2)
	time.Sleep(300 * time.Millisecond)

	// 2. Run gap sweeper with a very small gap window (e.g., 50ms)
	sweeper := consumer.NewGapSweeper(testReadDB, consumer.SweeperConfig{
		ConsumerName: consumerName,
		GapWindow:    50 * time.Millisecond,
		PollInterval: 100 * time.Millisecond,
	})
	time.Sleep(100 * time.Millisecond) // ensure first_seen_at is older than GapWindow

	ctx := context.Background()
	if err := sweeper.Sweep(ctx); err != nil {
		t.Fatalf("sweeper sweep: %v", err)
	}

	// Verify ordering_gaps row created for expected_version = 1 with status 'OPEN'
	var gapStatus string
	err = testReadDB.QueryRowContext(ctx, "SELECT status FROM ordering_gaps WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3 AND expected_version = 1",
		tenantID, consumerName, aggregateID).Scan(&gapStatus)
	if err != nil || gapStatus != "OPEN" {
		t.Fatalf("expected OPEN gap for v1, got %q, err=%v", gapStatus, err)
	}

	// 3. Deliver missing predecessor version 1
	env1 := makeEnvelope(t, tenantID, aggregateID, 1)
	publishEnvelope(t, js, env1)

	// Wait for runner to apply v1 and drain buffered v2
	deadline := time.Now().Add(4 * time.Second)
	var cursor int64
	for time.Now().Before(deadline) {
		_ = testReadDB.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
			tenantID, consumerName, aggregateID).Scan(&cursor)
		if cursor == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cursor != 2 {
		t.Fatalf("cursor after gap fill = %d, want 2", cursor)
	}

	// Verify ordering gap is marked 'RESOLVED'
	err = testReadDB.QueryRowContext(ctx, "SELECT status FROM ordering_gaps WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3 AND expected_version = 1",
		tenantID, consumerName, aggregateID).Scan(&gapStatus)
	if err != nil || gapStatus != "RESOLVED" {
		t.Fatalf("expected RESOLVED gap for v1, got %q, err=%v", gapStatus, err)
	}

	// Buffer must be empty
	var bufCount int
	_ = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM buffered_events WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&bufCount)
	if bufCount != 0 {
		t.Fatalf("buffer not drained: %d remaining", bufCount)
	}
}

// Scenario 7: Restart the projector with buffered events and verify they survive in PostgreSQL.
func TestConsumer_BufferedEventsSurviveRestart(t *testing.T) {
	if testReadDB == nil {
		t.Skip("read test database not configured")
	}
	nc, js := getNATSConnection(t)
	defer nc.Close()

	stream := testStreamName()
	prefix := testSubjectPrefix()
	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	t.Cleanup(func() { cleanTenantReadData(t, tenantID) })
	aggregateID, _ := ids.Generate()
	randID, _ := ids.Generate()
	consumerName := "survive_" + randID.String()
	t.Cleanup(func() { _ = js.DeleteConsumer(stream, consumerName) })

	filterSubject := prefix + "." + tenantID.String() + ".>"
	cfg := consumer.DefaultConsumerConfigWithPrefix(stream, consumerName, prefix)
	cfg.FilterSubject = filterSubject
	if _, err := js.AddConsumer(stream, &cfg); err != nil {
		t.Fatalf("add consumer: %v", err)
	}
	sub1, err := js.PullSubscribe(filterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}

	// 1. Deliver version 2
	env2 := makeEnvelope(t, tenantID, aggregateID, 2)
	publishEnvelope(t, js, env2)

	// Run runner 1 until v2 is buffered
	runner1 := consumer.NewRunner(testReadDB, sub1, nil, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    1,
		FetchTimeout: 200 * time.Millisecond,
	})
	r1Ctx, r1Cancel := context.WithCancel(context.Background())
	go func() { _ = runner1.Run(r1Ctx) }()

	ctx := context.Background()
	deadline := time.Now().Add(3 * time.Second)
	var bufCount int
	for time.Now().Before(deadline) {
		_ = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM buffered_events WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
			tenantID, consumerName, aggregateID).Scan(&bufCount)
		if bufCount == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if bufCount != 1 {
		t.Fatalf("v2 was not buffered: count = %d", bufCount)
	}

	// 2. Stop Runner 1 completely (simulating process restart)
	r1Cancel()
	_ = runner1.Stop(ctx)
	_ = sub1.Unsubscribe()
	nc.Close()

	// Verify buffered row survives in PostgreSQL
	err = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM buffered_events WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3 AND aggregate_version = 2",
		tenantID, consumerName, aggregateID).Scan(&bufCount)
	if err != nil || bufCount != 1 {
		t.Fatalf("buffered event did not survive in DB: %v", err)
	}

	// 3. Start a fresh process / connection and Runner 2 on the same consumer
	nc2, js2 := getNATSConnection(t)
	defer nc2.Close()

	sub2, err := js2.PullSubscribe(filterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe 2: %v", err)
	}
	runner2 := consumer.NewRunner(testReadDB, sub2, nil, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    1,
		FetchTimeout: 200 * time.Millisecond,
	})
	r2Ctx, r2Cancel := context.WithCancel(context.Background())
	defer r2Cancel()
	go func() { _ = runner2.Run(r2Ctx) }()

	// 4. Publish version 1 -> Runner 2 must apply v1 and drain previously persisted v2
	env1 := makeEnvelope(t, tenantID, aggregateID, 1)
	publishEnvelope(t, js2, env1)

	var cursor int64
	deadline = time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		_ = testReadDB.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
			tenantID, consumerName, aggregateID).Scan(&cursor)
		if cursor == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cursor != 2 {
		t.Fatalf("cursor after restart and drain = %d, want 2", cursor)
	}

	_ = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM buffered_events WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&bufCount)
	if bufCount != 0 {
		t.Fatalf("buffer was not drained after restart: count = %d", bufCount)
	}
}

// Scenario 8: Deliver a poison event until MaxDeliver; verify dead_letter_events is written before ACK.
func TestConsumer_PoisonMessageWrittenToDeadLetterBeforeAck(t *testing.T) {
	if testReadDB == nil {
		t.Skip("read test database not configured")
	}
	nc, js := getNATSConnection(t)
	defer nc.Close()

	stream := testStreamName()
	prefix := testSubjectPrefix()
	ids := identifiers.NewGenerator()
	randID, _ := ids.Generate()
	consumerName := "dlq_" + randID.String()
	t.Cleanup(func() { _ = js.DeleteConsumer(stream, consumerName) })

	tenantID, _ := ids.Generate()
	t.Cleanup(func() { cleanTenantReadData(t, tenantID) })
	aggregateID, _ := ids.Generate()
	filterSubject := prefix + "." + tenantID.String() + ".>"

	cfg := consumer.DefaultConsumerConfigWithPrefix(stream, consumerName, prefix)
	cfg.FilterSubject = filterSubject
	cfg.MaxDeliver = 3
	cfg.AckWait = 500 * time.Millisecond
	if _, err := js.AddConsumer(stream, &cfg); err != nil {
		t.Fatalf("add consumer: %v", err)
	}
	sub, err := js.PullSubscribe(filterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}

	// Custom handler that always fails to simulate repeated processing errors
	failingHandler := consumer.HandlerFunc(func(ctx context.Context, tx *sql.Tx, envelope events.Envelope) error {
		return errors.New("deliberate handler failure for poison test")
	})

	runner := consumer.NewRunner(testReadDB, sub, failingHandler, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    1,
		FetchTimeout: 200 * time.Millisecond,
		MaxDeliver:   3,
	})

	rCtx, rCancel := context.WithCancel(context.Background())
	defer rCancel()
	go func() { _ = runner.Run(rCtx) }()

	env := makeEnvelope(t, tenantID, aggregateID, 1)
	publishEnvelope(t, js, env)

	// Wait for 3 delivery attempts, ending in dead-letter insertion and ACK
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	var dlCount int
	for time.Now().Before(deadline) {
		_ = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM dead_letter_events WHERE consumer_name = $1 AND event_id = $2",
			consumerName, env.EventID).Scan(&dlCount)
		if dlCount == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if dlCount != 1 {
		t.Fatalf("dead letter event was not recorded: count = %d", dlCount)
	}

	var deliveryCount int
	var errMsg string
	err = testReadDB.QueryRowContext(ctx, "SELECT delivery_count, error FROM dead_letter_events WHERE consumer_name = $1 AND event_id = $2",
		consumerName, env.EventID).Scan(&deliveryCount, &errMsg)
	if err != nil {
		t.Fatalf("query dead letter: %v", err)
	}
	if deliveryCount < 3 {
		t.Fatalf("delivery count = %d, want >= 3", deliveryCount)
	}
	if errMsg == "" {
		t.Fatal("dead letter error is empty")
	}
}

// Scenario 9: Duplicate delivery of N events yields exactly N applied_events_log rows.
func TestConsumer_DuplicateDeliveryYieldsExactAppliedCount(t *testing.T) {
	if testReadDB == nil {
		t.Skip("read test database not configured")
	}
	nc, js := getNATSConnection(t)
	defer nc.Close()

	stream := testStreamName()
	prefix := testSubjectPrefix()
	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	t.Cleanup(func() { cleanTenantReadData(t, tenantID) })
	aggregateID, _ := ids.Generate()
	randID, _ := ids.Generate()
	consumerName := "dedup_n_" + randID.String()
	t.Cleanup(func() { _ = js.DeleteConsumer(stream, consumerName) })

	filterSubject := prefix + "." + tenantID.String() + ".>"
	cfg := consumer.DefaultConsumerConfigWithPrefix(stream, consumerName, prefix)
	cfg.FilterSubject = filterSubject
	if _, err := js.AddConsumer(stream, &cfg); err != nil {
		t.Fatalf("add consumer: %v", err)
	}
	sub, err := js.PullSubscribe(filterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}

	runner := consumer.NewRunner(testReadDB, sub, nil, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    5,
		FetchTimeout: 200 * time.Millisecond,
	})

	rCtx, rCancel := context.WithCancel(context.Background())
	defer rCancel()
	go func() { _ = runner.Run(rCtx) }()

	const numEvents = 5
	envelopes := make([]events.Envelope, numEvents)
	for i := 0; i < numEvents; i++ {
		envelopes[i] = makeEnvelope(t, tenantID, aggregateID, int64(i+1))
		publishEnvelope(t, js, envelopes[i])
	}

	// Wait for in-order processing of 1..5
	ctx := context.Background()
	deadline := time.Now().Add(4 * time.Second)
	var cursor int64
	for time.Now().Before(deadline) {
		_ = testReadDB.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
			tenantID, consumerName, aggregateID).Scan(&cursor)
		if cursor == numEvents {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if cursor != numEvents {
		t.Fatalf("cursor = %d, want %d", cursor, numEvents)
	}

	// Deliver duplicate of each event (simulating NATS redelivery / relay duplicate publish with new MsgId)
	for _, env := range envelopes {
		// Create a message without Nats-Msg-Id dedupe by using a different msg ID header to test inbox dedupe
		subject, _ := events.SubjectWithPrefix(prefix, env.TenantID, env.AggregateType, env.EventType)
		payload, _ := json.Marshal(env)
		dupMsg := nats.NewMsg(subject)
		dupID, _ := ids.Generate()
		dupMsg.Header.Set(events.NatsMsgIDHeader, dupID.String())
		dupMsg.Data = payload
		_, _ = js.PublishMsg(dupMsg)
	}

	time.Sleep(800 * time.Millisecond)

	// Verify exactly numEvents rows in applied_events_log!
	var appliedCount int
	err = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM applied_events_log WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&appliedCount)
	if err != nil || appliedCount != numEvents {
		t.Fatalf("applied count = %d, want exactly %d", appliedCount, numEvents)
	}

	// Verify cursor is unchanged at numEvents
	_ = testReadDB.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&cursor)
	if cursor != numEvents {
		t.Fatalf("cursor = %d, want %d", cursor, numEvents)
	}
}

// Regression test for first-event race fixed in 3f6278f:
// Two concurrent deliveries for a brand-new aggregate (both carrying the same tenant_id
// and aggregate_id; one with version 1, one with version 2) must never leave an in-order
// event buffered.
func TestConsumer_ConcurrentDeliveriesBrandNewAggregate_NeverBuffersInOrder(t *testing.T) {
	if testReadDB == nil {
		t.Skip("read test database not configured")
	}
	nc, js := getNATSConnection(t)
	defer nc.Close()

	stream := testStreamName()
	prefix := testSubjectPrefix()
	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	t.Cleanup(func() { cleanTenantReadData(t, tenantID) })
	aggregateID, _ := ids.Generate()
	randID, _ := ids.Generate()
	consumerName := "first_race_" + randID.String()
	t.Cleanup(func() { _ = js.DeleteConsumer(stream, consumerName) })

	filterSubject := prefix + "." + tenantID.String() + ".>"
	cfg := consumer.DefaultConsumerConfigWithPrefix(stream, consumerName, prefix)
	cfg.FilterSubject = filterSubject
	cfg.MaxDeliver = 5
	if _, err := js.AddConsumer(stream, &cfg); err != nil {
		t.Fatalf("add consumer: %v", err)
	}

	sub1, err := js.PullSubscribe(filterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe 1: %v", err)
	}
	sub2, err := js.PullSubscribe(filterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe 2: %v", err)
	}

	env1 := makeEnvelope(t, tenantID, aggregateID, 1)
	env2 := makeEnvelope(t, tenantID, aggregateID, 2)

	// Runner 1 will process version 1. Its failpoint hook (afterCursorRead) triggers
	// when version 1 has acquired/checked the cursor (cursor = 0) but before committing.
	// In the failpoint, we publish version 2 and allow Runner 2 to fetch and process it concurrently.
	var hookTriggered atomic.Bool
	deliveryBStarted := make(chan struct{}, 1)

	runner1 := consumer.NewRunnerWithHooks(testReadDB, sub1, nil, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    1,
		FetchTimeout: 200 * time.Millisecond,
	}, func(ctx context.Context, env events.Envelope) error {
		if env.AggregateVersion == 1 {
			hookTriggered.Store(true)
			// Publish version 2 to JetStream now so Runner 2 picks it up while Runner 1 is inside its transaction!
			publishEnvelope(t, js, env2)
			// Signal that delivery B can proceed
			select {
			case deliveryBStarted <- struct{}{}:
			default:
			}
			// Hold the transaction open for a brief period to force Delivery B to interleave
			time.Sleep(500 * time.Millisecond)
		}
		return nil
	}, nil)

	runner2 := consumer.NewRunner(testReadDB, sub2, nil, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    1,
		FetchTimeout: 200 * time.Millisecond,
	})

	r1Ctx, r1Cancel := context.WithCancel(context.Background())
	defer r1Cancel()
	r2Ctx, r2Cancel := context.WithCancel(context.Background())
	defer r2Cancel()

	// 1. Publish version 1
	publishEnvelope(t, js, env1)

	// 2. Start Runner 1 only, ensuring Runner 1 fetches version 1
	go func() { _ = runner1.Run(r1Ctx) }()

	// 3. Wait for Runner 1 to read cursor=0 and enter afterCursorRead failpoint
	select {
	case <-deliveryBStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("failpoint hook was never triggered for delivery A")
	}

	// 4. Start Runner 2 to concurrently process version 2 while Runner 1 is still in transaction
	go func() { _ = runner2.Run(r2Ctx) }()

	// Wait for both deliveries to finish processing
	ctx := context.Background()
	deadline := time.Now().Add(6 * time.Second)
	var cursor int64
	for time.Now().Before(deadline) {
		_ = testReadDB.QueryRowContext(ctx, "SELECT last_version FROM aggregate_cursors WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
			tenantID, consumerName, aggregateID).Scan(&cursor)
		if cursor == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify the invariant: cursor must advance to 2, and buffered events must be 0!
	if cursor != 2 {
		t.Fatalf("cursor = %d, want 2 (in-order events must not be blocked)", cursor)
	}

	var bufCount int
	err = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM buffered_events WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&bufCount)
	if err != nil || bufCount != 0 {
		t.Fatalf("buffered count = %d, want 0 (concurrent delivery must never leave in-order event buffered)", bufCount)
	}

	var appliedCount int
	err = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM applied_events_log WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3",
		tenantID, consumerName, aggregateID).Scan(&appliedCount)
	if err != nil || appliedCount != 2 {
		t.Fatalf("applied count = %d, want 2", appliedCount)
	}
}

// Scenario 10: Run the end-to-end wallet flow with relays and projector enabled;
// compare journal event totals with applied-event totals.
func TestConsumer_EndToEndWalletRelayAndProjectorAppliedTotals(t *testing.T) {
	if testReadDB == nil {
		t.Skip("read test database not configured")
	}
	nc, js := getNATSConnection(t)
	defer nc.Close()

	stream := testStreamName()
	prefix := testSubjectPrefix()
	natsURL := os.Getenv("NATS_TEST_URL")
	if natsURL == "" {
		natsURL = "nats://127.0.0.1:4222"
	}
	walletDSN := os.Getenv("WALLET_TEST_DATABASE_URL")
	if walletDSN == "" {
		walletDSN = "postgres://wallet_source_role:wallet_source_dev_password@127.0.0.1:5432/wallet_source_test?sslmode=disable"
	}

	// Always launch dedicated isolated test wallet instance on ephemeral port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ephemeral port: %v", err)
	}
	walletAddr := l.Addr().String()
	_ = l.Close()
	walletURL := "http://" + walletAddr
	internalWalletAddr := os.Getenv("INTERNAL_HTTP_ADDR")
	if internalWalletAddr == "" {
		internalWalletAddr = "127.0.0.1:0"
	}

	walletCmd := exec.Command("go", "run", "digitalwallet/services/wallet")
	walletCmd.Env = append(os.Environ(),
		"SERVICE_NAME=wallet-test",
		"INSTANCE_NAME=wallet-source-test",
		"HTTP_ADDR="+walletAddr,
		"INTERNAL_HTTP_ADDR="+internalWalletAddr,
		"DATABASE_URL="+walletDSN,
		"NATS_URL="+natsURL,
		"NATS_STREAM="+stream,
		"NATS_SUBJECT_PREFIX="+prefix,
		"LOG_LEVEL=error",
	)
	if err := walletCmd.Start(); err != nil {
		t.Skipf("cannot start wallet service: %v", err)
	}
	defer func() {
		if walletCmd.Process != nil {
			_ = walletCmd.Process.Kill()
		}
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	// Wait for wallet ready
	ready := false
	for i := 0; i < 40; i++ {
		time.Sleep(200 * time.Millisecond)
		r, err := client.Get(walletURL + "/healthz")
		if err == nil && r.StatusCode == http.StatusOK {
			ready = true
			_ = r.Body.Close()
			break
		}
	}
	if !ready {
		t.Fatal("wallet service failed to start within 8s")
	}

	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	tIDStr := tenantID.String()
	t.Cleanup(func() {
		cleanTenantWalletData(t, tenantID)
		cleanTenantReadData(t, tenantID)
	})

	consumerName := "projector_test_" + tIDStr
	cfg := consumer.DefaultConsumerConfigWithPrefix(stream, consumerName, prefix)
	cfg.FilterSubject = prefix + "." + tIDStr + ".>"
	if _, err := js.AddConsumer(stream, &cfg); err != nil && !errors.Is(err, nats.ErrConsumerNameAlreadyInUse) {
		t.Fatalf("add consumer: %v", err)
	}
	t.Cleanup(func() { _ = js.DeleteConsumer(stream, consumerName) })

	sub, err := js.PullSubscribe(cfg.FilterSubject, consumerName, nats.Bind(stream, consumerName))
	if err != nil {
		t.Fatalf("pull subscribe: %v", err)
	}

	runner := consumer.NewRunner(testReadDB, sub, nil, consumer.RunnerConfig{
		StreamName:   stream,
		ConsumerName: consumerName,
		BatchSize:    10,
		FetchTimeout: 200 * time.Millisecond,
	})
	rCtx, rCancel := context.WithCancel(context.Background())
	defer rCancel()
	go func() { _ = runner.Run(rCtx) }()

	// Execute end-to-end wallet operations via HTTP API:
	// 1. Create Wallet (emits wallet.created version 1)
	createReq, _ := json.Marshal(map[string]string{"external_reference": "e2e-ref-" + tIDStr})
	req, _ := http.NewRequestWithContext(context.Background(), "POST", walletURL+"/v1/wallets", bytes.NewReader(createReq))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tIDStr)
	req.Header.Set("Idempotency-Key", "create-wallet-"+tIDStr)

	createResp, err := client.Do(req)
	if err != nil || createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create wallet: status=%d, err=%v", createResp.StatusCode, err)
	}
	body, _ := io.ReadAll(createResp.Body)
	_ = createResp.Body.Close()
	var createdWallet struct {
		WalletID string `json:"wallet_id"`
	}
	_ = json.Unmarshal(body, &createdWallet)
	if createdWallet.WalletID == "" {
		t.Fatalf("no wallet_id in response: %s", string(body))
	}
	wID := createdWallet.WalletID

	// 2. Deposit $50 (emits wallet.deposit_posted version 2)
	depReq, _ := json.Marshal(map[string]any{"amount_minor": 5000, "currency": "USD", "reference": "deposit-1"})
	req, _ = http.NewRequestWithContext(context.Background(), "POST", walletURL+"/v1/wallets/"+wID+"/deposit", bytes.NewReader(depReq))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tIDStr)
	req.Header.Set("Idempotency-Key", "dep-"+tIDStr)
	depResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("deposit request failed: %v", err)
	}
	b, _ := io.ReadAll(depResp.Body)
	_ = depResp.Body.Close()
	if depResp.StatusCode != http.StatusOK && depResp.StatusCode != http.StatusCreated {
		t.Fatalf("deposit failed: status=%d, body=%s", depResp.StatusCode, string(b))
	}

	// 3. Withdraw $20 (emits wallet.withdrawal_posted version 3)
	withReq, _ := json.Marshal(map[string]any{"amount_minor": 2000, "currency": "USD", "reference": "withdraw-1"})
	req, _ = http.NewRequestWithContext(context.Background(), "POST", walletURL+"/v1/wallets/"+wID+"/withdraw", bytes.NewReader(withReq))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tIDStr)
	req.Header.Set("Idempotency-Key", "with-"+tIDStr)
	withResp, err := client.Do(req)
	if err != nil || (withResp.StatusCode != http.StatusOK && withResp.StatusCode != http.StatusCreated) {
		t.Fatalf("withdraw failed: status=%d, err=%v", withResp.StatusCode, err)
	}
	_ = withResp.Body.Close()

	// Connect to wallet_source_test DB to query journal & events table directly
	walletDB, err := sql.Open("pgx", walletDSN)
	if err != nil {
		t.Fatalf("connect wallet db: %v", err)
	}
	defer walletDB.Close()

	ctx := context.Background()
	// Count journal transactions / events in wallet_source_test DB for this tenant
	var walletEventCount int
	err = walletDB.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE tenant_id = $1", tenantID).Scan(&walletEventCount)
	if err != nil {
		t.Fatalf("count wallet events: %v", err)
	}
	if walletEventCount != 3 {
		t.Fatalf("wallet event count = %d, want 3", walletEventCount)
	}

	// 4. Wait for projector runner to consume all 3 events and write to applied_events_log
	deadline := time.Now().Add(10 * time.Second)
	var appliedCount int
	for time.Now().Before(deadline) {
		_ = testReadDB.QueryRowContext(ctx, "SELECT count(*) FROM applied_events_log WHERE tenant_id = $1 AND consumer_name = $2",
			tenantID, consumerName).Scan(&appliedCount)
		if appliedCount == walletEventCount {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if appliedCount != walletEventCount {
		t.Fatalf("applied events count = %d, want %d (matching wallet journal)", appliedCount, walletEventCount)
	}

	// 5. Query applied versions to verify contiguous order 1, 2, 3
	rows, err := testReadDB.QueryContext(ctx, "SELECT aggregate_version, event_type FROM applied_events_log WHERE tenant_id = $1 AND consumer_name = $2 ORDER BY aggregate_version ASC",
		tenantID, consumerName)
	if err != nil {
		t.Fatalf("query applied: %v", err)
	}
	defer rows.Close()

	var versions []int64
	var eventTypes []string
	for rows.Next() {
		var v int64
		var et string
		_ = rows.Scan(&v, &et)
		versions = append(versions, v)
		eventTypes = append(eventTypes, et)
	}
	if len(versions) != 3 || versions[0] != 1 || versions[1] != 2 || versions[2] != 3 {
		t.Fatalf("applied versions = %v, want [1 2 3]", versions)
	}
	if eventTypes[0] != "wallet.created" || eventTypes[1] != "wallet.deposit_posted" || eventTypes[2] != "wallet.withdrawal_posted" {
		t.Fatalf("applied event types = %v", eventTypes)
	}
}
