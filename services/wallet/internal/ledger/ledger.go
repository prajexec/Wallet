// Package ledger contains the pure accounting rules used by wallet commands.
// It has no database or HTTP dependencies.
package ledger

import (
	"errors"
	"fmt"

	"digitalwallet/shared/identifiers"
	"digitalwallet/shared/money"
)

const (
	OperationDeposit    = "DEPOSIT"
	OperationWithdrawal = "WITHDRAWAL"
	Debit               = "DEBIT"
	Credit              = "CREDIT"
	EntryWallet         = "wallet"
	EntryClearing       = "clearing"
)

var (
	ErrInvalidEntry       = errors.New("invalid journal entry")
	ErrUnbalancedEntry    = errors.New("journal entry is unbalanced")
	ErrInvalidPosting     = errors.New("invalid journal posting")
	ErrInsufficientFunds  = errors.New("insufficient funds")
	ErrUnsupportedCommand = errors.New("unsupported ledger command")
)

type Posting struct {
	ID         identifiers.UUID
	AccountID  identifiers.UUID
	Side       string
	Amount     money.Money
	EntryType  string
	TransferID *identifiers.UUID
}

type JournalEntry struct {
	OperationType     string
	ExternalReference string
	Postings          []Posting
}

// Deposit creates a balanced entry that credits the user wallet and debits
// the tenant's deposit-clearing account.
func Deposit(amount money.Money, walletAccountID, clearingAccountID identifiers.UUID, externalReference string) (JournalEntry, error) {
	if err := amount.ValidatePositive(); err != nil {
		return JournalEntry{}, err
	}
	return JournalEntry{
		OperationType:     OperationDeposit,
		ExternalReference: externalReference,
		Postings: []Posting{
			{AccountID: walletAccountID, Side: Credit, Amount: amount, EntryType: EntryWallet},
			{AccountID: clearingAccountID, Side: Debit, Amount: amount, EntryType: EntryClearing},
		},
	}, nil
}

// Withdrawal creates a balanced entry that debits the user wallet and
// credits the tenant's withdrawal-clearing account. Availability is checked
// by the repository while holding the wallet account row lock.
func Withdrawal(amount money.Money, walletAccountID, clearingAccountID identifiers.UUID, externalReference string) (JournalEntry, error) {
	if err := amount.ValidatePositive(); err != nil {
		return JournalEntry{}, err
	}
	return JournalEntry{
		OperationType:     OperationWithdrawal,
		ExternalReference: externalReference,
		Postings: []Posting{
			{AccountID: walletAccountID, Side: Debit, Amount: amount, EntryType: EntryWallet},
			{AccountID: clearingAccountID, Side: Credit, Amount: amount, EntryType: EntryClearing},
		},
	}, nil
}

// Validate checks the double-entry and posting-level invariants.
func (entry JournalEntry) Validate() error {
	if entry.OperationType != OperationDeposit && entry.OperationType != OperationWithdrawal {
		return fmt.Errorf("%w: operation type %q", ErrUnsupportedCommand, entry.OperationType)
	}
	if len(entry.Postings) < 2 {
		return fmt.Errorf("%w: at least two postings are required", ErrInvalidEntry)
	}
	var debit, credit int64
	var currency money.Currency
	for index, posting := range entry.Postings {
		if posting.AccountID.IsZero() {
			return fmt.Errorf("%w: posting %d has no account", ErrInvalidPosting, index)
		}
		if err := posting.Amount.ValidatePositive(); err != nil {
			return fmt.Errorf("%w: posting %d: %w", ErrInvalidPosting, index, err)
		}
		if posting.Side != Debit && posting.Side != Credit {
			return fmt.Errorf("%w: posting %d has side %q", ErrInvalidPosting, index, posting.Side)
		}
		if index == 0 {
			currency = posting.Amount.Currency
		} else if posting.Amount.Currency != currency {
			return fmt.Errorf("%w: posting %d currency mismatch", ErrInvalidPosting, index)
		}
		var err error
		if posting.Side == Debit {
			debit, err = checkedSum(debit, posting.Amount.AmountMinor)
		} else {
			credit, err = checkedSum(credit, posting.Amount.AmountMinor)
		}
		if err != nil {
			return fmt.Errorf("%w: posting totals overflow", ErrInvalidEntry)
		}
	}
	if debit != credit || debit <= 0 {
		return fmt.Errorf("%w: debit=%d credit=%d", ErrUnbalancedEntry, debit, credit)
	}
	return nil
}

// Balance computes a journal-derived balance for one account and currency.
func Balance(postings []Posting, accountID identifiers.UUID, currency money.Currency) (money.Money, error) {
	if err := currency.Validate(); err != nil {
		return money.Money{}, err
	}
	balance := money.FromMinor(0)
	for index, posting := range postings {
		if posting.AccountID != accountID || posting.Amount.Currency != currency {
			continue
		}
		amount := posting.Amount
		if posting.Side == Debit {
			var err error
			amount, err = amount.Negate()
			if err != nil {
				return money.Money{}, fmt.Errorf("posting %d: %w", index, err)
			}
		} else if posting.Side != Credit {
			return money.Money{}, fmt.Errorf("%w: posting %d side %q", ErrInvalidPosting, index, posting.Side)
		}
		var err error
		balance, err = balance.Add(amount)
		if err != nil {
			return money.Money{}, fmt.Errorf("posting %d: %w", index, err)
		}
	}
	return balance, nil
}

// AvailableBalance subtracts active holds from posted balance. Phase 3 callers
// pass zero holds; reserve/capture/release will supply the active term later.
func AvailableBalance(posted, activeHolds money.Money) (money.Money, error) {
	if err := posted.Validate(); err != nil {
		return money.Money{}, err
	}
	if err := activeHolds.Validate(); err != nil {
		return money.Money{}, err
	}
	if posted.Currency != activeHolds.Currency {
		return money.Money{}, money.ErrCurrencyMismatch
	}
	return posted.Sub(activeHolds)
}

func checkedSum(left, right int64) (int64, error) {
	result, err := money.MinorUnit(left).Add(money.MinorUnit(right))
	return int64(result), err
}
