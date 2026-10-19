package ledger

import (
	"errors"
	"math/rand"
	"testing"

	"digitalwallet/shared/identifiers"
	"digitalwallet/shared/money"
)

var (
	testWallet   = identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f1111")
	testClearing = identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f2222")
	testOther    = identifiers.MustParse("0192f4b7-6d92-7a4d-8cb5-1b3f9d8f3333")
)

func TestBuildEntries(t *testing.T) {
	amount := money.FromMinor(125)
	tests := []struct {
		name string
		make func() (JournalEntry, error)
		want []string
	}{
		{name: "deposit", make: func() (JournalEntry, error) { return Deposit(amount, testWallet, testClearing, "dep-1") }, want: []string{Credit, Debit}},
		{name: "withdrawal", make: func() (JournalEntry, error) { return Withdrawal(amount, testWallet, testClearing, "wd-1") }, want: []string{Debit, Credit}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry, err := test.make()
			if err != nil {
				t.Fatalf("build error = %v", err)
			}
			if err := entry.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			for index, side := range test.want {
				if entry.Postings[index].Side != side {
					t.Fatalf("posting %d side = %s, want %s", index, entry.Postings[index].Side, side)
				}
			}
		})
	}
}

func TestValidationRejectsInvalidEntries(t *testing.T) {
	amount := money.FromMinor(10)
	valid, err := Deposit(amount, testWallet, testClearing, "ref")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*JournalEntry)
		want error
	}{
		{name: "unbalanced", edit: func(entry *JournalEntry) { entry.Postings[1].Amount = money.FromMinor(9) }, want: ErrUnbalancedEntry},
		{name: "wrong side", edit: func(entry *JournalEntry) { entry.Postings[0].Side = "SIDEWAYS" }, want: ErrInvalidPosting},
		{name: "zero amount", edit: func(entry *JournalEntry) { entry.Postings[0].Amount = money.FromMinor(0) }, want: ErrInvalidPosting},
		{name: "missing posting", edit: func(entry *JournalEntry) { entry.Postings = entry.Postings[:1] }, want: ErrInvalidEntry},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := valid
			test.edit(&entry)
			if err := entry.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBalanceAndAvailableBalance(t *testing.T) {
	amount := money.FromMinor(100)
	entry, err := Deposit(amount, testWallet, testClearing, "ref")
	if err != nil {
		t.Fatal(err)
	}
	balance, err := Balance(entry.Postings, testWallet, money.USD)
	if err != nil || balance.AmountMinor != 100 {
		t.Fatalf("Balance() = %+v, %v", balance, err)
	}
	available, err := AvailableBalance(balance, money.FromMinor(0))
	if err != nil || available.AmountMinor != 100 {
		t.Fatalf("AvailableBalance() = %+v, %v", available, err)
	}
}

func TestRandomLedgerSequencesConserveMoney(t *testing.T) {
	for seed := int64(0); seed < 100; seed++ {
		random := rand.New(rand.NewSource(seed))
		balance := int64(0)
		postings := make([]Posting, 0, 200)
		for step := 0; step < 200; step++ {
			amount := int64(random.Intn(50) + 1)
			if random.Intn(3) == 0 && balance >= amount {
				entry, err := Withdrawal(money.FromMinor(amount), testWallet, testClearing, "random")
				if err != nil {
					t.Fatal(err)
				}
				balance -= amount
				postings = append(postings, entry.Postings...)
			} else {
				entry, err := Deposit(money.FromMinor(amount), testWallet, testClearing, "random")
				if err != nil {
					t.Fatal(err)
				}
				balance += amount
				postings = append(postings, entry.Postings...)
			}
			if balance < 0 {
				t.Fatalf("seed %d step %d produced negative balance %d", seed, step, balance)
			}
		}
		computed, err := Balance(postings, testWallet, money.USD)
		if err != nil || computed.AmountMinor != balance {
			t.Fatalf("seed %d computed balance = %+v, %v; model = %d", seed, computed, err, balance)
		}
	}
}

func TestBalanceIgnoresOtherAccount(t *testing.T) {
	entry, err := Deposit(money.FromMinor(20), testOther, testClearing, "other")
	if err != nil {
		t.Fatal(err)
	}
	balance, err := Balance(entry.Postings, testWallet, money.USD)
	if err != nil || balance.AmountMinor != 0 {
		t.Fatalf("Balance() = %+v, %v", balance, err)
	}
}
