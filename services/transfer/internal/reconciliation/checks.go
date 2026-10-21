package reconciliation

import (
	"errors"
	"fmt"
)

var ErrMismatch = errors.New("reconciliation mismatch")

type JournalTotals struct {
	Debits  int64
	Credits int64
}

func CheckJournalBalance(totals JournalTotals) error {
	if totals.Debits != totals.Credits || totals.Debits <= 0 {
		return fmt.Errorf("%w: debits=%d credits=%d", ErrMismatch, totals.Debits, totals.Credits)
	}
	return nil
}

type Conservation struct {
	SourceCapture       int64
	DestinationFinalize int64
	TransferAmount      int64
	OutboundClearing    int64
	InboundClearing     int64
}

func CheckConservation(value Conservation) error {
	if value.SourceCapture != value.DestinationFinalize || value.SourceCapture != value.TransferAmount {
		return fmt.Errorf("%w: source_capture=%d destination_finalize=%d transfer_amount=%d", ErrMismatch, value.SourceCapture, value.DestinationFinalize, value.TransferAmount)
	}
	if value.OutboundClearing != 0 || value.InboundClearing != 0 {
		return fmt.Errorf("%w: outbound_clearing=%d inbound_clearing=%d", ErrMismatch, value.OutboundClearing, value.InboundClearing)
	}
	return nil
}

func ValidExceptionTransition(current, next repositoryExceptionStatus, monetary bool) error {
	if current == "OPEN" && next != "OPEN" && next != "INVESTIGATING" {
		return ErrMismatch
	}
	if current == "INVESTIGATING" && next != "INVESTIGATING" && next != "RESOLVED" && next != "ACCEPTED_WITH_EVIDENCE" {
		return ErrMismatch
	}
	if current == "RESOLVED" || current == "ACCEPTED_WITH_EVIDENCE" {
		return ErrMismatch
	}
	if next == "ACCEPTED_WITH_EVIDENCE" && monetary {
		return ErrMismatch
	}
	return nil
}

type repositoryExceptionStatus string
