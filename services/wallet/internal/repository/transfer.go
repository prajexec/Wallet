package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"digitalwallet/services/wallet/internal/ledger"
	walletdb "digitalwallet/services/wallet/internal/storage/generated"
	"digitalwallet/shared/identifiers"
	"digitalwallet/shared/money"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) Capture(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash string) (CommandResult, error) {
	return r.postTransferOperation(ctx, tenantID, walletID, operationID, command, requestHash, "CAPTURE", "wallet.reservation_captured", ledger.Capture)
}

func (r *Repository) Finalize(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash string) (CommandResult, error) {
	return r.postTransferOperation(ctx, tenantID, walletID, operationID, command, requestHash, "FINALIZE", "wallet.pending_credit_finalized", ledger.Finalize)
}

func (r *Repository) Release(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash string) (CommandResult, error) {
	return r.transitionTransferState(ctx, tenantID, walletID, operationID, command, requestHash, "RELEASE", "wallet.reservation_released", "RESERVED", "RELEASED", true)
}

func (r *Repository) Compensate(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash string) (CommandResult, error) {
	if command.TargetOperationID.IsZero() {
		return CommandResult{}, ErrInvalidOperation
	}
	original, err := r.GetOperation(ctx, tenantID, command.TargetOperationID)
	if err != nil {
		return CommandResult{}, err
	}
	if original.Status != "COMPLETED" {
		return CommandResult{}, ErrInvalidState
	}
	return r.postCompensation(ctx, tenantID, walletID, operationID, command, requestHash, original)
}

type journalFactory func(money.Money, identifiers.UUID, identifiers.UUID, identifiers.UUID, string) (ledger.JournalEntry, error)

func (r *Repository) postTransferOperation(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash, kind, eventType string, factory journalFactory) (CommandResult, error) {
	var result CommandResult
	err := r.withTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := walletdb.New(tx)
		if err := validateTransferCommand(operationID, command); err != nil {
			return err
		}
		if err := q.AcquireWalletLock(ctx, walletdb.AcquireWalletLockParams{LockKey: tenantID.String() + ":wallet:" + walletID.String()}); err != nil {
			return err
		}
		if replayed, err := existingOperationResult(ctx, q, tenantID, walletID, operationID, command, requestHash); err != nil {
			return err
		} else if replayed != nil {
			result = *replayed
			return nil
		}
		wallet, err := q.GetWalletForRead(ctx, walletdb.GetWalletForReadParams{TenantID: tenantID, ID: walletID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if wallet.Status != "ACTIVE" {
			return ErrWalletClosed
		}
		hold, err := q.GetHoldByTransfer(ctx, walletdb.GetHoldByTransferParams{TenantID: tenantID, TransferID: command.TransferID, WalletID: walletID})
		if kind == "FINALIZE" {
			return r.finalizeInTransaction(ctx, q, tenantID, walletID, operationID, command, requestHash, wallet.AccountID, eventType, resultRef(&result))
		}
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if hold.Status != "RESERVED" || hold.AmountMinor != command.AmountMinor {
			return ErrInvalidState
		}
		if _, err := q.InsertWalletOperation(ctx, walletdb.InsertWalletOperationParams{TenantID: tenantID, OperationID: operationID, WalletID: walletID, TransferID: command.TransferID, OperationKind: kind, TargetOperationID: &hold.HoldOperationID, RequestHash: requestHash, AmountMinor: command.AmountMinor, Currency: command.Currency, CreatedAt: r.now().UTC()}); err != nil {
			return mapTransferConstraintError(err)
		}
		return r.finishJournalOperation(ctx, q, tenantID, walletID, operationID, command, requestHash, kind, eventType, wallet.AccountID, hold.HoldOperationID, factory, "RESERVED", resultRef(&result))
	})
	return result, err
}

func resultRef(result *CommandResult) *CommandResult { return result }

func existingOperationResult(ctx context.Context, q *walletdb.Queries, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash string) (*CommandResult, error) {
	operation, err := q.GetWalletOperation(ctx, walletdb.GetWalletOperationParams{TenantID: tenantID, OperationID: operationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if operation.RequestHash != requestHash || operation.WalletID != walletID || operation.TransferID != command.TransferID || operation.AmountMinor != command.AmountMinor {
		return nil, ErrIdempotencyConflict
	}
	body, err := json.Marshal(OperationResponse{OperationID: operation.OperationID, Status: operation.Status, JournalTransactionID: operation.JournalTransactionID})
	if err != nil {
		return nil, err
	}
	return &CommandResult{StatusCode: http.StatusOK, Body: body, Replayed: true}, nil
}

func (r *Repository) finishJournalOperation(ctx context.Context, q *walletdb.Queries, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash, kind, eventType string, walletAccountID, targetOperationID identifiers.UUID, factory journalFactory, expectedStatus string, result *CommandResult) error {
	clearingName := "outbound-clearing"
	if kind == "FINALIZE" {
		clearingName = "inbound-clearing"
	}
	clearing, err := ensureSystemAccount(ctx, q, r.ids, tenantID, clearingName, r.now().UTC())
	if err != nil {
		return err
	}
	amount, err := money.NewPositiveUSD(command.AmountMinor)
	if err != nil {
		return err
	}
	entry, err := factory(amount, walletAccountID, clearing.ID, command.TransferID, command.Reference)
	if err != nil {
		return err
	}
	if err := entry.Validate(); err != nil {
		return err
	}
	now := r.now().UTC()
	transactionID, err := r.insertJournal(ctx, q, tenantID, entry, command.TransferID, now)
	if err != nil {
		return err
	}
	eventID, _, err := r.appendEvent(ctx, q, tenantID, walletID, eventType, transferOperationData{WalletID: walletID, TransferID: command.TransferID, OperationID: operationID, AmountMinor: command.AmountMinor, Currency: money.USD, JournalTransactionID: &transactionID}, now)
	if err != nil {
		return err
	}
	if _, err := q.CompleteWalletOperation(ctx, walletdb.CompleteWalletOperationParams{TenantID: tenantID, OperationID: operationID, JournalTransactionID: &transactionID, EventID: &eventID, UpdatedAt: now}); err != nil {
		return err
	}
	if kind == "CAPTURE" {
		if _, err := q.TransitionHold(ctx, walletdb.TransitionHoldParams{TenantID: tenantID, HoldOperationID: targetOperationID, Status: "CAPTURED", UpdatedAt: now, Status_2: expectedStatus}); err != nil {
			return err
		}
	} else {
		if _, err := q.TransitionPendingCredit(ctx, walletdb.TransitionPendingCreditParams{TenantID: tenantID, PendingOperationID: targetOperationID, Status: "FINALIZED", UpdatedAt: now, Status_2: "PENDING"}); err != nil {
			return err
		}
	}
	body, err := json.Marshal(OperationResponse{OperationID: operationID, Status: "COMPLETED", JournalTransactionID: &transactionID})
	if err != nil {
		return err
	}
	*result = CommandResult{StatusCode: http.StatusOK, Body: body}
	_ = requestHash
	return nil
}

func (r *Repository) insertJournal(ctx context.Context, q *walletdb.Queries, tenantID identifiers.UUID, entry ledger.JournalEntry, transferID identifiers.UUID, now time.Time) (identifiers.UUID, error) {
	transactionID, err := r.ids.Generate()
	if err != nil {
		return identifiers.UUID{}, err
	}
	if _, err := q.InsertJournalTransaction(ctx, walletdb.InsertJournalTransactionParams{TenantID: tenantID, ID: transactionID, OperationType: entry.OperationType, CreatedAt: now}); err != nil {
		return identifiers.UUID{}, err
	}
	for _, posting := range entry.Postings {
		postingID, err := r.ids.Generate()
		if err != nil {
			return identifiers.UUID{}, err
		}
		if err := q.InsertJournalPosting(ctx, walletdb.InsertJournalPostingParams{TenantID: tenantID, ID: postingID, TransactionID: transactionID, AccountID: posting.AccountID, Side: posting.Side, AmountMinor: posting.Amount.AmountMinor, Currency: string(posting.Amount.Currency), EntryType: posting.EntryType, TransferID: &transferID, CreatedAt: now}); err != nil {
			return identifiers.UUID{}, err
		}
	}
	return transactionID, nil
}

func (r *Repository) transitionTransferState(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash, kind, eventType, expected, next string, release bool) (CommandResult, error) {
	var result CommandResult
	err := r.withTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := walletdb.New(tx)
		if err := validateTransferCommand(operationID, command); err != nil {
			return err
		}
		if err := q.AcquireWalletLock(ctx, walletdb.AcquireWalletLockParams{LockKey: tenantID.String() + ":wallet:" + walletID.String()}); err != nil {
			return err
		}
		if replayed, err := existingOperationResult(ctx, q, tenantID, walletID, operationID, command, requestHash); err != nil {
			return err
		} else if replayed != nil {
			result = *replayed
			return nil
		}
		hold, err := q.GetHoldByTransfer(ctx, walletdb.GetHoldByTransferParams{TenantID: tenantID, TransferID: command.TransferID, WalletID: walletID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if hold.Status != expected || hold.AmountMinor != command.AmountMinor {
			return ErrInvalidState
		}
		if _, err := q.InsertWalletOperation(ctx, walletdb.InsertWalletOperationParams{TenantID: tenantID, OperationID: operationID, WalletID: walletID, TransferID: command.TransferID, OperationKind: kind, TargetOperationID: &hold.HoldOperationID, RequestHash: requestHash, AmountMinor: command.AmountMinor, Currency: command.Currency, CreatedAt: r.now().UTC()}); err != nil {
			return mapTransferConstraintError(err)
		}
		now := r.now().UTC()
		if _, err := q.TransitionHold(ctx, walletdb.TransitionHoldParams{TenantID: tenantID, HoldOperationID: hold.HoldOperationID, Status: next, UpdatedAt: now, Status_2: expected}); err != nil {
			return err
		}
		eventID, _, err := r.appendEvent(ctx, q, tenantID, walletID, eventType, transferOperationData{WalletID: walletID, TransferID: command.TransferID, OperationID: operationID, AmountMinor: command.AmountMinor, Currency: money.USD}, now)
		if err != nil {
			return err
		}
		if _, err := q.CompleteWalletOperation(ctx, walletdb.CompleteWalletOperationParams{TenantID: tenantID, OperationID: operationID, EventID: &eventID, UpdatedAt: now}); err != nil {
			return err
		}
		body, err := json.Marshal(OperationResponse{OperationID: operationID, Status: "COMPLETED"})
		if err != nil {
			return err
		}
		result = CommandResult{StatusCode: http.StatusOK, Body: body}
		_ = release
		return nil
	})
	return result, err
}

func (r *Repository) finalizeInTransaction(ctx context.Context, q *walletdb.Queries, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash string, walletAccountID identifiers.UUID, eventType string, result *CommandResult) error {
	pending, err := q.GetPendingCreditByTransfer(ctx, walletdb.GetPendingCreditByTransferParams{TenantID: tenantID, TransferID: command.TransferID, WalletID: walletID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if pending.Status != "PENDING" || pending.AmountMinor != command.AmountMinor {
		return ErrInvalidState
	}
	if _, err := q.InsertWalletOperation(ctx, walletdb.InsertWalletOperationParams{TenantID: tenantID, OperationID: operationID, WalletID: walletID, TransferID: command.TransferID, OperationKind: "FINALIZE", TargetOperationID: &pending.PendingOperationID, RequestHash: requestHash, AmountMinor: command.AmountMinor, Currency: command.Currency, CreatedAt: r.now().UTC()}); err != nil {
		return mapTransferConstraintError(err)
	}
	return r.finishJournalOperation(ctx, q, tenantID, walletID, operationID, command, requestHash, "FINALIZE", eventType, walletAccountID, pending.PendingOperationID, ledger.Finalize, "PENDING", result)
}

func (r *Repository) postCompensation(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash string, original OperationResponse) (CommandResult, error) {
	var result CommandResult
	err := r.withTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := walletdb.New(tx)
		if err := q.AcquireWalletLock(ctx, walletdb.AcquireWalletLockParams{LockKey: tenantID.String() + ":wallet:" + walletID.String()}); err != nil {
			return err
		}
		if replayed, err := existingOperationResult(ctx, q, tenantID, walletID, operationID, command, requestHash); err != nil {
			return err
		} else if replayed != nil {
			result = *replayed
			return nil
		}
		wallet, err := q.GetWalletForRead(ctx, walletdb.GetWalletForReadParams{TenantID: tenantID, ID: walletID})
		if err != nil {
			return err
		}
		if _, err := q.InsertWalletOperation(ctx, walletdb.InsertWalletOperationParams{TenantID: tenantID, OperationID: operationID, WalletID: walletID, TransferID: command.TransferID, OperationKind: "COMPENSATE", TargetOperationID: &command.TargetOperationID, RequestHash: requestHash, AmountMinor: command.AmountMinor, Currency: command.Currency, CreatedAt: r.now().UTC()}); err != nil {
			return mapTransferConstraintError(err)
		}
		clearing, err := ensureSystemAccount(ctx, q, r.ids, tenantID, "outbound-clearing", r.now().UTC())
		if err != nil {
			return err
		}
		amount, err := money.NewPositiveUSD(command.AmountMinor)
		if err != nil {
			return err
		}
		entry, err := ledger.Compensate(amount, wallet.AccountID, clearing.ID, command.TransferID, "CAPTURE", command.Reference)
		if err != nil {
			return err
		}
		transactionID, err := r.insertJournal(ctx, q, tenantID, entry, command.TransferID, r.now().UTC())
		if err != nil {
			return err
		}
		eventID, _, err := r.appendEvent(ctx, q, tenantID, walletID, "wallet.compensation_posted", transferOperationData{WalletID: walletID, TransferID: command.TransferID, OperationID: operationID, AmountMinor: command.AmountMinor, Currency: money.USD, JournalTransactionID: &transactionID}, r.now().UTC())
		if err != nil {
			return err
		}
		if _, err := q.CompleteWalletOperation(ctx, walletdb.CompleteWalletOperationParams{TenantID: tenantID, OperationID: operationID, JournalTransactionID: &transactionID, EventID: &eventID, UpdatedAt: r.now().UTC()}); err != nil {
			return err
		}
		body, err := json.Marshal(OperationResponse{OperationID: operationID, Status: "COMPLETED", JournalTransactionID: &transactionID})
		if err != nil {
			return err
		}
		result = CommandResult{StatusCode: http.StatusOK, Body: body}
		_ = original
		return nil
	})
	return result, err
}
