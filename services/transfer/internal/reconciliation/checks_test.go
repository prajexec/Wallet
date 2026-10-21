package reconciliation

import "testing"

func TestCheckJournalBalance(t *testing.T) {
	if err := CheckJournalBalance(JournalTotals{Debits: 100, Credits: 100}); err != nil {
		t.Fatal(err)
	}
	if err := CheckJournalBalance(JournalTotals{Debits: 100, Credits: 99}); err == nil {
		t.Fatal("expected mismatch")
	}
}

func TestCheckConservation(t *testing.T) {
	if err := CheckConservation(Conservation{SourceCapture: 50, DestinationFinalize: 50, TransferAmount: 50}); err != nil {
		t.Fatal(err)
	}
	if err := CheckConservation(Conservation{SourceCapture: 50, DestinationFinalize: 49, TransferAmount: 50}); err == nil {
		t.Fatal("expected mismatch")
	}
	if err := CheckConservation(Conservation{SourceCapture: 50, DestinationFinalize: 50, TransferAmount: 50, OutboundClearing: 1}); err == nil {
		t.Fatal("expected clearing mismatch")
	}
}

func TestValidExceptionTransition(t *testing.T) {
	cases := []struct {
		current, next string
		monetary      bool
		want          bool
	}{
		{"OPEN", "INVESTIGATING", true, true},
		{"OPEN", "RESOLVED", true, false},
		{"INVESTIGATING", "RESOLVED", true, true},
		{"INVESTIGATING", "ACCEPTED_WITH_EVIDENCE", true, false},
		{"INVESTIGATING", "ACCEPTED_WITH_EVIDENCE", false, true},
		{"RESOLVED", "OPEN", true, false},
	}
	for _, tc := range cases {
		if got := ValidExceptionTransition(repositoryExceptionStatus(tc.current), repositoryExceptionStatus(tc.next), tc.monetary) == nil; got != tc.want {
			t.Errorf("%s -> %s monetary=%v valid=%v want %v", tc.current, tc.next, tc.monetary, got, tc.want)
		}
	}
}
