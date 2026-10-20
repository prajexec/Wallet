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
	TenantID         identifiers.UUID `json:"tenant_id"`
	WalletID         identifiers.UUID `json:"wallet_id"`
	AccountID        identifiers.UUID `json:"account_id"`
	Currency         money.Currency   `json:"currency"`
	Status           string           `json:"status"`
	CreatedAt        time.Time        `json:"created_at"`
	AggregateVersion int64            `json:"aggregate_version"`
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
	AsOfVersion   int64            `json:"as_of_version"`
}

type JournalCommandResponse struct {
	OperationID          identifiers.UUID `json:"operation_id"`
	JournalTransactionID identifiers.UUID `json:"journal_transaction_id"`
	EventID              identifiers.UUID `json:"event_id"`
	AmountMinor          int64            `json:"amount_minor"`
	Currency             money.Currency   `json:"currency"`
	AggregateVersion     int64            `json:"aggregate_version"`
}

type WalletVerificationRow struct {
	TenantID           identifiers.UUID `json:"tenant_id"`
	WalletID           identifiers.UUID `json:"wallet_id"`
	Currency           string           `json:"currency"`
	PostedBalanceMinor int64            `json:"posted_balance_minor"`
	AsOfVersion        int64            `json:"as_of_version"`
}

type WalletVerificationPage struct {
	Wallets           []WalletVerificationRow `json:"wallets"`
	NextAfterTenantID *identifiers.UUID       `json:"next_after_tenant_id,omitempty"`
	NextAfterWalletID *identifiers.UUID       `json:"next_after_wallet_id,omitempty"`
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

func (r *Repository) appendEvent(ctx context.Context, q *walletdb.Queries, tenantID, aggregateID identifiers.UUID, eventType string, data any, occurredAt time.Time) (identifiers.UUID, int64, error) {
	eventID, err := r.ids.Generate()
	if err != nil {
		return identifiers.UUID{}, 0, err
	}
	correlationID, err := r.ids.Generate()
	if err != nil {
		return identifiers.UUID{}, 0, err
	}
	nextVersion, err := q.GetNextAggregateVersion(ctx, walletdb.GetNextAggregateVersionParams{TenantID: tenantID, AggregateID: aggregateID})
	if err != nil {
		return identifiers.UUID{}, 0, err
	}
	envelope, err := events.NewEnvelope(eventID, eventType, 1, occurredAt.UTC(), tenantID, "wallet", aggregateID, nextVersion, r.producer, correlationID, nil, data, map[string]any{})
	if err != nil {
		return identifiers.UUID{}, 0, err
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return identifiers.UUID{}, 0, err
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
		return identifiers.UUID{}, 0, fmt.Errorf("insert wallet event: %w", err)
	}
	outboxID, err := r.ids.Generate()
	if err != nil {
		return identifiers.UUID{}, 0, err
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
		return identifiers.UUID{}, 0, fmt.Errorf("insert wallet outbox message: %w", err)
	}
	return eventID, nextVersion, nil
}

// VerifyWallets scans wallets across all tenants using keyset pagination in a REPEATABLE READ snapshot.
func (r *Repository) VerifyWallets(ctx context.Context, afterTenantID, afterWalletID identifiers.UUID, limit int32) (WalletVerificationPage, error) {
	if (afterTenantID.IsZero()) != (afterWalletID.IsZero()) {
		return WalletVerificationPage{}, errors.New("after_tenant_id and after_wallet_id must be provided together")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return WalletVerificationPage{}, fmt.Errorf("begin repeatable read verification tx: %w", err)
	}
	defer tx.Rollback(ctx)

	q := walletdb.New(tx)
	rows, err := q.ListWalletVerificationRows(ctx, walletdb.ListWalletVerificationRowsParams{
		Column1: afterTenantID,
		Column2: afterWalletID,
		Limit:   limit,
	})
	if err != nil {
		return WalletVerificationPage{}, fmt.Errorf("list wallet verification rows: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return WalletVerificationPage{}, fmt.Errorf("commit verification tx: %w", err)
	}

	result := WalletVerificationPage{
		Wallets: make([]WalletVerificationRow, len(rows)),
	}
	for i, row := range rows {
		result.Wallets[i] = WalletVerificationRow{
			TenantID:           row.TenantID,
			WalletID:           row.WalletID,
			Currency:           row.Currency,
			PostedBalanceMinor: row.PostedBalanceMinor,
			AsOfVersion:        row.AsOfVersion,
		}
	}
	if len(rows) == int(limit) {
		last := rows[len(rows)-1]
		nextT := last.TenantID
		nextW := last.WalletID
		result.NextAfterTenantID = &nextT
		result.NextAfterWalletID = &nextW
	}
	return result, nil
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

// StatsResponse represents outbox and event metrics reported by /internal/v1/stats.
type StatsResponse struct {
	Service                     string          `json:"service"`
	UnpublishedOutboxCount      int64           `json:"unpublished_outbox_count"`
	OldestUnpublishedAgeSeconds float64         `json:"oldest_unpublished_age_seconds"`
	AttemptsHistogram           map[int32]int64 `json:"attempts_histogram"`
	BufferedEventCount          int64           `json:"buffered_event_count"`
	OpenGapCount                int64           `json:"open_gap_count"`
	DeadLetterCount             int64           `json:"dead_letter_count"`
}

// GetStats returns outbox observability metrics without a metrics stack.
func (r *Repository) GetStats(ctx context.Context) (StatsResponse, error) {
	stats := StatsResponse{
		Service:           "wallet",
		AttemptsHistogram: make(map[int32]int64),
	}
	const countQuery = `
SELECT 
    count(*),
    COALESCE(EXTRACT(EPOCH FROM (now() - MIN(created_at))), 0)
FROM outbox_messages
WHERE published_at IS NULL;
`
	if err := r.pool.QueryRow(ctx, countQuery).Scan(&stats.UnpublishedOutboxCount, &stats.OldestUnpublishedAgeSeconds); err != nil {
		return stats, fmt.Errorf("query outbox count and age: %w", err)
	}

	const histQuery = `
SELECT attempts, count(*)
FROM outbox_messages
WHERE published_at IS NULL
GROUP BY attempts
ORDER BY attempts;
`
	rows, err := r.pool.Query(ctx, histQuery)
	if err != nil {
		return stats, fmt.Errorf("query outbox attempts histogram: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var attempt int32
		var count int64
		if err := rows.Scan(&attempt, &count); err != nil {
			return stats, fmt.Errorf("scan outbox histogram row: %w", err)
		}
		stats.AttemptsHistogram[attempt] = count
	}
	return stats, rows.Err()
}
