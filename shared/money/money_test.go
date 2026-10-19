package money

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestNewPositive(t *testing.T) {
	tests := []struct {
		name     string
		amount   int64
		currency Currency
		wantErr  error
	}{
		{name: "positive USD", amount: 125, currency: USD},
		{name: "zero", amount: 0, currency: USD, wantErr: ErrNonPositive},
		{name: "negative", amount: -1, currency: USD, wantErr: ErrNonPositive},
		{name: "unsupported currency", amount: 1, currency: Currency("EUR"), wantErr: ErrInvalidCurrency},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NewPositive(test.amount, test.currency)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("NewPositive() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewPositive() error = %v", err)
			}
			if got.AmountMinor != test.amount || got.Currency != test.currency {
				t.Fatalf("NewPositive() = %+v", got)
			}
		})
	}
}

func TestMoneyCheckedArithmetic(t *testing.T) {
	tests := []struct {
		name    string
		left    Money
		right   Money
		want    int64
		wantErr error
		op      func(Money, Money) (Money, error)
	}{
		{name: "add", left: FromMinor(10), right: FromMinor(5), want: 15, op: Money.Add},
		{name: "subtract", left: FromMinor(10), right: FromMinor(5), want: 5, op: Money.Sub},
		{name: "add overflow", left: FromMinor(math.MaxInt64), right: FromMinor(1), wantErr: ErrOverflow, op: Money.Add},
		{name: "subtract overflow", left: FromMinor(math.MinInt64), right: FromMinor(1), wantErr: ErrOverflow, op: Money.Sub},
		{name: "currency mismatch", left: FromMinor(10), right: Money{AmountMinor: 1, Currency: Currency("EUR")}, wantErr: ErrInvalidMoney, op: Money.Add},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.op(test.left, test.right)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("arithmetic error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil || got.AmountMinor != test.want {
				t.Fatalf("arithmetic = %+v, %v; want %d, nil", got, err, test.want)
			}
		})
	}
}

func TestMoneyMultiplyAndNegateOverflow(t *testing.T) {
	if got, err := FromMinor(12).Multiply(-2); err != nil || got.AmountMinor != -24 {
		t.Fatalf("Multiply() = %+v, %v; want -24, nil", got, err)
	}
	if _, err := FromMinor(math.MaxInt64).Multiply(2); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Multiply() error = %v, want ErrOverflow", err)
	}
	if _, err := (Money{AmountMinor: math.MinInt64, Currency: USD}).Negate(); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Negate() error = %v, want ErrOverflow", err)
	}
}

func TestMoneyJSON(t *testing.T) {
	want := FromMinor(1234)
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got Money
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got != want {
		t.Fatalf("decoded money = %+v, want %+v", got, want)
	}
}
