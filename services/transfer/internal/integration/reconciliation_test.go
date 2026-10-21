package integration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"digitalwallet/services/transfer/internal/reconciliation"
	"digitalwallet/services/transfer/internal/repository"
	"digitalwallet/shared/identifiers"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func openTestDB(t *testing.T) (*sql.DB, context.Context, context.CancelFunc) {
	t.Helper()
	dsn := os.Getenv("TRANSFER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TRANSFER_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := db.PingContext(ctx); err != nil {
		cancel()
		t.Skipf("transfer test database unavailable: %v", err)
	}
	return db, ctx, cancel
}

func TestReconciliationExceptionLifecycle(t *testing.T) {
	db, ctx, cancel := openTestDB(t)
	defer cancel()

	ids := identifiers.NewGenerator()
	repo := repository.New(db, ids)
	tenant := mustID(t)
	source := mustID(t)
	destination := mustID(t)
	transfer, _, err := repo.Create(ctx, tenant, "phase7-lifecycle", "phase7-hash", repository.Transfer{SourceWalletID: source, DestinationWalletID: destination, AmountMinor: 500, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	exception, reused, err := repo.Reconciliation(ctx, tenant, transfer.TransferID, "CAPTURE", true, map[string]any{"operation_id": "ambiguous"})
	if err != nil || reused {
		t.Fatalf("open exception = %+v reused=%v err=%v", exception, reused, err)
	}
	replayed, reused, err := repo.Reconciliation(ctx, tenant, transfer.TransferID, "CAPTURE", true, map[string]any{"different": "ignored"})
	if err != nil || !reused || replayed.ExceptionID != exception.ExceptionID {
		t.Fatalf("duplicate exception = %+v reused=%v err=%v", replayed, reused, err)
	}
	claimed, err := repo.ClaimException(ctx, tenant, exception.ExceptionID, "operator-7")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != repository.ExceptionInvestigating {
		t.Fatalf("status = %s", claimed.Status)
	}
	resolved, err := repo.ResolveException(ctx, tenant, exception.ExceptionID, "operator-7", "operator-7", repository.ResolutionNoAction, repository.ResolutionEvidence{
		TransferState: "CREATED",
		AmountMinor:   500,
		Currency:      "USD",
		Source: repository.OperationEvidence{
			OperationIDs:          []identifiers.UUID{mustID(t)},
			JournalTransactionIDs: []identifiers.UUID{mustID(t)},
		},
		Destination: repository.OperationEvidence{
			OperationIDs:          []identifiers.UUID{mustID(t)},
			JournalTransactionIDs: []identifiers.UUID{mustID(t)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != repository.ExceptionResolved || resolved.ResolutionAction != repository.ResolutionNoAction {
		t.Fatalf("resolved exception = %+v", resolved)
	}
}

func TestReconciliationInvestigationNotes(t *testing.T) {
	db, ctx, cancel := openTestDB(t)
	defer cancel()

	ids := identifiers.NewGenerator()
	repo := repository.New(db, ids)
	tenant := mustID(t)
	source := mustID(t)
	destination := mustID(t)
	transfer, _, err := repo.Create(ctx, tenant, "phase7-notes", "phase7-hash-notes", repository.Transfer{SourceWalletID: source, DestinationWalletID: destination, AmountMinor: 1000, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	exception, _, err := repo.Reconciliation(ctx, tenant, transfer.TransferID, "CAPTURE", true, map[string]any{"reason": "timeout"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimException(ctx, tenant, exception.ExceptionID, "analyst-1")
	if err != nil {
		t.Fatal(err)
	}

	// Add investigation notes
	if err := repo.AddExceptionNote(ctx, claimed, "analyst-1", map[string]any{"action": "inspected wallet-source logs", "finding": "no record"}); err != nil {
		t.Fatalf("failed to add first note: %v", err)
	}
	if err := repo.AddExceptionNote(ctx, claimed, "analyst-1", map[string]any{"action": "inspected wallet-destination logs", "finding": "no record"}); err != nil {
		t.Fatalf("failed to add second note: %v", err)
	}

	notes, err := repo.ListExceptionNotes(ctx, tenant, exception.ExceptionID)
	if err != nil {
		t.Fatalf("failed to list notes: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(notes))
	}
	if notes[0].Owner != "analyst-1" || notes[1].Owner != "analyst-1" {
		t.Fatalf("unexpected note owners: %+v", notes)
	}
}

func TestReconciliationNonMonetaryAcceptance(t *testing.T) {
	db, ctx, cancel := openTestDB(t)
	defer cancel()

	ids := identifiers.NewGenerator()
	repo := repository.New(db, ids)
	tenant := mustID(t)
	source := mustID(t)
	destination := mustID(t)
	transfer, _, err := repo.Create(ctx, tenant, "phase7-non-monetary", "phase7-hash-nm", repository.Transfer{SourceWalletID: source, DestinationWalletID: destination, AmountMinor: 750, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	// Create non-monetary exception
	exception, _, err := repo.Reconciliation(ctx, tenant, transfer.TransferID, "NOTIFICATION_DELAY", false, map[string]any{"detail": "nats puback delayed"})
	if err != nil {
		t.Fatal(err)
	}
	if exception.IsMonetary {
		t.Fatal("expected is_monetary=false")
	}

	claimed, err := repo.ClaimException(ctx, tenant, exception.ExceptionID, "operator-nm")
	if err != nil {
		t.Fatal(err)
	}

	accepted, err := repo.AcceptException(ctx, tenant, claimed.ExceptionID, "operator-nm", "operator-nm", repository.ResolutionEvidence{
		TransferState: "CREATED",
		AmountMinor:   750,
		Currency:      "USD",
		Source: repository.OperationEvidence{
			OperationIDs:          []identifiers.UUID{mustID(t)},
			JournalTransactionIDs: []identifiers.UUID{mustID(t)},
		},
		Destination: repository.OperationEvidence{
			OperationIDs:          []identifiers.UUID{mustID(t)},
			JournalTransactionIDs: []identifiers.UUID{mustID(t)},
		},
	})
	if err != nil {
		t.Fatalf("failed to accept non-monetary exception: %v", err)
	}
	if accepted.Status != repository.ExceptionAcceptedWithEvidence || accepted.ResolutionAction != repository.ResolutionAccept {
		t.Fatalf("unexpected accepted exception: %+v", accepted)
	}
}

func TestReconciliationMismatchAndImmutabilityConstraints(t *testing.T) {
	db, ctx, cancel := openTestDB(t)
	defer cancel()

	ids := identifiers.NewGenerator()
	repo := repository.New(db, ids)
	tenant := mustID(t)
	source := mustID(t)
	destination := mustID(t)
	transfer, _, err := repo.Create(ctx, tenant, "phase7-constraints", "phase7-hash-constraints", repository.Transfer{SourceWalletID: source, DestinationWalletID: destination, AmountMinor: 2500, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	exception, _, err := repo.Reconciliation(ctx, tenant, transfer.TransferID, "FINALIZE", true, map[string]any{"err": "timeout"})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Direct transition from OPEN to RESOLVED should fail
	_, err = repo.ResolveException(ctx, tenant, exception.ExceptionID, "op-1", "op-1", repository.ResolutionNoAction, repository.ResolutionEvidence{
		TransferState: "CREATED", AmountMinor: 2500, Currency: "USD",
		Source:      repository.OperationEvidence{OperationIDs: []identifiers.UUID{mustID(t)}, JournalTransactionIDs: []identifiers.UUID{mustID(t)}},
		Destination: repository.OperationEvidence{OperationIDs: []identifiers.UUID{mustID(t)}, JournalTransactionIDs: []identifiers.UUID{mustID(t)}},
	})
	if err == nil {
		t.Fatal("expected error when resolving directly from OPEN")
	}

	// Claim exception
	claimed, err := repo.ClaimException(ctx, tenant, exception.ExceptionID, "op-1")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Claiming with different operator while in INVESTIGATING should fail with conflict
	_, err = repo.ClaimException(ctx, tenant, claimed.ExceptionID, "op-2")
	if !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("expected ErrConflict when different operator claims, got %v", err)
	}

	// 3. Monetary exception cannot be ACCEPTED_WITH_EVIDENCE
	_, err = repo.AcceptException(ctx, tenant, claimed.ExceptionID, "op-1", "op-1", repository.ResolutionEvidence{
		TransferState: "CREATED", AmountMinor: 2500, Currency: "USD",
		Source:      repository.OperationEvidence{OperationIDs: []identifiers.UUID{mustID(t)}, JournalTransactionIDs: []identifiers.UUID{mustID(t)}},
		Destination: repository.OperationEvidence{OperationIDs: []identifiers.UUID{mustID(t)}, JournalTransactionIDs: []identifiers.UUID{mustID(t)}},
	})
	if err == nil {
		t.Fatal("expected error when accepting monetary exception")
	}

	// 4. Monetary resolution with mismatched amount should fail validation
	_, err = repo.ResolveException(ctx, tenant, claimed.ExceptionID, "op-1", "op-1", repository.ResolutionNoAction, repository.ResolutionEvidence{
		TransferState: "CREATED", AmountMinor: 9999, Currency: "USD",
		Source:      repository.OperationEvidence{OperationIDs: []identifiers.UUID{mustID(t)}, JournalTransactionIDs: []identifiers.UUID{mustID(t)}},
		Destination: repository.OperationEvidence{OperationIDs: []identifiers.UUID{mustID(t)}, JournalTransactionIDs: []identifiers.UUID{mustID(t)}},
	})
	if !errors.Is(err, repository.ErrInvalidResolution) {
		t.Fatalf("expected ErrInvalidResolution for mismatched amount, got %v", err)
	}

	// 5. Monetary resolution with mismatched currency should fail validation
	_, err = repo.ResolveException(ctx, tenant, claimed.ExceptionID, "op-1", "op-1", repository.ResolutionNoAction, repository.ResolutionEvidence{
		TransferState: "CREATED", AmountMinor: 2500, Currency: "EUR",
		Source:      repository.OperationEvidence{OperationIDs: []identifiers.UUID{mustID(t)}, JournalTransactionIDs: []identifiers.UUID{mustID(t)}},
		Destination: repository.OperationEvidence{OperationIDs: []identifiers.UUID{mustID(t)}, JournalTransactionIDs: []identifiers.UUID{mustID(t)}},
	})
	if !errors.Is(err, repository.ErrInvalidResolution) {
		t.Fatalf("expected ErrInvalidResolution for mismatched currency, got %v", err)
	}

	// 6. Monetary resolution with valid evidence succeeds
	resolved, err := repo.ResolveException(ctx, tenant, claimed.ExceptionID, "op-1", "op-1", repository.ResolutionNoAction, repository.ResolutionEvidence{
		TransferState: "CREATED", AmountMinor: 2500, Currency: "USD",
		Source:      repository.OperationEvidence{OperationIDs: []identifiers.UUID{mustID(t)}, JournalTransactionIDs: []identifiers.UUID{mustID(t)}},
		Destination: repository.OperationEvidence{OperationIDs: []identifiers.UUID{mustID(t)}, JournalTransactionIDs: []identifiers.UUID{mustID(t)}},
	})
	if err != nil {
		t.Fatalf("valid resolution failed: %v", err)
	}

	// 7. Modifying resolved exception should fail (trigger prevents updates on terminal rows)
	_, err = repo.ClaimException(ctx, tenant, resolved.ExceptionID, "op-1")
	if err == nil {
		t.Fatal("expected error when modifying terminal exception")
	}

	// 8. Trigger prevents deletion of exceptions
	_, err = db.ExecContext(ctx, `DELETE FROM reconciliation_exceptions WHERE tenant_id=$1 AND exception_id=$2`, tenant.String(), resolved.ExceptionID.String())
	if err == nil {
		t.Fatal("expected trigger error on DELETE of reconciliation_exceptions")
	}
}

func TestReconciliationLivenessSweep(t *testing.T) {
	db, ctx, cancel := openTestDB(t)
	defer cancel()

	ids := identifiers.NewGenerator()
	repo := repository.New(db, ids)
	tenant := mustID(t)
	source := mustID(t)
	destination := mustID(t)
	transfer, _, err := repo.Create(ctx, tenant, "phase7-sweep", "phase7-hash-sweep", repository.Transfer{SourceWalletID: source, DestinationWalletID: destination, AmountMinor: 1500, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}

	// Create an exception in OPEN state
	exception, _, err := repo.Reconciliation(ctx, tenant, transfer.TransferID, "RESERVE", true, map[string]any{"reason": "timeout"})
	if err != nil {
		t.Fatal(err)
	}

	// Verify it shows up in active list
	actives, err := repo.ListActiveExceptions(ctx, tenant, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, act := range actives {
		if act.ExceptionID == exception.ExceptionID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected exception %s to be in active list", exception.ExceptionID)
	}

	// Create reconciliation service without configured wallet clients and sweep
	service := reconciliation.New(repo, nil, nil, nil)
	if err := service.Sweep(ctx, "sweep-operator"); err != nil {
		t.Fatalf("sweep failed: %v", err)
	}

	// Verify that an investigation note was added explaining the client issue
	notes, err := repo.ListExceptionNotes(ctx, tenant, exception.ExceptionID)
	if err != nil {
		t.Fatalf("failed to list exception notes: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("expected at least one investigation note after sweep")
	}
	if notes[0].Owner != "sweep-operator" {
		t.Fatalf("expected note owner 'sweep-operator', got %q", notes[0].Owner)
	}
}

func mustID(t *testing.T) identifiers.UUID {
	t.Helper()
	id, err := identifiers.NewGenerator().Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
