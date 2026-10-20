package projection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"digitalwallet/services/projector/internal/storage/generated"
	"digitalwallet/shared/events"
	"digitalwallet/shared/money"
)

type Handler struct{ Slot string }

func NewHandler(slot string) (*Handler, error) {
	if slot != "a" && slot != "b" {
		return nil, fmt.Errorf("invalid projection slot %q", slot)
	}
	return &Handler{Slot: slot}, nil
}

func (h *Handler) HandleEvent(ctx context.Context, tx *sql.Tx, envelope events.Envelope) error {
	if h == nil || (h.Slot != "a" && h.Slot != "b") {
		return errors.New("projection handler has invalid slot")
	}
	q := storage.New(tx)
	state := State{}
	current, err := q.GetWalletBalanceForSlot(ctx, storage.GetWalletBalanceForSlotParams{
		ProjectionSlot: h.Slot,
		TenantID:       envelope.TenantID,
		WalletID:       envelope.AggregateID,
		Currency:       "USD",
	})
	if err == nil {
		state = State{
			Exists:             true,
			WalletID:           current.WalletID,
			Currency:           money.Currency(current.Currency),
			PostedBalanceMinor: current.PostedBalanceMinor,
			HeldBalanceMinor:   current.HeldBalanceMinor,
			AvailableMinor:     current.AvailableBalanceMinor,
			LastVersion:        current.LastAppliedVersion,
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read projection balance: %w", err)
	}

	updated, entry, err := Apply(state, envelope)
	if err != nil {
		return err
	}
	if !updated.Exists {
		return nil
	}
	if entry != nil {
		nextEntryVersion, err := q.GetNextEntryVersion(ctx, storage.GetNextEntryVersionParams{
			ProjectionSlot: h.Slot,
			TenantID:       envelope.TenantID,
			WalletID:       envelope.AggregateID,
		})
		if err != nil {
			return fmt.Errorf("get next projection entry version: %w", err)
		}
		if err := q.InsertWalletEntry(ctx, storage.InsertWalletEntryParams{
			ProjectionSlot:       h.Slot,
			TenantID:             envelope.TenantID,
			WalletID:             envelope.AggregateID,
			EntryVersion:         nextEntryVersion,
			AggregateVersion:     entry.AggregateVersion,
			EventID:              entry.EventID,
			JournalTransactionID: entry.JournalTransactionID,
			EntryType:            entry.EntryType,
			Side:                 entry.Side,
			AmountMinor:          entry.AmountMinor,
			Currency:             string(entry.Currency),
			Reference:            entry.Reference,
			OccurredAt:           envelope.OccurredAt,
		}); err != nil {
			return fmt.Errorf("insert projection entry: %w", err)
		}
	}
	if err := q.UpsertWalletBalance(ctx, storage.UpsertWalletBalanceParams{
		ProjectionSlot:        h.Slot,
		TenantID:              envelope.TenantID,
		WalletID:              envelope.AggregateID,
		Currency:              string(updated.Currency),
		PostedBalanceMinor:    updated.PostedBalanceMinor,
		HeldBalanceMinor:      updated.HeldBalanceMinor,
		AvailableBalanceMinor: updated.AvailableMinor,
		LastAppliedVersion:    updated.LastVersion,
		SourceEventID:         envelope.EventID,
		UpdatedAt:             envelope.OccurredAt,
	}); err != nil {
		return fmt.Errorf("upsert projection balance: %w", err)
	}
	return nil
}
