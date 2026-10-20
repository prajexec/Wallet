package projection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"digitalwallet/services/projector/internal/storage/generated"
	"digitalwallet/shared/identifiers"
)

var ErrNotFound = errors.New("projected wallet not found")

type BalanceRead struct {
	WalletID                   identifiers.UUID `json:"wallet_id"`
	Currency                   string           `json:"currency"`
	PostedBalanceMinor         int64            `json:"posted_balance_minor"`
	HeldBalanceMinor           int64            `json:"held_balance_minor"`
	AvailableBalanceMinor      int64            `json:"available_balance_minor"`
	ReadModelConsistentThrough int64            `json:"read_model_consistent_through"`
	ProjectionSlot             string           `json:"projection_slot"`
	UpdatedAt                  time.Time        `json:"updated_at"`
}

type EntryRead struct {
	EntryVersion         int64            `json:"entry_version"`
	AggregateVersion     int64            `json:"aggregate_version"`
	EventID              identifiers.UUID `json:"event_id"`
	JournalTransactionID identifiers.UUID `json:"journal_transaction_id"`
	EntryType            string           `json:"entry_type"`
	Side                 string           `json:"side"`
	AmountMinor          int64            `json:"amount_minor"`
	Currency             string           `json:"currency"`
	Reference            string           `json:"reference"`
	OccurredAt           time.Time        `json:"occurred_at"`
}

type EntryPage struct {
	Entries                    []EntryRead `json:"entries"`
	NextCursor                 string      `json:"next_cursor,omitempty"`
	ProjectionSlot             string      `json:"projection_slot"`
	ReadModelConsistentThrough int64       `json:"read_model_consistent_through"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) GetBalance(ctx context.Context, tenantID, walletID identifiers.UUID) (BalanceRead, error) {
	if s == nil || s.db == nil {
		return BalanceRead{}, errors.New("projection read store is not configured")
	}
	row, err := storage.New(s.db).GetActiveWalletBalance(ctx, storage.GetActiveWalletBalanceParams{TenantID: tenantID, WalletID: walletID, Currency: "USD"})
	if errors.Is(err, sql.ErrNoRows) {
		return BalanceRead{}, ErrNotFound
	}
	if err != nil {
		return BalanceRead{}, fmt.Errorf("read projected balance: %w", err)
	}
	return BalanceRead{
		WalletID:                   row.WalletID,
		Currency:                   row.Currency,
		PostedBalanceMinor:         row.PostedBalanceMinor,
		HeldBalanceMinor:           row.HeldBalanceMinor,
		AvailableBalanceMinor:      row.AvailableBalanceMinor,
		ReadModelConsistentThrough: row.LastAppliedVersion,
		ProjectionSlot:             row.ProjectionSlot,
		UpdatedAt:                  row.UpdatedAt,
	}, nil
}

func (s *Store) ListEntries(ctx context.Context, tenantID, walletID identifiers.UUID, afterVersion int64, limit int32) (EntryPage, error) {
	balance, err := s.GetBalance(ctx, tenantID, walletID)
	if err != nil {
		return EntryPage{}, err
	}
	rows, err := storage.New(s.db).ListActiveWalletEntries(ctx, storage.ListActiveWalletEntriesParams{TenantID: tenantID, WalletID: walletID, EntryVersion: afterVersion, Limit: limit})
	if err != nil {
		return EntryPage{}, fmt.Errorf("list projected entries: %w", err)
	}
	page := EntryPage{ProjectionSlot: balance.ProjectionSlot, ReadModelConsistentThrough: balance.ReadModelConsistentThrough}
	page.Entries = make([]EntryRead, 0, len(rows))
	for _, row := range rows {
		page.Entries = append(page.Entries, EntryRead{
			EntryVersion: row.EntryVersion, AggregateVersion: row.AggregateVersion, EventID: row.EventID,
			JournalTransactionID: row.JournalTransactionID, EntryType: row.EntryType, Side: row.Side,
			AmountMinor: row.AmountMinor, Currency: row.Currency, Reference: row.Reference, OccurredAt: row.OccurredAt,
		})
	}
	if len(rows) == int(limit) && len(rows) > 0 {
		page.NextCursor = strconv.FormatInt(rows[len(rows)-1].EntryVersion, 10)
	}
	return page, nil
}
