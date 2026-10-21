package reconciliation

import (
	"errors"
	"testing"

	"digitalwallet/services/transfer/internal/repository"
	"digitalwallet/services/transfer/internal/walletclient"
	"digitalwallet/shared/identifiers"
)

func TestDecide(t *testing.T) {
	tests := []struct {
		name      string
		step      string
		op        walletclient.Response
		err       error
		wantOut   Outcome
		wantState string
		wantRes   bool
	}{
		{
			name:    "wallet 404 returns absent and resumes",
			step:    "RESERVE",
			err:     &walletclient.Error{Status: 404},
			wantOut: OutcomeAbsent,
			wantRes: true,
		},
		{
			name:    "network error returns unknown",
			step:    "RESERVE",
			err:     errors.New("connection reset"),
			wantOut: OutcomeUnknown,
			wantRes: false,
		},
		{
			name:    "wallet 500 error returns unknown",
			step:    "RESERVE",
			err:     &walletclient.Error{Status: 500},
			wantOut: OutcomeUnknown,
			wantRes: false,
		},
		{
			name:    "non-completed status returns unknown",
			step:    "RESERVE",
			op:      walletclient.Response{Status: "PENDING"},
			wantOut: OutcomeUnknown,
			wantRes: false,
		},
		{
			name:      "reserve completed confirms and resumes to RESERVED",
			step:      "RESERVE",
			op:        walletclient.Response{Status: "COMPLETED"},
			wantOut:   OutcomeConfirmed,
			wantState: "RESERVED",
			wantRes:   true,
		},
		{
			name:      "pending credit completed confirms and resumes to PENDING_CREDITED",
			step:      "PENDING_CREDIT",
			op:        walletclient.Response{Status: "COMPLETED"},
			wantOut:   OutcomeConfirmed,
			wantState: "PENDING_CREDITED",
			wantRes:   true,
		},
		{
			name:      "capture completed confirms and resumes to CAPTURED",
			step:      "CAPTURE",
			op:        walletclient.Response{Status: "COMPLETED"},
			wantOut:   OutcomeConfirmed,
			wantState: "CAPTURED",
			wantRes:   true,
		},
		{
			name:      "finalize completed confirms to COMPLETED without resume",
			step:      "FINALIZE",
			op:        walletclient.Response{Status: "COMPLETED"},
			wantOut:   OutcomeConfirmed,
			wantState: "COMPLETED",
			wantRes:   false,
		},
		{
			name:      "release completed confirms to COMPENSATED and resumes",
			step:      "RELEASE",
			op:        walletclient.Response{Status: "COMPLETED"},
			wantOut:   OutcomeConfirmed,
			wantState: "COMPENSATED",
			wantRes:   true,
		},
		{
			name:      "compensate completed confirms to COMPENSATED without resume",
			step:      "COMPENSATE",
			op:        walletclient.Response{Status: "COMPLETED"},
			wantOut:   OutcomeConfirmed,
			wantState: "COMPENSATED",
			wantRes:   false,
		},
		{
			name:    "unknown step returns unknown",
			step:    "UNKNOWN_STEP",
			op:      walletclient.Response{Status: "COMPLETED"},
			wantOut: OutcomeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.step, tt.op, tt.err)
			if got.Outcome != tt.wantOut {
				t.Errorf("Decide() Outcome = %v, want %v", got.Outcome, tt.wantOut)
			}
			if got.NextState != tt.wantState {
				t.Errorf("Decide() NextState = %v, want %v", got.NextState, tt.wantState)
			}
			if got.Resume != tt.wantRes {
				t.Errorf("Decide() Resume = %v, want %v", got.Resume, tt.wantRes)
			}
		})
	}
}

func TestOperationMatches(t *testing.T) {
	gen := identifiers.NewGenerator()
	tenantID, _ := gen.Generate()
	sourceWalletID, _ := gen.Generate()
	destWalletID, _ := gen.Generate()
	transferID, _ := gen.Generate()
	journalTxID, _ := gen.Generate()

	transfer := repository.Transfer{
		TenantID:            tenantID,
		TransferID:          transferID,
		SourceWalletID:      sourceWalletID,
		DestinationWalletID: destWalletID,
		AmountMinor:         5000,
		Currency:            "USD",
	}

	baseOp := walletclient.Response{
		TenantID:             tenantID,
		WalletID:             sourceWalletID,
		TransferID:           transferID,
		OperationKind:        "RESERVE",
		AmountMinor:          5000,
		Currency:             "USD",
		Status:               "COMPLETED",
		JournalTransactionID: nil,
	}

	if !operationMatches(baseOp, tenantID, transfer, sourceWalletID, "RESERVE") {
		t.Fatal("expected base reserve operation to match")
	}

	// Capture requires JournalTransactionID
	captureOp := baseOp
	captureOp.OperationKind = "CAPTURE"
	if operationMatches(captureOp, tenantID, transfer, sourceWalletID, "CAPTURE") {
		t.Fatal("expected capture without journal_transaction_id to NOT match")
	}
	captureOp.JournalTransactionID = &journalTxID
	if !operationMatches(captureOp, tenantID, transfer, sourceWalletID, "CAPTURE") {
		t.Fatal("expected capture with journal_transaction_id to match")
	}

	// Amount mismatch
	badAmountOp := captureOp
	badAmountOp.AmountMinor = 4000
	if operationMatches(badAmountOp, tenantID, transfer, sourceWalletID, "CAPTURE") {
		t.Fatal("expected mismatched amount to NOT match")
	}

	// Currency mismatch
	badCurrencyOp := captureOp
	badCurrencyOp.Currency = "EUR"
	if operationMatches(badCurrencyOp, tenantID, transfer, sourceWalletID, "CAPTURE") {
		t.Fatal("expected mismatched currency to NOT match")
	}

	// Status mismatch
	badStatusOp := captureOp
	badStatusOp.Status = "FAILED"
	if operationMatches(badStatusOp, tenantID, transfer, sourceWalletID, "CAPTURE") {
		t.Fatal("expected non-completed status to NOT match")
	}
}

func TestStateBefore(t *testing.T) {
	if got := stateBefore("RESERVE"); got != "CREATED" {
		t.Errorf("stateBefore(RESERVE) = %q, want CREATED", got)
	}
	if got := stateBefore("PENDING_CREDIT"); got != "RESERVED" {
		t.Errorf("stateBefore(PENDING_CREDIT) = %q, want RESERVED", got)
	}
	if got := stateBefore("CAPTURE"); got != "PENDING_CREDITED" {
		t.Errorf("stateBefore(CAPTURE) = %q, want PENDING_CREDITED", got)
	}
	if got := stateBefore("FINALIZE"); got != "CAPTURED" {
		t.Errorf("stateBefore(FINALIZE) = %q, want CAPTURED", got)
	}
}
