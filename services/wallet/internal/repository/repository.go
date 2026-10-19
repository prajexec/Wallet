package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"digitalwallet/services/wallet/internal/ledger"
	walletdb "digitalwallet/services/wallet/internal/storage/generated"
	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
	"digitalwallet/shared/money"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	statusCompleted = "COMPLETED"
	currencyUSD     = "USD"
	maxRetries      = 3
)

var (
	ErrNotFound            = errors.New("wallet not found")
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different payload")
	ErrIdempotencyProgress = errors.New("idempotency request is already in progress")
	ErrWalletClosed        = errors.New("wallet is closed")
	ErrInsufficientFunds   = ledger.ErrInsufficientFunds
)

type Repository struct {
	pool     *pgxpool.Pool
	ids      identifiers.Generator
	now      func() time.Time
	producer string
}

type CommandResult struct {
	StatusCode int
	Body       []byte
	Replayed   bool
}

type Wallet struct {
	TenantID  identifiers.UUID `json:"tenant_id"`
	WalletID  identifiers.UUID `json:"wallet_id"`
	AccountID identifiers.UUID `json:"account_id"`
	Currency  money.Currency   `json:"currency"`
	Status    string           `json:"status"`
	CreatedAt time.Time        `json:"created_at"`
}

type Balance struct {
	Currency       money.Currency `json:"currency"`
	CurrentMinor   int64          `json:"current_minor"`
	HeldMinor      int64          `json:"held_minor"`
	AvailableMinor int64          `json:"available_minor"`
}

type BalanceResponse struct {
	WalletID      identifiers.UUID `json:"wallet_id"`
	Balance       Balance          `json:"balance"`
	Authoritative bool             `json:"authoritative"`
}

type JournalCommandResponse struct {
	OperationID          identifiers.UUID `json:"operation_id"`
	JournalTransactionID identifiers.UUID `json:"journal_transaction_id"`
	EventID              identifiers.UUID `json:"event_id"`
	AmountMinor          int64            `json:"amount_minor"`
	Currency             money.Currency   `json:"currency"`
}

type EntryPosting struct {
	ID          identifiers.UUID  `json:"id"`
	AccountID   identifiers.UUID  `json:"account_id"`
	Side        string            `json:"side"`
	AmountMinor int64             `json:"amount_minor"`
	Currency    money.Currency    `json:"currency"`
	EntryType   string            `json:"entry_type"`
	TransferID  *identifiers.UUID `json:"transfer_id,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

type JournalEntry struct {
	JournalTransactionID identifiers.UUID `json:"journal_transaction_id"`
	OperationType        string           `json:"operation_type"`
	ExternalReference    *string          `json:"external_reference,omitempty"`
	CreatedAt            time.Time        `json:"created_at"`
	Postings             []EntryPosting   `json:"postings"`
}

type EntryPage struct {
	Entries    []JournalEntry `json:"entries"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func New(pool *pgxpool.Pool, ids identifiers.Generator, producer string) *Repository {
	if ids == nil {
		ids = identifiers.NewGenerator()
	}
	if producer == "" {
		producer = "wallet"
	}
	return &Repository{pool: pool, ids: ids, now: func() time.Time { return time.Now().UTC() }, producer: producer}
}

// NewWithClock is intended for deterministic repository tests.
func NewWithClock(pool *pgxpool.Pool, ids identifiers.Generator, producer string, now func() time.Time) *Repository {
	repository := New(pool, ids, producer)
	if now != nil {
		repository.now = now
	}
	return repository
}

func HashRequest(payload any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (r *Repository) withTransaction(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	if r == nil || r.pool == nil {
		return errors.New("wallet repository has no database pool")
	}
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return err
		}
		err = fn(ctx, tx)
		if err != nil {
			_ = tx.Rollback(ctx)
			if !isRetryable(err) {
				return err
			}
			lastErr = err
			if err := retryDelay(ctx, attempt); err != nil {
				return err
			}
			continue
		}
		err = tx.Commit(ctx)
		if err == nil {
			return nil
		}
		if !isRetryable(err) {
			return err
		}
		lastErr = err
		if delayErr := retryDelay(ctx, attempt); delayErr != nil {
			return delayErr
		}
	}
	return fmt.Errorf("wallet transaction retries exhausted: %w", lastErr)
}

func retryDelay(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt+1) * 5 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

func claimIdempotency(ctx context.Context, q *walletdb.Queries, tenantID identifiers.UUID, key, requestHash string) (*CommandResult, error) {
	if err := q.AcquireIdempotencyLock(ctx, walletdb.AcquireIdempotencyLockParams{TenantID: tenantID, Key: key}); err != nil {
		return nil, err
	}
	record, err := q.GetIdempotencyRecord(ctx, walletdb.GetIdempotencyRecordParams{TenantID: tenantID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if record.RequestHash != requestHash {
		return nil, ErrIdempotencyConflict
	}
	if record.Status != statusCompleted {
		return nil, ErrIdempotencyProgress
	}
	canonical, err := canonicalJSON(record.ResponseBody)
	if err != nil {
		return nil, err
	}
	return &CommandResult{StatusCode: int(record.ResponseStatus), Body: canonical, Replayed: true}, nil
}

func completeIdempotency(ctx context.Context, q *walletdb.Queries, tenantID identifiers.UUID, key, requestHash string, statusCode int, body []byte, createdAt time.Time) error {
	canonical, err := canonicalJSON(body)
	if err != nil {
		return err
	}
	rows, err := q.InsertIdempotencyRecord(ctx, walletdb.InsertIdempotencyRecordParams{
		TenantID:       tenantID,
		Key:            key,
		RequestHash:    requestHash,
		Status:         statusCompleted,
		ResponseStatus: int32(statusCode),
		ResponseBody:   canonical,
		CreatedAt:      createdAt,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("idempotency record was not inserted")
	}
	return nil
}

func canonicalJSON(body []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (r *Repository) appendEvent(ctx context.Context, q *walletdb.Queries, tenantID, aggregateID identifiers.UUID, eventType string, data any, occurredAt time.Time) (identifiers.UUID, error) {
	eventID, err := r.ids.Generate()
	if err != nil {
		return identifiers.UUID{}, err
	}
	correlationID, err := r.ids.Generate()
	if err != nil {
		return identifiers.UUID{}, err
	}
	nextVersion, err := q.GetNextAggregateVersion(ctx, walletdb.GetNextAggregateVersionParams{TenantID: tenantID, AggregateID: aggregateID})
	if err != nil {
		return identifiers.UUID{}, err
	}
	envelope, err := events.NewEnvelope(eventID, eventType, 1, occurredAt.UTC(), tenantID, "wallet", aggregateID, nextVersion, r.producer, correlationID, nil, data, map[string]any{})
	if err != nil {
		return identifiers.UUID{}, err
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return identifiers.UUID{}, err
	}
	if _, err := q.InsertEvent(ctx, walletdb.InsertEventParams{
		TenantID:         tenantID,
		EventID:          eventID,
		AggregateType:    envelope.AggregateType,
		AggregateID:      aggregateID,
		AggregateVersion: nextVersion,
		EventType:        eventType,
		SchemaVersion:    int32(envelope.SchemaVersion),
		CorrelationID:    correlationID,
		CausationID:      nil,
		OccurredAt:       occurredAt.UTC(),
		Payload:          payload,
	}); err != nil {
		return identifiers.UUID{}, fmt.Errorf("insert wallet event: %w", err)
	}
	outboxID, err := r.ids.Generate()
	if err != nil {
		return identifiers.UUID{}, err
	}
	if _, err := q.InsertOutboxMessage(ctx, walletdb.InsertOutboxMessageParams{
		TenantID:      tenantID,
		ID:            outboxID,
		EventID:       eventID,
		CreatedAt:     occurredAt.UTC(),
		PublishedAt:   nil,
		Attempts:      0,
		NextAttemptAt: occurredAt.UTC(),
	}); err != nil {
		return identifiers.UUID{}, fmt.Errorf("insert wallet outbox message: %w", err)
	}
	return eventID, nil
}

func ensureSystemAccount(ctx context.Context, q *walletdb.Queries, ids identifiers.Generator, tenantID identifiers.UUID, name string, now time.Time) (walletdb.Account, error) {
	account, err := q.GetAccountByName(ctx, walletdb.GetAccountByNameParams{TenantID: tenantID, Name: name, Currency: currencyUSD})
	if err == nil {
		return account, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return walletdb.Account{}, err
	}
	accountID, err := ids.Generate()
	if err != nil {
		return walletdb.Account{}, err
	}
	if err := q.InsertAccount(ctx, walletdb.InsertAccountParams{TenantID: tenantID, ID: accountID, Kind: "system_clearing", Name: name, Currency: currencyUSD, CreatedAt: now}); err != nil {
		return walletdb.Account{}, err
	}
	return q.GetAccountByName(ctx, walletdb.GetAccountByNameParams{TenantID: tenantID, Name: name, Currency: currencyUSD})
}
