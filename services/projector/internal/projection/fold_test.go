package projection

import (
	"errors"
	"testing"
	"time"

	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
	"digitalwallet/shared/money"
)

func TestApplyWalletLifecycle(t *testing.T) {
	ids := identifiers.NewDeterministicGenerator()
	tenant, _ := ids.Generate()
	wallet, _ := ids.Generate()
	createdID, _ := ids.Generate()
	correlation, _ := ids.Generate()
	created, err := events.NewEnvelope(createdID, "wallet.created", 1, time.Unix(0, 0).UTC(), tenant, "wallet", wallet, 1, "wallet-source", correlation, nil, map[string]any{"wallet_id": wallet, "account_id": wallet, "currency": "USD"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, entry, err := Apply(State{}, created)
	if err != nil || entry != nil || !state.Exists || state.LastVersion != 1 {
		t.Fatalf("created state=%+v entry=%+v err=%v", state, entry, err)
	}

	depositID, _ := ids.Generate()
	deposit, err := events.NewEnvelope(depositID, "wallet.deposit_posted", 1, time.Unix(1, 0).UTC(), tenant, "wallet", wallet, 2, "wallet-source", correlation, nil, map[string]any{
		"wallet_id": wallet, "journal_transaction_id": createdID, "amount_minor": 25, "currency": "USD", "side": "CREDIT", "entry_type": "DEPOSIT", "reference": "seed",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, entry, err = Apply(state, deposit)
	if err != nil || entry == nil || state.PostedBalanceMinor != 25 || state.AvailableMinor != 25 || state.LastVersion != 2 {
		t.Fatalf("deposit state=%+v entry=%+v err=%v", state, entry, err)
	}
	if entry.AmountMinor != 25 || entry.Side != "CREDIT" || entry.Currency != money.USD {
		t.Fatalf("entry=%+v", entry)
	}
}

func TestApplyRejectsUnsupportedSchemaAndNegativeBalance(t *testing.T) {
	ids := identifiers.NewDeterministicGenerator()
	tenant, _ := ids.Generate()
	wallet, _ := ids.Generate()
	eventID, _ := ids.Generate()
	correlation, _ := ids.Generate()
	created, _ := events.NewEnvelope(eventID, "wallet.created", 1, time.Unix(0, 0).UTC(), tenant, "wallet", wallet, 1, "wallet-source", correlation, nil, map[string]any{"wallet_id": wallet, "account_id": wallet, "currency": "USD"}, nil)
	state, _, _ := Apply(State{}, created)
	withdrawalID, _ := ids.Generate()
	withdrawal, _ := events.NewEnvelope(withdrawalID, "wallet.withdrawal_posted", 1, time.Unix(1, 0).UTC(), tenant, "wallet", wallet, 2, "wallet-source", correlation, nil, map[string]any{"wallet_id": wallet, "journal_transaction_id": eventID, "amount_minor": 1, "currency": "USD", "side": "DEBIT", "entry_type": "WITHDRAWAL"}, nil)
	if _, _, err := Apply(state, withdrawal); !errors.Is(err, ErrInvalidProjectionEvent) {
		t.Fatalf("negative withdrawal error=%v", err)
	}
	withdrawal.SchemaVersion = 2
	if _, _, err := Apply(state, withdrawal); !errors.Is(err, ErrUnsupportedSchemaVersion) {
		t.Fatalf("unsupported schema error=%v", err)
	}
}

func TestApplyUnknownEventAdvancesVersionWithoutMutation(t *testing.T) {
	ids := identifiers.NewDeterministicGenerator()
	tenant, _ := ids.Generate()
	wallet, _ := ids.Generate()
	eventID, _ := ids.Generate()
	correlation, _ := ids.Generate()
	event, err := events.NewEnvelope(eventID, "wallet.future_event", 1, time.Unix(0, 0).UTC(), tenant, "wallet", wallet, 1, "wallet-source", correlation, nil, map[string]any{"ignored": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, entry, err := Apply(State{}, event)
	if err != nil || entry != nil || state.LastVersion != 1 || state.Exists {
		t.Fatalf("state=%+v entry=%+v err=%v", state, entry, err)
	}
}
