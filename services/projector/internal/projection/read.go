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
	WalletID                   identifiers.UUID
	Currency                   string
	PostedBalanceMinor         int64
	HeldBalanceMinor           int64
	AvailableBalanceMinor      int64
	ReadModelConsistentThrough int64
	ProjectionSlot             string
	UpdatedAt                  time.Time
}

type EntryRead struct {
	EntryVersion         int64
	AggregateVersion     int64
	EventID              identifiers.UUID
	JournalTransactionID identifiers.UUID
	EntryType            string
	Side                 string
	AmountMinor          int64
	Currency             string
	Reference            string
	OccurredAt           time.Time
}

type EntryPage struct {
	Entries                    []EntryRead
	NextCursor                 string
	ProjectionSlot             string
	ReadModelConsistentThrough int64
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
