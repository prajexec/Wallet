package projection

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
	"digitalwallet/shared/money"
)

var (
	ErrUnsupportedSchemaVersion = errors.New("unsupported projection event schema version")
	ErrVersionGap               = errors.New("projection event version is not contiguous")
	ErrInvalidProjectionEvent   = errors.New("invalid projection event")
)

type State struct {
	Exists             bool
	WalletID           identifiers.UUID
	Currency           money.Currency
	PostedBalanceMinor int64
	HeldBalanceMinor   int64
	AvailableMinor     int64
	LastVersion        int64
}

type Entry struct {
	AggregateVersion     int64
	EventID              identifiers.UUID
	JournalTransactionID identifiers.UUID
	EntryType            string
	Side                 string
	AmountMinor          int64
	Currency             money.Currency
	Reference            string
}

type walletCreatedPayload struct {
	WalletID  identifiers.UUID `json:"wallet_id"`
	AccountID identifiers.UUID `json:"account_id"`
	Currency  money.Currency   `json:"currency"`
}

type journalPostedPayload struct {
	WalletID             identifiers.UUID `json:"wallet_id"`
	JournalTransactionID identifiers.UUID `json:"journal_transaction_id"`
	AmountMinor          int64            `json:"amount_minor"`
	Currency             money.Currency   `json:"currency"`
	Side                 string           `json:"side"`
	EntryType            string           `json:"entry_type"`
	Reference            string           `json:"reference"`
}

func Apply(state State, envelope events.Envelope) (State, *Entry, error) {
	if envelope.SchemaVersion != 1 {
		return state, nil, fmt.Errorf("%w: got %d", ErrUnsupportedSchemaVersion, envelope.SchemaVersion)
	}
	if envelope.AggregateVersion != state.LastVersion+1 {
		return state, nil, fmt.Errorf("%w: got %d after %d", ErrVersionGap, envelope.AggregateVersion, state.LastVersion)
	}

	state.LastVersion = envelope.AggregateVersion
	switch envelope.EventType {
	case "wallet.created":
		var payload walletCreatedPayload
		if err := decode(envelope, &payload); err != nil {
			return state, nil, err
		}
		if payload.WalletID != envelope.AggregateID || payload.Currency.Validate() != nil {
			return state, nil, fmt.Errorf("%w: invalid wallet.created payload", ErrInvalidProjectionEvent)
		}
		state.Exists = true
		state.WalletID = payload.WalletID
		state.Currency = payload.Currency
		return state, nil, nil

	case "wallet.deposit_posted", "wallet.withdrawal_posted":
		if !state.Exists {
			return state, nil, fmt.Errorf("%w: journal event before wallet.created", ErrInvalidProjectionEvent)
		}
		var payload journalPostedPayload
		if err := decode(envelope, &payload); err != nil {
			return state, nil, err
		}
		if payload.WalletID != envelope.AggregateID || payload.Currency != state.Currency || payload.AmountMinor <= 0 || payload.Side == "" || payload.EntryType == "" {
			return state, nil, fmt.Errorf("%w: invalid journal payload", ErrInvalidProjectionEvent)
		}
		amount, err := money.NewPositive(payload.AmountMinor, payload.Currency)
		if err != nil {
			return state, nil, err
		}
		current := money.FromMinor(state.PostedBalanceMinor)
		if payload.Side == "CREDIT" {
			current, err = current.Add(amount)
		} else if payload.Side == "DEBIT" {
			current, err = current.Sub(amount)
		} else {
			return state, nil, fmt.Errorf("%w: invalid side %q", ErrInvalidProjectionEvent, payload.Side)
		}
		if err != nil {
			return state, nil, err
		}
		if current.AmountMinor < 0 {
			return state, nil, fmt.Errorf("%w: available balance would be negative", ErrInvalidProjectionEvent)
		}
		state.PostedBalanceMinor = current.AmountMinor
		state.AvailableMinor = current.AmountMinor
		return state, &Entry{
			AggregateVersion:     envelope.AggregateVersion,
			EventID:              envelope.EventID,
			JournalTransactionID: payload.JournalTransactionID,
			EntryType:            payload.EntryType,
			Side:                 payload.Side,
			AmountMinor:          payload.AmountMinor,
			Currency:             payload.Currency,
			Reference:            payload.Reference,
		}, nil
	default:
		// Unknown event types are counted by the aggregate cursor but do not
		// invent a read-model mutation.
		return state, nil, nil
	}
}

func decode(envelope events.Envelope, target any) error {
	if len(envelope.Data) == 0 || strings.TrimSpace(string(envelope.Data)) == "null" {
		return fmt.Errorf("%w: missing data", ErrInvalidProjectionEvent)
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("%w: decode data: %v", ErrInvalidProjectionEvent, err)
	}
	return nil
}
