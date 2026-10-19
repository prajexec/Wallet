// Package money provides checked integer money operations for the USD-only v1
// contract. It deliberately has no floating-point representation.
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

var (
	// ErrInvalidCurrency is returned for a currency other than USD.
	ErrInvalidCurrency = errors.New("invalid currency")
	// ErrCurrencyMismatch is returned when two monetary values use different currencies.
	ErrCurrencyMismatch = errors.New("currency mismatch")
	// ErrOverflow is returned when checked arithmetic cannot fit in int64.
	ErrOverflow = errors.New("money arithmetic overflow")
	// ErrNonPositive is returned for an amount that must be greater than zero.
	ErrNonPositive = errors.New("amount must be positive")
	// ErrInvalidMoney is returned when a Money value has an invalid currency.
	ErrInvalidMoney = errors.New("invalid money")
)

// Currency is the ISO-style currency code carried by a Money value.
type Currency string

const (
	// USD is the only supported v1 currency.
	USD Currency = "USD"
)

// MinorUnit is a signed integer number of currency minor units. A signed
// value is useful for journal-derived balances; command amounts should use
// NewPositive or NewPositiveUSD.
type MinorUnit int64

// Money is an amount in integer minor units and its currency. AmountMinor is
// signed so subtraction and journal-derived balances can be represented
// without a second numeric type.
type Money struct {
	AmountMinor int64    `json:"amount_minor"`
	Currency    Currency `json:"currency"`
}

// New validates the currency and creates a signed monetary value. Zero and
// negative values are valid here because balances and journal calculations
// can legitimately use them. Use NewPositive for command amounts.
func New(amountMinor int64, currency Currency) (Money, error) {
	if err := currency.Validate(); err != nil {
		return Money{}, err
	}
	return Money{AmountMinor: amountMinor, Currency: currency}, nil
}

// NewUSD creates a signed USD value.
func NewUSD(amountMinor int64) (Money, error) {
	return New(amountMinor, USD)
}

// NewPositive validates that amountMinor is greater than zero and creates a
// monetary value. This is the constructor for deposits, withdrawals,
// reservations, postings, and other command amounts.
func NewPositive(amountMinor int64, currency Currency) (Money, error) {
	if err := currency.Validate(); err != nil {
		return Money{}, err
	}
	if amountMinor <= 0 {
		return Money{}, ErrNonPositive
	}
	return Money{AmountMinor: amountMinor, Currency: currency}, nil
}

// NewPositiveUSD creates a positive USD command amount.
func NewPositiveUSD(amountMinor int64) (Money, error) {
	return NewPositive(amountMinor, USD)
}

// FromMinor constructs a signed USD value and is useful for balance results.
func FromMinor(amountMinor int64) Money {
	return Money{AmountMinor: amountMinor, Currency: USD}
}

// Validate checks the currency portion of a money value.
func (m Money) Validate() error {
	if err := m.Currency.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMoney, err)
	}
	return nil
}

// ValidatePositive checks the command invariant that an amount is greater
// than zero.
func (m Money) ValidatePositive() error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.AmountMinor <= 0 {
		return ErrNonPositive
	}
	return nil
}

// Validate checks that the currency is supported.
func (c Currency) Validate() error {
	if c != USD {
		return fmt.Errorf("%w: %q", ErrInvalidCurrency, c)
	}
	return nil
}

// IsUSD reports whether c is the supported v1 currency.
func (c Currency) IsUSD() bool {
	return c == USD
}

// Add adds two values after validating their currencies and checks int64
// overflow.
func (m Money) Add(other Money) (Money, error) {
	if err := m.validatePair(other); err != nil {
		return Money{}, err
	}
	value, err := checkedAdd(m.AmountMinor, other.AmountMinor)
	if err != nil {
		return Money{}, err
	}
	return Money{AmountMinor: value, Currency: m.Currency}, nil
}

// Sub subtracts two values after validating their currencies and checks int64
// overflow.
func (m Money) Sub(other Money) (Money, error) {
	if err := m.validatePair(other); err != nil {
		return Money{}, err
	}
	value, err := checkedSub(m.AmountMinor, other.AmountMinor)
	if err != nil {
		return Money{}, err
	}
	return Money{AmountMinor: value, Currency: m.Currency}, nil
}

// Multiply multiplies a value by an integer factor with overflow protection.
func (m Money) Multiply(factor int64) (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	value, err := checkedMultiply(m.AmountMinor, factor)
	if err != nil {
		return Money{}, err
	}
	return Money{AmountMinor: value, Currency: m.Currency}, nil
}

// Negate changes the sign with overflow protection.
func (m Money) Negate() (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	if m.AmountMinor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{AmountMinor: -m.AmountMinor, Currency: m.Currency}, nil
}

// Abs returns the absolute value with overflow protection.
func (m Money) Abs() (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	if m.AmountMinor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	if m.AmountMinor < 0 {
		m.AmountMinor = -m.AmountMinor
	}
	return m, nil
}

// Compare compares values with matching currencies, returning -1, 0, or 1.
func (m Money) Compare(other Money) (int, error) {
	if err := m.validatePair(other); err != nil {
		return 0, err
	}
	if m.AmountMinor < other.AmountMinor {
		return -1, nil
	}
	if m.AmountMinor > other.AmountMinor {
		return 1, nil
	}
	return 0, nil
}

// IsPositive reports whether the amount is greater than zero.
func (m Money) IsPositive() bool {
	return m.AmountMinor > 0
}

// IsNonNegative reports whether the amount is at least zero.
func (m Money) IsNonNegative() bool {
	return m.AmountMinor >= 0
}

// Minor returns the amount as the named minor-unit type.
func (m Money) Minor() MinorUnit {
	return MinorUnit(m.AmountMinor)
}

// Add performs checked arithmetic on two raw minor-unit values.
func (value MinorUnit) Add(other MinorUnit) (MinorUnit, error) {
	result, err := checkedAdd(int64(value), int64(other))
	return MinorUnit(result), err
}

// Sub performs checked arithmetic on two raw minor-unit values.
func (value MinorUnit) Sub(other MinorUnit) (MinorUnit, error) {
	result, err := checkedSub(int64(value), int64(other))
	return MinorUnit(result), err
}

// Multiply performs checked arithmetic on a raw minor-unit value.
func (value MinorUnit) Multiply(factor int64) (MinorUnit, error) {
	result, err := checkedMultiply(int64(value), factor)
	return MinorUnit(result), err
}

func (m Money) validatePair(other Money) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if err := other.Validate(); err != nil {
		return err
	}
	if m.Currency != other.Currency {
		return ErrCurrencyMismatch
	}
	return nil
}

func checkedAdd(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, ErrOverflow
	}
	if right < 0 && left < math.MinInt64-right {
		return 0, ErrOverflow
	}
	return left + right, nil
}

func checkedSub(left, right int64) (int64, error) {
	if right > 0 && left < math.MinInt64+right {
		return 0, ErrOverflow
	}
	if right < 0 && left > math.MaxInt64+right {
		return 0, ErrOverflow
	}
	return left - right, nil
}

func checkedMultiply(left, right int64) (int64, error) {
	if left == 0 || right == 0 {
		return 0, nil
	}
	if left == -1 && right == math.MinInt64 || right == -1 && left == math.MinInt64 {
		return 0, ErrOverflow
	}
	if left > 0 {
		if right > 0 && left > math.MaxInt64/right {
			return 0, ErrOverflow
		}
		if right < 0 && left > math.MinInt64/right {
			return 0, ErrOverflow
		}
	} else {
		if right > 0 && left < math.MinInt64/right {
			return 0, ErrOverflow
		}
		if right < 0 && left < math.MaxInt64/right {
			return 0, ErrOverflow
		}
	}
	return left * right, nil
}

// MarshalJSON validates money and emits the event/API minor-unit shape.
func (m Money) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	type wire Money
	return json.Marshal(wire(m))
}

// UnmarshalJSON decodes and validates money.
func (m *Money) UnmarshalJSON(value []byte) error {
	type wire Money
	var decoded wire
	if err := json.Unmarshal(value, &decoded); err != nil {
		return err
	}
	result := Money(decoded)
	if err := result.Validate(); err != nil {
		return err
	}
	if m == nil {
		return errors.New("nil money destination")
	}
	*m = result
	return nil
}
