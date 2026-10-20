package integration

import (
	"context"
	"testing"
	"time"

	"digitalwallet/services/projector/internal/projection"
	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
)

func TestProjectionHandlerBuildsBalanceAndEntries(t *testing.T) {
	if testReadDB == nil {
		t.Skip("read test database not configured")
	}
	ids := identifiers.NewGenerator()
	tenantID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	walletID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	correlationID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	var slot string
	if err := testReadDB.QueryRowContext(context.Background(), `SELECT active_slot FROM projection_versions WHERE projection_name = 'wallet'`).Scan(&slot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := testReadDB.ExecContext(ctx, `DELETE FROM wallet_entry_view WHERE projection_slot = $1 AND tenant_id = $2`, slot, tenantID); err != nil {
			t.Errorf("clean projection entries: %v", err)
		}
		if _, err := testReadDB.ExecContext(ctx, `DELETE FROM wallet_balance_view WHERE projection_slot = $1 AND tenant_id = $2`, slot, tenantID); err != nil {
			t.Errorf("clean projection balances: %v", err)
		}
	})
	handler, err := projection.NewHandler(slot)
	if err != nil {
		t.Fatal(err)
	}

	createdEventID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	created, err := events.NewEnvelope(createdEventID, "wallet.created", 1, time.Unix(1, 0).UTC(), tenantID, "wallet", walletID, 1, "wallet-source", correlationID, nil, map[string]any{"wallet_id": walletID, "account_id": walletID, "currency": "USD"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(envelope events.Envelope) {
		t.Helper()
		tx, err := testReadDB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := handler.HandleEvent(context.Background(), tx, envelope); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	apply(created)

	depositID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	deposit, err := events.NewEnvelope(depositID, "wallet.deposit_posted", 1, time.Unix(2, 0).UTC(), tenantID, "wallet", walletID, 2, "wallet-source", correlationID, nil, map[string]any{"wallet_id": walletID, "journal_transaction_id": depositID, "amount_minor": 30, "currency": "USD", "side": "CREDIT", "entry_type": "DEPOSIT", "reference": "seed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply(deposit)
	withdrawalID, err := ids.Generate()
	if err != nil {
		t.Fatal(err)
	}
	withdrawal, err := events.NewEnvelope(withdrawalID, "wallet.withdrawal_posted", 1, time.Unix(3, 0).UTC(), tenantID, "wallet", walletID, 3, "wallet-source", correlationID, nil, map[string]any{"wallet_id": walletID, "journal_transaction_id": withdrawalID, "amount_minor": 5, "currency": "USD", "side": "DEBIT", "entry_type": "WITHDRAWAL", "reference": "spend"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply(withdrawal)

	var posted, available, version int64
	if err := testReadDB.QueryRowContext(context.Background(), `SELECT posted_balance_minor, available_balance_minor, last_applied_version FROM wallet_balance_view WHERE projection_slot=$1 AND tenant_id=$2 AND wallet_id=$3 AND currency='USD'`, slot, tenantID, walletID).Scan(&posted, &available, &version); err != nil {
		t.Fatal(err)
	}
	if posted != 25 || available != 25 || version != 3 {
		t.Fatalf("balance posted=%d available=%d version=%d", posted, available, version)
	}
	rows, err := testReadDB.QueryContext(context.Background(), `SELECT entry_version, aggregate_version, amount_minor, side, entry_type, reference FROM wallet_entry_view WHERE projection_slot=$1 AND tenant_id=$2 AND wallet_id=$3 ORDER BY entry_version`, slot, tenantID, walletID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantEntries := []struct {
		entryVersion, aggregateVersion, amount int64
		side, entryType, reference             string
	}{
		{1, 2, 30, "CREDIT", "DEPOSIT", "seed"},
		{2, 3, 5, "DEBIT", "WITHDRAWAL", "spend"},
	}
	for i, want := range wantEntries {
		if !rows.Next() {
			t.Fatalf("entry row %d missing", i+1)
		}
		var got struct {
			entryVersion, aggregateVersion, amount int64
			side, entryType, reference             string
		}
		if err := rows.Scan(&got.entryVersion, &got.aggregateVersion, &got.amount, &got.side, &got.entryType, &got.reference); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("entry %d = %+v, want %+v", i+1, got, want)
		}
	}
	if rows.Next() {
		t.Fatal("projection contains more entries than expected")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	store := projection.NewStore(testReadDB)
	read, err := store.GetBalance(context.Background(), tenantID, walletID)
	if err != nil || read.PostedBalanceMinor != 25 || read.ReadModelConsistentThrough != 3 {
		t.Fatalf("read=%+v err=%v", read, err)
	}
}
