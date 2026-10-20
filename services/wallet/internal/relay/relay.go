// Package relay publishes committed wallet outbox messages to JetStream.
package relay

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	walletdb "digitalwallet/services/wallet/internal/storage/generated"
	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

const (
	defaultBatchSize      int32 = 50
	defaultPollInterval         = 250 * time.Millisecond
	defaultLeaseDuration        = 5 * time.Second
	defaultPublishTimeout       = 2 * time.Second
	defaultBaseBackoff          = 500 * time.Millisecond
	defaultMaxBackoff           = time.Minute
	maxStoredErrorLength        = 2048
)

var (
	// ErrRelayRunning prevents two Run loops from sharing one relay instance.
	ErrRelayRunning = errors.New("wallet outbox relay is already running")
	// ErrLeaseLost means another relay reclaimed the row before this worker
	// could update its delivery metadata.
	ErrLeaseLost = errors.New("wallet outbox lease was lost")
	// errAfterPublish is deliberately reachable only through the unexported test
	// hook. It models a process stopping after PubAck and before the DB mark.
	errAfterPublish = errors.New("wallet outbox test failpoint after publish")
)

// JitterFunc adds deterministic or caller-selected jitter to a retry delay.
// delay is the exponential component before jitter is applied. The returned
// value is clamped to the configured maximum backoff.
type JitterFunc func(attempt int, eventID identifiers.UUID, delay time.Duration) time.Duration

// Config controls relay scheduling and retry behavior.
type Config struct {
	BatchSize      int32
	PollInterval   time.Duration
	LeaseDuration  time.Duration
	PublishTimeout time.Duration
	BaseBackoff    time.Duration
	MaxBackoff     time.Duration
	SubjectPrefix  string
	Jitter         JitterFunc
	Logger         *slog.Logger

	// afterPublish is intentionally unexported. Tests in this package use it
	// to stop between PubAck and MarkOutboxPublished; production composition
	// cannot inject this failpoint through the public constructor.
	afterPublish func(OutboxMessage) error
}

// OutboxMessage is the immutable event data and lease identity claimed by a
// relay worker. LeaseUntil is used in every metadata update so an expired,
// reclaimed lease cannot be changed by the old worker.
type OutboxMessage struct {
	TenantID      identifiers.UUID
	ID            identifiers.UUID
	EventID       identifiers.UUID
	CreatedAt     time.Time
	Attempts      int32
	LeaseUntil    time.Time
	AggregateType string
	EventType     string
	Payload       []byte
}

type outboxStore interface {
	Claim(context.Context, int32, time.Time) ([]OutboxMessage, error)
	MarkPublished(context.Context, OutboxMessage, time.Time) error
	MarkFailed(context.Context, OutboxMessage, string, time.Time) error
}

type publisher interface {
	PublishMsg(*nats.Msg, ...nats.PubOpt) (*nats.PubAck, error)
}

// Relay owns one polling loop. It does not hold a PostgreSQL transaction while
// publishing to NATS.
type Relay struct {
	store     outboxStore
	publisher publisher
	config    Config
	now       func() time.Time

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// New constructs a production relay backed by the supplied wallet pool and
// JetStream context.
func New(pool *pgxpool.Pool, jetStream nats.JetStreamContext, config Config) *Relay {
	return newRelay(&poolStore{pool: pool}, jetStream, config)
}

// NewForTesting constructs a relay with an unexported test failpoint hook.
// It is intended for test suites and cannot be injected via production New.
func NewForTesting(pool *pgxpool.Pool, jetStream nats.JetStreamContext, config Config, afterPublish func(OutboxMessage) error) *Relay {
	config.afterPublish = afterPublish
	return newRelay(&poolStore{pool: pool}, jetStream, config)
}

func newRelay(store outboxStore, jetStream publisher, config Config) *Relay {
	config = withDefaults(config)
	return &Relay{
		store:     store,
		publisher: jetStream,
		config:    config,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func withDefaults(config Config) Config {
	if config.BatchSize <= 0 {
		config.BatchSize = defaultBatchSize
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.PublishTimeout <= 0 {
		config.PublishTimeout = defaultPublishTimeout
	}
	if config.LeaseDuration <= config.PublishTimeout {
		config.LeaseDuration = 2 * config.PublishTimeout
	}
	if config.BaseBackoff <= 0 {
		config.BaseBackoff = defaultBaseBackoff
	}
	if config.MaxBackoff < config.BaseBackoff {
		config.MaxBackoff = defaultMaxBackoff
		if config.MaxBackoff < config.BaseBackoff {
			config.MaxBackoff = config.BaseBackoff
		}
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Jitter == nil {
		config.Jitter = deterministicJitter
	}
	return config
}

// Run claims, publishes, and records outbox messages until ctx is canceled.
// Transient database and NATS errors are recorded/logged and do not terminate
// the relay process; the lease or retry metadata makes the row eligible again.
func (r *Relay) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runContext, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		cancel()
		return ErrRelayRunning
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
		if runContext.Err() != nil {
			return nil
		}
		leaseUntil := r.now().UTC().Add(r.config.LeaseDuration)
		messages, err := r.store.Claim(runContext, r.config.BatchSize, leaseUntil)
		if err != nil {
			r.config.Logger.ErrorContext(runContext, "claim wallet outbox messages", "error", err)
			if !wait(runContext, r.config.PollInterval) {
				return nil
			}
			continue
		}
		if len(messages) == 0 {
			if !wait(runContext, r.config.PollInterval) {
				return nil
			}
			continue
		}
		for _, message := range messages {
			if err := r.publishOne(runContext, message); err != nil {
				r.config.Logger.ErrorContext(runContext, "publish wallet outbox message", "tenant_id", message.TenantID, "event_id", message.EventID, "error", err)
			}
			if runContext.Err() != nil {
				return nil
			}
		}
	}
}

// Stop requests cancellation and waits for the polling/publish loop to exit.
// It is safe to call Stop when Run has not been started or has already ended.
func (r *Relay) Stop(ctx context.Context) error {
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

func (r *Relay) publishOne(ctx context.Context, message OutboxMessage) error {
	prefix := r.config.SubjectPrefix
	if prefix == "" {
		prefix = events.SubjectPrefix
	}
	subject, err := events.SubjectWithPrefix(prefix, message.TenantID, message.AggregateType, message.EventType)
	if err == nil {
		transport := events.Message{Subject: subject, NatsMsgID: message.EventID.String(), Payload: message.Payload}
		err = transport.Validate()
	}
	if err != nil {
		return r.recordFailure(ctx, message, fmt.Errorf("validate outbox event: %w", err))
	}
	if r.publisher == nil {
		return r.recordFailure(ctx, message, errors.New("wallet outbox publisher is nil"))
	}

	natsMessage := nats.NewMsg(subject)
	natsMessage.Header.Set(events.NatsMsgIDHeader, message.EventID.String())
	natsMessage.Data = message.Payload
	publishContext, cancel := context.WithTimeout(ctx, r.config.PublishTimeout)
	_, err = r.publisher.PublishMsg(natsMessage, nats.Context(publishContext), nats.MsgId(message.EventID.String()))
	cancel()
	if err != nil {
		return r.recordFailure(ctx, message, fmt.Errorf("publish event: %w", err))
	}
	if r.config.afterPublish != nil {
		if hookErr := r.config.afterPublish(message); hookErr != nil {
			return fmt.Errorf("%w: %v", errAfterPublish, hookErr)
		}
	}
	publishedAt := r.now().UTC()
	if err := r.store.MarkPublished(ctx, message, publishedAt); err != nil {
		return fmt.Errorf("mark outbox message published: %w", err)
	}
	return nil
}

func (r *Relay) recordFailure(ctx context.Context, message OutboxMessage, publishErr error) error {
	attempt := int(message.Attempts) + 1
	if attempt < 1 {
		attempt = 1
	}
	delay := retryDelay(r.config, attempt, message.EventID)
	nextAttemptAt := r.now().UTC().Add(delay)
	if err := r.store.MarkFailed(ctx, message, trimError(publishErr), nextAttemptAt); err != nil {
		return errors.Join(publishErr, fmt.Errorf("record outbox failure: %w", err))
	}
	return publishErr
}

func retryDelay(config Config, attempt int, eventID identifiers.UUID) time.Duration {
	delay := config.BaseBackoff
	for index := 1; index < attempt && delay < config.MaxBackoff; index++ {
		if delay > config.MaxBackoff/2 {
			delay = config.MaxBackoff
			break
		}
		delay *= 2
	}
	if delay > config.MaxBackoff {
		delay = config.MaxBackoff
	}
	jitter := config.Jitter(attempt, eventID, delay)
	if jitter < 0 {
		jitter = 0
	}
	if jitter > config.MaxBackoff-delay {
		jitter = config.MaxBackoff - delay
	}
	return delay + jitter
}

func deterministicJitter(attempt int, eventID identifiers.UUID, delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	var input [20]byte
	copy(input[:16], eventID[:])
	binary.BigEndian.PutUint32(input[16:], uint32(attempt))
	digest := sha256.Sum256(input[:])
	maximum := delay / 4
	if maximum <= 0 {
		return 0
	}
	return time.Duration(binary.BigEndian.Uint64(digest[:8]) % uint64(maximum+1))
}

func trimError(err error) string {
	value := "unknown publish failure"
	if err != nil {
		value = err.Error()
	}
	if len(value) > maxStoredErrorLength {
		return value[:maxStoredErrorLength]
	}
	return value
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type poolStore struct {
	pool *pgxpool.Pool
}

func (s *poolStore) Claim(ctx context.Context, limit int32, leaseUntil time.Time) ([]OutboxMessage, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("wallet outbox relay has no database pool")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	queries := walletdb.New(tx)
	rows, err := queries.ClaimOutboxMessages(ctx, walletdb.ClaimOutboxMessagesParams{Limit: limit, LockedUntil: &leaseUntil})
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	claimed := make([]OutboxMessage, 0, len(rows))
	for _, row := range rows {
		if row.LockedUntil == nil {
			return nil, errors.New("claimed wallet outbox message has no lease")
		}
		claimed = append(claimed, OutboxMessage{
			TenantID: row.TenantID, ID: row.ID, EventID: row.EventID, CreatedAt: row.CreatedAt,
			Attempts: row.Attempts, LeaseUntil: row.LockedUntil.UTC(), AggregateType: row.AggregateType,
			EventType: row.EventType, Payload: append([]byte(nil), row.Payload...),
		})
	}
	return claimed, nil
}

func (s *poolStore) MarkPublished(ctx context.Context, message OutboxMessage, publishedAt time.Time) error {
	if s == nil || s.pool == nil {
		return errors.New("wallet outbox relay has no database pool")
	}
	rows, err := walletdb.New(s.pool).MarkOutboxPublished(ctx, walletdb.MarkOutboxPublishedParams{
		TenantID: message.TenantID, ID: message.ID, PublishedAt: &publishedAt, LockedUntil: &message.LeaseUntil,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *poolStore) MarkFailed(ctx context.Context, message OutboxMessage, lastError string, nextAttemptAt time.Time) error {
	if s == nil || s.pool == nil {
		return errors.New("wallet outbox relay has no database pool")
	}
	rows, err := walletdb.New(s.pool).MarkOutboxFailed(ctx, walletdb.MarkOutboxFailedParams{
		TenantID: message.TenantID, ID: message.ID, LockedUntil: &message.LeaseUntil, LastError: &lastError,
		NextAttemptAt: nextAttemptAt,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}
