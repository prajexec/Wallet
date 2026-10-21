package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"digitalwallet/services/wallet/internal/ledger"
	walletdb "digitalwallet/services/wallet/internal/storage/generated"
	"digitalwallet/shared/identifiers"
	"digitalwallet/shared/money"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var clearingAccounts = []string{
	"deposit-clearing",
	"withdrawal-clearing",
	"outbound-clearing",
	"inbound-clearing",
}

type walletCreatedData struct {
	WalletID  identifiers.UUID `json:"wallet_id"`
	AccountID identifiers.UUID `json:"account_id"`
	Currency  money.Currency   `json:"currency"`
}

type journalPostedData struct {
	WalletID             identifiers.UUID `json:"wallet_id"`
	JournalTransactionID identifiers.UUID `json:"journal_transaction_id"`
	AmountMinor          int64            `json:"amount_minor"`
	Currency             money.Currency   `json:"currency"`
	Side                 string           `json:"side"`
	EntryType            string           `json:"entry_type"`
	Reference            string           `json:"reference"`
}

type transferOperationData struct {
	WalletID             identifiers.UUID  `json:"wallet_id"`
	TransferID           identifiers.UUID  `json:"transfer_id"`
	OperationID          identifiers.UUID  `json:"operation_id"`
	AmountMinor          int64             `json:"amount_minor"`
	Currency             money.Currency    `json:"currency"`
	JournalTransactionID *identifiers.UUID `json:"journal_transaction_id,omitempty"`
}

type TransferMoneyCommand struct {
	TransferID        identifiers.UUID
	AmountMinor       int64
	Currency          string
	Reference         string
	TargetOperationID identifiers.UUID
}

func (r *Repository) GetOperation(ctx context.Context, tenantID, operationID identifiers.UUID) (OperationResponse, error) {
	operation, err := walletdb.New(r.pool).GetWalletOperation(ctx, walletdb.GetWalletOperationParams{TenantID: tenantID, OperationID: operationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return OperationResponse{}, ErrNotFound
	}
	if err != nil {
		return OperationResponse{}, err
	}
	return OperationResponse{
		TenantID: operation.TenantID, OperationID: operation.OperationID, WalletID: operation.WalletID,
		TransferID: operation.TransferID, OperationKind: operation.OperationKind, AmountMinor: operation.AmountMinor,
		Currency: money.Currency(operation.Currency), Status: operation.Status, JournalTransactionID: operation.JournalTransactionID,
	}, nil
}

func (r *Repository) Reserve(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash string) (CommandResult, error) {
	return r.stateOnlyOperation(ctx, tenantID, walletID, operationID, command, requestHash, "RESERVE", "wallet.reservation_created")
}

func (r *Repository) CreatePendingCredit(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash string) (CommandResult, error) {
	return r.stateOnlyOperation(ctx, tenantID, walletID, operationID, command, requestHash, "PENDING_CREDIT", "wallet.pending_credit_created")
}

func (r *Repository) stateOnlyOperation(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, command TransferMoneyCommand, requestHash, kind, eventType string) (CommandResult, error) {
	var result CommandResult
	err := r.withTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := walletdb.New(tx)
		if err := validateTransferCommand(operationID, command); err != nil {
			return err
		}
		if err := q.AcquireWalletLock(ctx, walletdb.AcquireWalletLockParams{LockKey: tenantID.String() + ":wallet:" + walletID.String()}); err != nil {
			return err
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
		if existing, lookupErr := q.GetWalletOperation(ctx, walletdb.GetWalletOperationParams{TenantID: tenantID, OperationID: operationID}); lookupErr == nil {
			if existing.RequestHash != requestHash || existing.AmountMinor != command.AmountMinor || existing.TransferID != command.TransferID || existing.WalletID != walletID {
				return ErrIdempotencyConflict
			}
			body, err := json.Marshal(OperationResponse{OperationID: operationID, Status: existing.Status, JournalTransactionID: existing.JournalTransactionID})
			if err != nil {
				return err
			}
			result = CommandResult{StatusCode: http.StatusOK, Body: body, Replayed: true}
			return nil
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return lookupErr
		}
		if kind == "RESERVE" {
			balance, err := q.GetWalletBalanceWithVersion(ctx, walletdb.GetWalletBalanceWithVersionParams{TenantID: tenantID, WalletID: walletID, AggregateID: walletID, Currency: currencyUSD})
			if err != nil {
				return err
			}
			if int64(balance.AvailableBalanceMinor) < command.AmountMinor {
				return ErrInsufficientFunds
			}
		}
		_, err = q.InsertWalletOperation(ctx, walletdb.InsertWalletOperationParams{TenantID: tenantID, OperationID: operationID, WalletID: walletID, TransferID: command.TransferID, OperationKind: kind, RequestHash: requestHash, AmountMinor: command.AmountMinor, Currency: command.Currency, CreatedAt: r.now().UTC()})
		if err != nil {
			return mapTransferConstraintError(err)
		}
		now := r.now().UTC()
		var eventData any = transferOperationData{WalletID: walletID, TransferID: command.TransferID, OperationID: operationID, AmountMinor: command.AmountMinor, Currency: money.Currency(command.Currency)}
		if kind == "RESERVE" {
			if _, err := q.InsertHold(ctx, walletdb.InsertHoldParams{TenantID: tenantID, HoldOperationID: operationID, WalletID: walletID, TransferID: command.TransferID, AmountMinor: command.AmountMinor, Currency: command.Currency, CreatedAt: now}); err != nil {
				return mapTransferConstraintError(err)
			}
		} else {
			if _, err := q.InsertPendingCredit(ctx, walletdb.InsertPendingCreditParams{TenantID: tenantID, PendingOperationID: operationID, WalletID: walletID, TransferID: command.TransferID, AmountMinor: command.AmountMinor, Currency: command.Currency, CreatedAt: now}); err != nil {
				return mapTransferConstraintError(err)
			}
		}
		eventID, version, err := r.appendEvent(ctx, q, tenantID, walletID, eventType, eventData, now)
		if err != nil {
			return err
		}
		if _, err := q.CompleteWalletOperation(ctx, walletdb.CompleteWalletOperationParams{TenantID: tenantID, OperationID: operationID, EventID: &eventID, UpdatedAt: now}); err != nil {
			return err
		}
		response := OperationResponse{OperationID: operationID, Status: "COMPLETED"}
		body, err := json.Marshal(response)
		if err != nil {
			return err
		}
		result = CommandResult{StatusCode: http.StatusOK, Body: body}
		_ = version
		return nil
	})
	return result, err
}

func validateTransferCommand(operationID identifiers.UUID, command TransferMoneyCommand) error {
	if operationID.IsZero() || !operationID.IsV7() {
		return ErrInvalidOperation
	}
	if command.TransferID.IsZero() || !command.TransferID.IsV7() {
		return ErrInvalidOperation
	}
	amount, err := money.NewPositiveUSD(command.AmountMinor)
	if err != nil {
		return err
	}
	if command.Currency != string(amount.Currency) {
		return money.ErrInvalidCurrency
	}
	return nil
}

func mapTransferConstraintError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23514") {
		return ErrIdempotencyConflict
	}
	return err
}

func (r *Repository) CreateWallet(ctx context.Context, tenantID identifiers.UUID, key, requestHash, externalReference string) (CommandResult, error) {
	var result CommandResult
	err := r.withTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := walletdb.New(tx)
		replayed, err := claimIdempotency(ctx, q, tenantID, key, requestHash)
		if err != nil {
			return err
		}
		if replayed != nil {
			result = *replayed
			return nil
		}
		walletID, err := r.ids.Generate()
		if err != nil {
			return err
		}
		now := r.now().UTC()
		if err := q.InsertAccount(ctx, walletdb.InsertAccountParams{
			TenantID: tenantID, ID: walletID, Kind: "user_wallet", Name: "wallet:" + walletID.String(), Currency: currencyUSD, CreatedAt: now,
		}); err != nil {
			return err
		}
		for _, accountName := range clearingAccounts {
			if _, err := ensureSystemAccount(ctx, q, r.ids, tenantID, accountName, now); err != nil {
				return err
			}
		}
		if _, err := q.InsertWallet(ctx, walletdb.InsertWalletParams{TenantID: tenantID, ID: walletID, AccountID: walletID, Status: "ACTIVE", CreatedAt: now}); err != nil {
			return err
		}
		eventID, nextVersion, err := r.appendEvent(ctx, q, tenantID, walletID, "wallet.created", walletCreatedData{WalletID: walletID, AccountID: walletID, Currency: money.USD}, now)
		if err != nil {
			return err
		}
		body, err := json.Marshal(Wallet{TenantID: tenantID, WalletID: walletID, AccountID: walletID, Currency: money.USD, Status: "ACTIVE", CreatedAt: now, AggregateVersion: nextVersion})
		if err != nil {
			return err
		}
		body, err = canonicalJSON(body)
		if err != nil {
			return err
		}
		if err := completeIdempotency(ctx, q, tenantID, key, requestHash, 201, body, now); err != nil {
			return err
		}
		_ = eventID
		result = CommandResult{StatusCode: 201, Body: body}
		return nil
	})
	return result, err
}

func (r *Repository) Deposit(ctx context.Context, tenantID, walletID identifiers.UUID, key, requestHash string, amountMinor int64, reference string) (CommandResult, error) {
	return r.postMoney(ctx, tenantID, walletID, key, requestHash, amountMinor, reference, false)
}

func (r *Repository) Withdraw(ctx context.Context, tenantID, walletID identifiers.UUID, key, requestHash string, amountMinor int64, reference string) (CommandResult, error) {
	return r.postMoney(ctx, tenantID, walletID, key, requestHash, amountMinor, reference, true)
}

func (r *Repository) postMoney(ctx context.Context, tenantID, walletID identifiers.UUID, key, requestHash string, amountMinor int64, reference string, withdrawal bool) (CommandResult, error) {
	var result CommandResult
	err := r.withTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := walletdb.New(tx)
		replayed, err := claimIdempotency(ctx, q, tenantID, key, requestHash)
		if err != nil {
			return err
		}
		if replayed != nil {
			result = *replayed
			return nil
		}
		lockKey := tenantID.String() + ":wallet:" + walletID.String()
		if err := q.AcquireWalletLock(ctx, walletdb.AcquireWalletLockParams{LockKey: lockKey}); err != nil {
			return err
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
		amount, err := money.NewPositiveUSD(amountMinor)
		if err != nil {
			return err
		}
		balance, err := q.GetWalletBalanceWithVersion(ctx, walletdb.GetWalletBalanceWithVersionParams{
			TenantID:    tenantID,
			WalletID:    walletID,
			AggregateID: walletID,
			Currency:    currencyUSD,
		})
		if err != nil {
			return err
		}
		if withdrawal && int64(balance.AvailableBalanceMinor) < amountMinor {
			return ErrInsufficientFunds
		}
		clearingName := "deposit-clearing"
		var entry ledger.JournalEntry
		if withdrawal {
			clearingName = "withdrawal-clearing"
			entry, err = ledger.Withdrawal(amount, wallet.AccountID, identifiers.UUID{}, reference)
		} else {
			entry, err = ledger.Deposit(amount, wallet.AccountID, identifiers.UUID{}, reference)
		}
		if err != nil {
			return err
		}
		clearing, err := ensureSystemAccount(ctx, q, r.ids, tenantID, clearingName, r.now().UTC())
		if err != nil {
			return err
		}
		entry.Postings[1].AccountID = clearing.ID
		if err := entry.Validate(); err != nil {
			return err
		}
		now := r.now().UTC()
		transactionID, err := r.ids.Generate()
		if err != nil {
			return err
		}
		var externalReference *string
		if strings.TrimSpace(reference) != "" {
			externalReference = &reference
		}
		if _, err := q.InsertJournalTransaction(ctx, walletdb.InsertJournalTransactionParams{TenantID: tenantID, ID: transactionID, OperationType: entry.OperationType, ExternalReference: externalReference, CreatedAt: now}); err != nil {
			return err
		}
		for index := range entry.Postings {
			postingID, err := r.ids.Generate()
			if err != nil {
				return err
			}
			entry.Postings[index].ID = postingID
			posting := entry.Postings[index]
			if err := q.InsertJournalPosting(ctx, walletdb.InsertJournalPostingParams{
				TenantID: tenantID, ID: postingID, TransactionID: transactionID, AccountID: posting.AccountID,
				Side: posting.Side, AmountMinor: posting.Amount.AmountMinor, Currency: string(posting.Amount.Currency),
				EntryType: posting.EntryType, TransferID: posting.TransferID, CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		eventType := "wallet.deposit_posted"
		side := "CREDIT"
		entryType := "DEPOSIT"
		if withdrawal {
			eventType = "wallet.withdrawal_posted"
			side = "DEBIT"
			entryType = "WITHDRAWAL"
		}
		eventID, nextVersion, err := r.appendEvent(ctx, q, tenantID, walletID, eventType, journalPostedData{
			WalletID:             walletID,
			JournalTransactionID: transactionID,
			AmountMinor:          amountMinor,
			Currency:             money.USD,
			Side:                 side,
			EntryType:            entryType,
			Reference:            reference,
		}, now)
		if err != nil {
			return err
		}
		body, err := json.Marshal(JournalCommandResponse{
			OperationID:          eventID,
			JournalTransactionID: transactionID,
			EventID:              eventID,
			AmountMinor:          amountMinor,
			Currency:             money.USD,
			AggregateVersion:     nextVersion,
		})
		if err != nil {
			return err
		}
		body, err = canonicalJSON(body)
		if err != nil {
			return err
		}
		if err := completeIdempotency(ctx, q, tenantID, key, requestHash, 201, body, now); err != nil {
			return err
		}
		result = CommandResult{StatusCode: 201, Body: body}
		return nil
	})
	return result, err
}

func (r *Repository) GetWallet(ctx context.Context, tenantID, walletID identifiers.UUID) (Wallet, error) {
	row, err := walletdb.New(r.pool).GetWallet(ctx, walletdb.GetWalletParams{TenantID: tenantID, ID: walletID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Wallet{}, ErrNotFound
	}
	if err != nil {
		return Wallet{}, err
	}
	return Wallet{
		TenantID:         row.TenantID,
		WalletID:         row.ID,
		AccountID:        row.AccountID,
		Currency:         money.Currency(currencyUSD),
		Status:           row.Status,
		CreatedAt:        row.CreatedAt,
		AggregateVersion: row.AggregateVersion,
	}, nil
}

func (r *Repository) GetBalance(ctx context.Context, tenantID, walletID identifiers.UUID) (BalanceResponse, error) {
	wallet, err := r.GetWallet(ctx, tenantID, walletID)
	if err != nil {
		return BalanceResponse{}, err
	}
	bal, err := walletdb.New(r.pool).GetWalletBalanceWithVersion(ctx, walletdb.GetWalletBalanceWithVersionParams{
		TenantID:    tenantID,
		WalletID:    walletID,
		AggregateID: wallet.WalletID,
		Currency:    currencyUSD,
	})
	if err != nil {
		return BalanceResponse{}, err
	}
	return BalanceResponse{
		WalletID:      walletID,
		Balance:       Balance{Currency: money.USD, CurrentMinor: bal.PostedBalanceMinor, HeldMinor: bal.HeldBalanceMinor, AvailableMinor: int64(bal.AvailableBalanceMinor)},
		Authoritative: true,
		AsOfVersion:   bal.AsOfVersion,
	}, nil
}

type cursor struct {
	CreatedAt time.Time        `json:"created_at"`
	ID        identifiers.UUID `json:"id"`
}

func EncodeCursor(createdAt time.Time, id identifiers.UUID) string {
	payload, _ := json.Marshal(cursor{CreatedAt: createdAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func DecodeCursor(value string) (time.Time, identifiers.UUID, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, identifiers.UUID{}, fmt.Errorf("invalid cursor: %w", err)
	}
	var parsed cursor
	if err := json.Unmarshal(decoded, &parsed); err != nil || parsed.CreatedAt.IsZero() || parsed.ID.IsZero() {
		return time.Time{}, identifiers.UUID{}, errors.New("invalid cursor")
	}
	return parsed.CreatedAt, parsed.ID, nil
}

func (r *Repository) ListEntries(ctx context.Context, tenantID, walletID identifiers.UUID, value string, limit int32) (EntryPage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	wallet, err := r.GetWallet(ctx, tenantID, walletID)
	if err != nil {
		return EntryPage{}, err
	}
	q := walletdb.New(r.pool)
	var rows []walletEntryRow
	if strings.TrimSpace(value) == "" {
		resultRows, queryErr := q.ListWalletEntries(ctx, walletdb.ListWalletEntriesParams{TenantID: tenantID, AccountID: wallet.AccountID, Limit: limit})
		err = queryErr
		for _, row := range resultRows {
			rows = append(rows, walletEntryRow{TransactionID: row.TransactionID, OperationType: row.OperationType, ExternalReference: row.ExternalReference, TransactionCreatedAt: row.TransactionCreatedAt, Postings: row.Postings})
		}
	} else {
		createdAt, id, decodeErr := DecodeCursor(value)
		if decodeErr != nil {
			return EntryPage{}, decodeErr
		}
		resultRows, queryErr := q.ListWalletEntriesBefore(ctx, walletdb.ListWalletEntriesBeforeParams{TenantID: tenantID, AccountID: wallet.AccountID, Column3: createdAt, Column4: id, Limit: limit})
		err = queryErr
		for _, row := range resultRows {
			rows = append(rows, walletEntryRow{TransactionID: row.TransactionID, OperationType: row.OperationType, ExternalReference: row.ExternalReference, TransactionCreatedAt: row.TransactionCreatedAt, Postings: row.Postings})
		}
	}
	if err != nil {
		return EntryPage{}, err
	}
	entries := make([]JournalEntry, 0, len(rows))
	for _, row := range rows {
		var postings []EntryPosting
		if err := json.Unmarshal(row.Postings, &postings); err != nil {
			return EntryPage{}, err
		}
		entries = append(entries, JournalEntry{JournalTransactionID: row.TransactionID, OperationType: row.OperationType, ExternalReference: row.ExternalReference, CreatedAt: row.TransactionCreatedAt, Postings: postings})
	}
	page := EntryPage{Entries: entries}
	if len(entries) == int(limit) {
		last := entries[len(entries)-1]
		page.NextCursor = EncodeCursor(last.CreatedAt, last.JournalTransactionID)
	}
	return page, nil
}

type walletEntryRow struct {
	TransactionID        identifiers.UUID
	OperationType        string
	ExternalReference    *string
	TransactionCreatedAt time.Time
	Postings             []byte
}
