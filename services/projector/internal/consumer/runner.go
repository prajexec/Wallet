package consumer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
	"digitalwallet/shared/ordering"
	"github.com/nats-io/nats.go"
)

var (
	// ErrRunnerRunning indicates the consumer runner is already active.
	ErrRunnerRunning = errors.New("projector consumer runner is already running")
	// ErrSweeperRunning indicates the gap sweeper is already active.
	ErrSweeperRunning = errors.New("gap sweeper is already running")
)

// Handler processes an applied domain event within an active database transaction.
type Handler interface {
	HandleEvent(ctx context.Context, tx *sql.Tx, envelope events.Envelope) error
}

// HandlerFunc adapts a function to the Handler interface.
type HandlerFunc func(ctx context.Context, tx *sql.Tx, envelope events.Envelope) error

func (f HandlerFunc) HandleEvent(ctx context.Context, tx *sql.Tx, envelope events.Envelope) error {
	return f(ctx, tx, envelope)
}

// AppliedEventsLogHandler is the Phase 4 trivial projection handler that records
// applied events into the applied_events_log table. Phase 5 replaces this with real projections.
type AppliedEventsLogHandler struct {
	ConsumerName string
}

func (h *AppliedEventsLogHandler) HandleEvent(ctx context.Context, tx *sql.Tx, envelope events.Envelope) error {
	consumerName := h.ConsumerName
	if consumerName == "" {
		consumerName = "projector"
	}
	appliedAt := time.Now().UTC()
	const query = `
INSERT INTO applied_events_log (tenant_id, consumer_name, event_id, aggregate_id, aggregate_version, event_type, applied_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (tenant_id, consumer_name, aggregate_id, aggregate_version) DO NOTHING;
`
	_, err := tx.ExecContext(ctx, query,
		envelope.TenantID,
		consumerName,
		envelope.EventID,
		envelope.AggregateID,
		envelope.AggregateVersion,
		envelope.EventType,
		appliedAt,
	)
	return err
}

// RunnerConfig configures the ordered consumer runner.
type RunnerConfig struct {
	StreamName   string
	ConsumerName string
	BatchSize    int
	FetchTimeout time.Duration
	MaxDeliver   int
	Logger       *slog.Logger
	Now          func() time.Time
}

func (c RunnerConfig) withDefaults() RunnerConfig {
	if c.StreamName == "" {
		c.StreamName = "WALLET_EVENTS"
	}
	if c.ConsumerName == "" {
		c.ConsumerName = "projector"
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 10
	}
	if c.FetchTimeout <= 0 {
		c.FetchTimeout = time.Second
	}
	if c.MaxDeliver <= 0 {
		c.MaxDeliver = 5
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Now == nil {
		c.Now = func() time.Time { return time.Now().UTC() }
	}
	return c
}

// MessageFetcher abstracts fetching messages from NATS JetStream for pull consumers.
type MessageFetcher interface {
	Fetch(int, ...nats.PullOpt) ([]*nats.Msg, error)
}

// Runner consumes messages from JetStream, applies per-aggregate ordering and deduplication,
// and invokes the handler inside a single database transaction before acknowledging.
type Runner struct {
	db      *sql.DB
	fetcher MessageFetcher
	handler Handler
	config  RunnerConfig

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}

	appliedCount    atomic.Uint64
	duplicateCount  atomic.Uint64
	bufferedCount   atomic.Uint64
	deadLetterCount atomic.Uint64
}

// NewRunner creates a new ordered consumer runner.
func NewRunner(db *sql.DB, fetcher MessageFetcher, handler Handler, config RunnerConfig) *Runner {
	if handler == nil {
		handler = &AppliedEventsLogHandler{ConsumerName: config.ConsumerName}
	}
	return &Runner{
		db:      db,
		fetcher: fetcher,
		handler: handler,
		config:  config.withDefaults(),
	}
}

// Run processes messages until the context is canceled.
func (r *Runner) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		cancel()
		return ErrRunnerRunning
	}
	r.running = true
	r.cancel = cancel
	r.done = make(chan struct{})
	done := r.done
	r.mu.Unlock()

	defer func() {
		cancel()
		r.mu.Lock()
		r.running = false
		r.cancel = nil
		r.done = nil
		close(done)
		r.mu.Unlock()
	}()

	for {
		if runCtx.Err() != nil {
			return nil
		}

		msgs, err := r.fetcher.Fetch(r.config.BatchSize, nats.Context(runCtx))
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, nats.ErrTimeout) {
				continue
			}
			r.config.Logger.ErrorContext(runCtx, "fetch jetstream messages", "error", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		for _, msg := range msgs {
			if runCtx.Err() != nil {
				return nil
			}
			r.processOne(runCtx, msg)
		}
	}
}

// Stop requests the runner to stop and waits for it to exit.
func (r *Runner) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return nil
	}
	cancel := r.cancel
	done := r.done
	cancel()
	r.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) processOne(ctx context.Context, msg *nats.Msg) {
	meta, _ := msg.Metadata()
	var deliveryCount int
	if meta != nil {
		deliveryCount = int(meta.NumDelivered)
	}
	if deliveryCount <= 0 {
		deliveryCount = 1
	}

	var envelope events.Envelope
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		r.handlePoison(ctx, msg, identifiers.UUID{}, identifiers.UUID{}, 0, msg.Data, fmt.Sprintf("unmarshal event: %v", err), deliveryCount)
		return
	}
	if err := envelope.Validate(); err != nil {
		r.handlePoison(ctx, msg, envelope.EventID, envelope.AggregateID, envelope.AggregateVersion, msg.Data, fmt.Sprintf("invalid envelope: %v", err), deliveryCount)
		return
	}

	err := r.processEventTx(ctx, envelope)
	if err != nil {
		if deliveryCount >= r.config.MaxDeliver {
			r.handlePoison(ctx, msg, envelope.EventID, envelope.AggregateID, envelope.AggregateVersion, msg.Data, err.Error(), deliveryCount)
			return
		}
		r.config.Logger.ErrorContext(ctx, "failed processing event, naking", "tenant_id", envelope.TenantID, "event_id", envelope.EventID, "delivery_count", deliveryCount, "error", err)
		_ = msg.Nak()
		return
	}

	// ACK only after the database transaction has committed!
	if err := msg.Ack(); err != nil {
		r.config.Logger.ErrorContext(ctx, "failed acknowledging message after commit", "error", err)
	}
}

func (r *Runner) processEventTx(ctx context.Context, envelope events.Envelope) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := r.config.Now()

	// 1. Insert into inbox_messages. Unique violation means duplicate -> ACK without effect.
	const insertInbox = `
INSERT INTO inbox_messages (tenant_id, consumer_name, event_id, aggregate_type, aggregate_id, aggregate_version, event_type, payload, received_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, consumer_name, event_id) DO NOTHING;
`
	payloadJSON, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope payload: %w", err)
	}

	res, err := tx.ExecContext(ctx, insertInbox,
		envelope.TenantID,
		r.config.ConsumerName,
		envelope.EventID,
		envelope.AggregateType,
		envelope.AggregateID,
		envelope.AggregateVersion,
		envelope.EventType,
		payloadJSON,
		now,
	)
	if err != nil {
		return fmt.Errorf("insert inbox message: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rows affected for inbox: %w", err)
	}
	if rowsAffected == 0 {
		// Duplicate message already in inbox!
		r.duplicateCount.Add(1)
		return tx.Commit()
	}

	// 2. Read the aggregate cursor with FOR UPDATE to serialize aggregate processing.
	const selectCursor = `
SELECT last_version
FROM aggregate_cursors
WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3
FOR UPDATE;
`
	var currentVersion int64
	cursorErr := tx.QueryRowContext(ctx, selectCursor, envelope.TenantID, r.config.ConsumerName, envelope.AggregateID).Scan(&currentVersion)
	if errors.Is(cursorErr, sql.ErrNoRows) {
		currentVersion = 0
	} else if cursorErr != nil {
		return fmt.Errorf("read aggregate cursor: %w", cursorErr)
	}

	// Read buffered versions for this aggregate.
	const selectBuffered = `
SELECT aggregate_version
FROM buffered_events
WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3
ORDER BY aggregate_version ASC;
`
	rows, err := tx.QueryContext(ctx, selectBuffered, envelope.TenantID, r.config.ConsumerName, envelope.AggregateID)
	if err != nil {
		return fmt.Errorf("read buffered versions: %w", err)
	}
	defer rows.Close()

	var bufferedVersions []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("scan buffered version: %w", err)
		}
		bufferedVersions = append(bufferedVersions, v)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("buffered versions rows error: %w", err)
	}

	// 3. Apply pure ordering decision.
	decision := ordering.Decide(ordering.Input{
		CurrentVersion:   currentVersion,
		IncomingVersion:  envelope.AggregateVersion,
		BufferedVersions: bufferedVersions,
	})

	switch decision.Kind {
	case ordering.DuplicateOrStale:
		r.duplicateCount.Add(1)
		// Stale or duplicate version: already in inbox, cursor unchanged.

	case ordering.FutureBuffer:
		// Version > current + 1: persist into durable buffered_events table.
		const insertBuffer = `
INSERT INTO buffered_events (tenant_id, consumer_name, aggregate_id, aggregate_version, event_id, aggregate_type, event_type, payload, first_seen_at, last_seen_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
ON CONFLICT (tenant_id, consumer_name, aggregate_id, aggregate_version)
DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at;
`
		_, err := tx.ExecContext(ctx, insertBuffer,
			envelope.TenantID,
			r.config.ConsumerName,
			envelope.AggregateID,
			envelope.AggregateVersion,
			envelope.EventID,
			envelope.AggregateType,
			envelope.EventType,
			payloadJSON,
			now,
		)
		if err != nil {
			return fmt.Errorf("insert buffered event: %w", err)
		}
		r.bufferedCount.Add(1)

	case ordering.InOrder, ordering.GapFillDrain:
		// Version == current + 1: apply handler, advance cursor, then drain successors.
		// First apply incoming event:
		if err := r.handler.HandleEvent(ctx, tx, envelope); err != nil {
			return fmt.Errorf("handle event: %w", err)
		}
		r.appliedCount.Add(1)

		// Drain any buffered successors in order within the same transaction:
		for _, ver := range decision.AppliedVersions[1:] {
			// Fetch buffered payload:
			const selectBufferedRow = `
SELECT event_id, aggregate_type, event_type, payload
FROM buffered_events
WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3 AND aggregate_version = $4;
`
			var (
				bEventID       identifiers.UUID
				bAggregateType string
				bEventType     string
				bPayload       []byte
			)
			err := tx.QueryRowContext(ctx, selectBufferedRow, envelope.TenantID, r.config.ConsumerName, envelope.AggregateID, ver).Scan(
				&bEventID, &bAggregateType, &bEventType, &bPayload,
			)
			if err != nil {
				return fmt.Errorf("fetch buffered successor v%d: %w", ver, err)
			}

			var bEnvelope events.Envelope
			if err := json.Unmarshal(bPayload, &bEnvelope); err != nil {
				return fmt.Errorf("unmarshal buffered successor v%d: %w", ver, err)
			}

			if err := r.handler.HandleEvent(ctx, tx, bEnvelope); err != nil {
				return fmt.Errorf("handle buffered successor v%d: %w", ver, err)
			}
			r.appliedCount.Add(1)

			// Delete from buffered_events
			const deleteBuffer = `
DELETE FROM buffered_events
WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3 AND aggregate_version = $4;
`
			if _, err := tx.ExecContext(ctx, deleteBuffer, envelope.TenantID, r.config.ConsumerName, envelope.AggregateID, ver); err != nil {
				return fmt.Errorf("delete drained buffered event v%d: %w", ver, err)
			}

			// If gap existed for this resolved version, resolve it in ordering_gaps
			const resolveGap = `
UPDATE ordering_gaps
SET status = 'RESOLVED', resolved_at = $5
WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3 AND expected_version = $4 AND status = 'OPEN';
`
			if _, err := tx.ExecContext(ctx, resolveGap, envelope.TenantID, r.config.ConsumerName, envelope.AggregateID, ver, now); err != nil {
				return fmt.Errorf("resolve gap for version %d: %w", ver, err)
			}
		}

		// Also resolve gap if the incoming version itself was expected:
		const resolveIncomingGap = `
UPDATE ordering_gaps
SET status = 'RESOLVED', resolved_at = $5
WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3 AND expected_version = $4 AND status = 'OPEN';
`
		if _, err := tx.ExecContext(ctx, resolveIncomingGap, envelope.TenantID, r.config.ConsumerName, envelope.AggregateID, envelope.AggregateVersion, now); err != nil {
			return fmt.Errorf("resolve incoming gap for version %d: %w", envelope.AggregateVersion, err)
		}

		// Advance cursor to decision.NextVersion:
		const upsertCursor = `
INSERT INTO aggregate_cursors (tenant_id, consumer_name, aggregate_id, last_version, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, consumer_name, aggregate_id)
DO UPDATE SET last_version = EXCLUDED.last_version, updated_at = EXCLUDED.updated_at;
`
		if _, err := tx.ExecContext(ctx, upsertCursor, envelope.TenantID, r.config.ConsumerName, envelope.AggregateID, decision.NextVersion, now); err != nil {
			return fmt.Errorf("update aggregate cursor: %w", err)
		}

	default:
		return fmt.Errorf("invalid ordering decision: %s", decision.Kind)
	}

	return tx.Commit()
}

func (r *Runner) handlePoison(ctx context.Context, msg *nats.Msg, eventID, aggregateID identifiers.UUID, version int64, payload []byte, errMsg string, deliveryCount int) {
	r.deadLetterCount.Add(1)
	r.config.Logger.ErrorContext(ctx, "recording poison message in dead letter queue",
		"event_id", eventID,
		"delivery_count", deliveryCount,
		"error", errMsg,
	)

	if eventID.IsZero() {
		eventID, _ = identifiers.NewGenerator().Generate()
	}
	if aggregateID.IsZero() {
		aggregateID, _ = identifiers.NewGenerator().Generate()
	}

	const insertDeadLetter = `
INSERT INTO dead_letter_events (tenant_id, consumer_name, event_id, aggregate_id, aggregate_version, payload, error, delivery_count, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, consumer_name, event_id) DO NOTHING;
`
	// Fallback tenant ID: extract from payload if available, else generate
	tenantID := extractTenantID(payload)
	if tenantID.IsZero() {
		tenantID, _ = identifiers.NewGenerator().Generate()
	}

	if len(payload) == 0 {
		payload = []byte("{}")
	} else if !json.Valid(payload) {
		payload, _ = json.Marshal(map[string]string{"raw_payload": string(payload)})
	}

	_, err := r.db.ExecContext(ctx, insertDeadLetter,
		tenantID,
		r.config.ConsumerName,
		eventID,
		aggregateID,
		version,
		payload,
		errMsg,
		deliveryCount,
		r.config.Now(),
	)
	if err != nil {
		r.config.Logger.ErrorContext(ctx, "failed to record dead letter event", "error", err)
	}

	_ = msg.Ack()
}

func extractTenantID(payload []byte) identifiers.UUID {
	var partial struct {
		TenantID string `json:"tenant_id"`
	}
	if err := json.Unmarshal(payload, &partial); err == nil && partial.TenantID != "" {
		id, err := identifiers.Parse(partial.TenantID)
		if err == nil {
			return id
		}
	}
	return identifiers.UUID{}
}

// SweeperConfig configures the gap detection sweeper.
type SweeperConfig struct {
	ConsumerName string
	GapWindow    time.Duration
	PollInterval time.Duration
	Logger       *slog.Logger
	Now          func() time.Time
}

func (c SweeperConfig) withDefaults() SweeperConfig {
	if c.ConsumerName == "" {
		c.ConsumerName = "projector"
	}
	if c.GapWindow <= 0 {
		c.GapWindow = 30 * time.Second
	}
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Now == nil {
		c.Now = func() time.Time { return time.Now().UTC() }
	}
	return c
}

// GapSweeper periodically finds buffered events whose missing predecessors have not
// arrived within the gap window, writes ordering_gaps rows, and emits structured logs.
type GapSweeper struct {
	db     *sql.DB
	config SweeperConfig

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	done     chan struct{}
	gapCount atomic.Uint64
}

// NewGapSweeper creates a gap detection sweeper.
func NewGapSweeper(db *sql.DB, config SweeperConfig) *GapSweeper {
	return &GapSweeper{
		db:     db,
		config: config.withDefaults(),
	}
}

// Run executes the gap sweeper loop until context is canceled.
func (s *GapSweeper) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		cancel()
		return ErrSweeperRunning
	}
	s.running = true
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()

	defer func() {
		cancel()
		s.mu.Lock()
		s.running = false
		s.cancel = nil
		s.done = nil
		close(done)
		s.mu.Unlock()
	}()

	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-runCtx.Done():
			return nil
		case <-ticker.C:
			if err := s.Sweep(runCtx); err != nil && !errors.Is(err, context.Canceled) {
				s.config.Logger.ErrorContext(runCtx, "gap sweep error", "error", err)
			}
		}
	}
}

// Stop terminates the sweeper loop.
func (s *GapSweeper) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	done := s.done
	cancel()
	s.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Sweep checks for expired gaps once.
func (s *GapSweeper) Sweep(ctx context.Context) error {
	now := s.config.Now()
	cutoff := now.Add(-s.config.GapWindow)

	// Query buffered events older than cutoff:
	const query = `
SELECT b.tenant_id, b.aggregate_id, b.aggregate_version,
       COALESCE(c.last_version, 0) AS current_version
FROM buffered_events b
LEFT JOIN aggregate_cursors c
  ON c.tenant_id = b.tenant_id
 AND c.consumer_name = b.consumer_name
 AND c.aggregate_id = b.aggregate_id
WHERE b.consumer_name = $1
  AND b.first_seen_at <= $2
ORDER BY b.tenant_id, b.aggregate_id, b.aggregate_version ASC;
`
	rows, err := s.db.QueryContext(ctx, query, s.config.ConsumerName, cutoff)
	if err != nil {
		return fmt.Errorf("query expired buffered events: %w", err)
	}
	defer rows.Close()

	type gapCandidate struct {
		tenantID       identifiers.UUID
		aggregateID    identifiers.UUID
		bufferVersion  int64
		currentVersion int64
	}

	var candidates []gapCandidate
	for rows.Next() {
		var c gapCandidate
		if err := rows.Scan(&c.tenantID, &c.aggregateID, &c.bufferVersion, &c.currentVersion); err != nil {
			return fmt.Errorf("scan gap candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("gap candidates rows error: %w", err)
	}

	for _, cand := range candidates {
		// Detect all missing versions between currentVersion + 1 and bufferVersion - 1
		for expected := cand.currentVersion + 1; expected < cand.bufferVersion; expected++ {
			// Check if expected version is already buffered:
			var exists bool
			const checkBuf = `
SELECT EXISTS(
    SELECT 1 FROM buffered_events
    WHERE tenant_id = $1 AND consumer_name = $2 AND aggregate_id = $3 AND aggregate_version = $4
);
`
			if err := s.db.QueryRowContext(ctx, checkBuf, cand.tenantID, s.config.ConsumerName, cand.aggregateID, expected).Scan(&exists); err != nil {
				return fmt.Errorf("check buffered version: %w", err)
			}
			if exists {
				continue
			}

			// Record gap
			const upsertGap = `
INSERT INTO ordering_gaps (tenant_id, consumer_name, aggregate_id, expected_version, status, first_seen_at, last_alerted_at, resolved_at)
VALUES ($1, $2, $3, $4, 'OPEN', $5, $5, NULL)
ON CONFLICT (tenant_id, consumer_name, aggregate_id, expected_version)
DO UPDATE SET last_alerted_at = EXCLUDED.last_alerted_at
WHERE ordering_gaps.status = 'OPEN';
`
			res, err := s.db.ExecContext(ctx, upsertGap, cand.tenantID, s.config.ConsumerName, cand.aggregateID, expected, now)
			if err != nil {
				return fmt.Errorf("upsert ordering gap: %w", err)
			}
			rowsAff, _ := res.RowsAffected()
			if rowsAff > 0 {
				s.gapCount.Add(1)
				s.config.Logger.ErrorContext(ctx, "ordering gap detected: missing predecessor version",
					"tenant_id", cand.tenantID,
					"consumer_name", s.config.ConsumerName,
					"aggregate_id", cand.aggregateID,
					"expected_version", expected,
					"buffered_version", cand.bufferVersion,
				)
			}
		}
	}

	return nil
}

// GapCount returns the count of open gap detections emitted.
func (s *GapSweeper) GapCount() uint64 {
	return s.gapCount.Load()
}

// Stats holds observability metrics for projector ordered consumption.
type Stats struct {
	Service                     string          `json:"service"`
	UnpublishedOutboxCount      int64           `json:"unpublished_outbox_count"`
	OldestUnpublishedAgeSeconds float64         `json:"oldest_unpublished_age_seconds"`
	AttemptsHistogram           map[int32]int64 `json:"attempts_histogram"`
	BufferedEventCount          int64           `json:"buffered_event_count"`
	OpenGapCount                int64           `json:"open_gap_count"`
	DeadLetterCount             int64           `json:"dead_letter_count"`
}

// QueryStats queries projector metrics from the read database.
func QueryStats(ctx context.Context, db *sql.DB, consumerName string) (Stats, error) {
	if consumerName == "" {
		consumerName = "projector"
	}
	stats := Stats{
		Service:           "projector",
		AttemptsHistogram: make(map[int32]int64),
	}

	const qBuf = `SELECT count(*) FROM buffered_events WHERE consumer_name = $1;`
	if err := db.QueryRowContext(ctx, qBuf, consumerName).Scan(&stats.BufferedEventCount); err != nil {
		return stats, fmt.Errorf("count buffered events: %w", err)
	}

	const qGaps = `SELECT count(*) FROM ordering_gaps WHERE consumer_name = $1 AND status = 'OPEN';`
	if err := db.QueryRowContext(ctx, qGaps, consumerName).Scan(&stats.OpenGapCount); err != nil {
		return stats, fmt.Errorf("count open gaps: %w", err)
	}

	const qDL = `SELECT count(*) FROM dead_letter_events WHERE consumer_name = $1;`
	if err := db.QueryRowContext(ctx, qDL, consumerName).Scan(&stats.DeadLetterCount); err != nil {
		return stats, fmt.Errorf("count dead letter events: %w", err)
	}

	return stats, nil
}
