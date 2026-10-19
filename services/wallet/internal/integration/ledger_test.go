package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"digitalwallet/services/wallet/internal/repository"
	"digitalwallet/shared/identifiers"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	testPool *pgxpool.Pool
	testRepo *repository.Repository
)

func TestMain(main *testing.M) {
	dsn := os.Getenv("WALLET_TEST_DATABASE_URL")
	if dsn == "" {
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		panic(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		panic(err)
	}
	testPool = pool
	testRepo = repository.New(pool, identifiers.NewUUIDv7Generator(), "wallet-source")
	code := main.Run()
	pool.Close()
	os.Exit(code)
}

func contextForTest(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func tenant(t *testing.T) identifiers.UUID {
	t.Helper()
	id, err := identifiers.NewUUIDv7()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func createWallet(t *testing.T, tenantID identifiers.UUID, key string) (repository.Wallet, repository.CommandResult) {
	t.Helper()
	request := struct {
		ExternalReference string `json:"external_reference"`
	}{ExternalReference: key}
	hash, err := repository.HashRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := testRepo.CreateWallet(contextForTest(t), tenantID, key, hash, key)
	if err != nil {
		t.Fatal(err)
	}
	var wallet repository.Wallet
	if err := json.Unmarshal(result.Body, &wallet); err != nil {
		t.Fatal(err)
	}
	return wallet, result
}

func moneyRequest(amount int64, currency string) struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
} {
	return struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
	}{AmountMinor: amount, Currency: currency}
}

func hashMoney(t *testing.T, amount int64) string {
	t.Helper()
	hash, err := repository.HashRequest(moneyRequest(amount, "USD"))
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestDuplicateDepositReturnsStoredResponse(t *testing.T) {
	tenantID := tenant(t)
	wallet, _ := createWallet(t, tenantID, "create-duplicate-deposit")
	first, err := testRepo.Deposit(contextForTest(t), tenantID, wallet.WalletID, "deposit-1", hashMoney(t, 125), 125, "deposit")
	if err != nil {
		t.Fatal(err)
	}
	second, err := testRepo.Deposit(contextForTest(t), tenantID, wallet.WalletID, "deposit-1", hashMoney(t, 125), 125, "deposit")
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Body) != string(second.Body) || !second.Replayed {
		t.Fatalf("duplicate result = %+v, first body %s, second body %s", second, first.Body, second.Body)
	}
	balance, err := testRepo.GetBalance(contextForTest(t), tenantID, wallet.WalletID)
	if err != nil || balance.Balance.CurrentMinor != 125 {
		t.Fatalf("balance = %+v, error = %v", balance, err)
	}
}

func TestIdempotencyPayloadConflictReturnsError(t *testing.T) {
	tenantID := tenant(t)
	wallet, _ := createWallet(t, tenantID, "create-idempotency-conflict")
	_, err := testRepo.Deposit(contextForTest(t), tenantID, wallet.WalletID, "same-key", hashMoney(t, 10), 10, "first")
	if err != nil {
		t.Fatal(err)
	}
	_, err = testRepo.Deposit(contextForTest(t), tenantID, wallet.WalletID, "same-key", hashMoney(t, 11), 11, "second")
	if !errors.Is(err, repository.ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestConcurrentWithdrawalsNeverOverspend(t *testing.T) {
	tenantID := tenant(t)
	wallet, _ := createWallet(t, tenantID, "create-concurrent-withdrawals")
	if _, err := testRepo.Deposit(contextForTest(t), tenantID, wallet.WalletID, "seed", hashMoney(t, 100), 100, "seed"); err != nil {
		t.Fatal(err)
	}
	var successful atomic.Int64
	var wait sync.WaitGroup
	withdrawHash := hashMoney(t, 1)
	for index := 0; index < 128; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := testRepo.Withdraw(context.Background(), tenantID, wallet.WalletID, fmt.Sprintf("withdraw-%d", index), withdrawHash, 1, "concurrent")
			if err == nil {
				successful.Add(1)
				return
			}
			if !errors.Is(err, repository.ErrInsufficientFunds) {
				t.Errorf("withdrawal %d error = %v", index, err)
			}
		}()
	}
	wait.Wait()
	if successful.Load() != 100 {
		t.Fatalf("successful withdrawals = %d, want 100", successful.Load())
	}
	balance, err := testRepo.GetBalance(contextForTest(t), tenantID, wallet.WalletID)
	if err != nil || balance.Balance.AvailableMinor != 0 {
		t.Fatalf("final balance = %+v, error = %v", balance, err)
	}
}

func TestConcurrentSameIdempotencyKeyHasOneEffect(t *testing.T) {
	tenantID := tenant(t)
	wallet, _ := createWallet(t, tenantID, "create-concurrent-idempotency")
	hash := hashMoney(t, 7)
	results := make(chan repository.CommandResult, 32)
	errorsCh := make(chan error, 32)
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := testRepo.Deposit(context.Background(), tenantID, wallet.WalletID, "same-concurrent-key", hash, 7, "same")
			if err != nil {
				errorsCh <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("concurrent idempotency error = %v", err)
	}
	var bodies []string
	for result := range results {
		bodies = append(bodies, string(result.Body))
	}
	if len(bodies) != 32 {
		t.Fatalf("results = %d, want 32", len(bodies))
	}
	for _, body := range bodies[1:] {
		if body != bodies[0] {
			t.Fatalf("stored responses differ: %s vs %s", bodies[0], body)
		}
	}
	balance, err := testRepo.GetBalance(contextForTest(t), tenantID, wallet.WalletID)
	if err != nil || balance.Balance.CurrentMinor != 7 {
		t.Fatalf("balance = %+v, error = %v", balance, err)
	}
}

func TestJournalImmutabilityTriggers(t *testing.T) {
	tenantID := tenant(t)
	wallet, _ := createWallet(t, tenantID, "create-immutability")
	result, err := testRepo.Deposit(contextForTest(t), tenantID, wallet.WalletID, "immutable-deposit", hashMoney(t, 5), 5, "immutable")
	if err != nil {
		t.Fatal(err)
	}
	var response repository.JournalCommandResponse
	if err := json.Unmarshal(result.Body, &response); err != nil {
		t.Fatal(err)
	}
	checks := []string{
		"UPDATE journal_transactions SET operation_type = 'CHANGED' WHERE tenant_id = $1 AND id = $2",
		"DELETE FROM journal_postings WHERE tenant_id = $1 AND transaction_id = $2",
		"TRUNCATE journal_transactions",
		"TRUNCATE journal_postings",
		"UPDATE events SET event_type = 'changed' WHERE tenant_id = $1 AND event_id = $2",
		"DELETE FROM events WHERE tenant_id = $1 AND event_id = $2",
		"TRUNCATE events",
	}
	for _, query := range checks {
		var err error
		if stringsContains(query, "journal_transactions") && stringsContains(query, "id = $2") {
			err = execWithArgs(query, tenantID, response.JournalTransactionID)
		} else if stringsContains(query, "journal_postings") && stringsContains(query, "transaction_id") {
			err = execWithArgs(query, tenantID, response.JournalTransactionID)
		} else if stringsContains(query, "events") && stringsContains(query, "event_id") {
			err = execWithArgs(query, tenantID, response.EventID)
		} else {
			_, err = testPool.Exec(context.Background(), query)
		}
		if err == nil {
			t.Fatalf("immutability query unexpectedly succeeded: %s", query)
		}
	}
}

func TestUnbalancedTransactionRejectedAtCommit(t *testing.T) {
	tenantID := tenant(t)
	wallet, _ := createWallet(t, tenantID, "create-unbalanced")
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	transactionID, _ := identifiers.NewUUIDv7()
	postingID, _ := identifiers.NewUUIDv7()
	_, err = tx.Exec(context.Background(), `INSERT INTO journal_transactions (tenant_id, id, operation_type, created_at) VALUES ($1, $2, 'TEST', now())`, tenantID, transactionID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(context.Background(), `INSERT INTO journal_postings (tenant_id, id, transaction_id, account_id, side, amount_minor, currency, entry_type, created_at) VALUES ($1, $2, $3, $4, 'CREDIT', 1, 'USD', 'wallet', now())`, tenantID, postingID, transactionID, wallet.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err == nil {
		t.Fatal("unbalanced transaction committed")
	}
}

func TestCrossTenantForeignKeyAndReplay(t *testing.T) {
	firstTenant := tenant(t)
	secondTenant := tenant(t)
	firstWallet, _ := createWallet(t, firstTenant, "create-cross-tenant-one")
	secondWallet, _ := createWallet(t, secondTenant, "create-cross-tenant-two")
	if _, err := testRepo.Deposit(context.Background(), firstTenant, firstWallet.WalletID, "cross-seed", hashMoney(t, 13), 13, "seed"); err != nil {
		t.Fatal(err)
	}
	transactionID, _ := identifiers.NewUUIDv7()
	postingID, _ := identifiers.NewUUIDv7()
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(context.Background(), `INSERT INTO journal_transactions (tenant_id, id, operation_type, created_at) VALUES ($1, $2, 'CROSS', now())`, secondTenant, transactionID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(context.Background(), `INSERT INTO journal_postings (tenant_id, id, transaction_id, account_id, side, amount_minor, currency, entry_type, created_at) VALUES ($1, $2, $3, $4, 'CREDIT', 1, 'USD', 'wallet', now())`, secondTenant, postingID, transactionID, firstWallet.AccountID)
	if err == nil {
		t.Fatal("cross-tenant posting unexpectedly succeeded")
	}
	_ = tx.Rollback(context.Background())
	if _, err := testRepo.GetBalance(context.Background(), firstTenant, secondWallet.WalletID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("cross-tenant API-equivalent read error = %v", err)
	}
	balance, err := testRepo.GetBalance(context.Background(), firstTenant, firstWallet.WalletID)
	if err != nil || balance.Balance.CurrentMinor != 13 {
		t.Fatalf("replayed balance = %+v, error = %v", balance, err)
	}
	var raw int64
	err = testPool.QueryRow(context.Background(), `SELECT COALESCE(SUM(CASE WHEN side = 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0) FROM journal_postings WHERE tenant_id = $1 AND account_id = $2 AND currency = 'USD'`, firstTenant, firstWallet.AccountID).Scan(&raw)
	if err != nil || raw != balance.Balance.CurrentMinor {
		t.Fatalf("raw replay = %d, API = %d, error = %v", raw, balance.Balance.CurrentMinor, err)
	}
}

func TestEveryMutationHasEventAndOutboxVersion(t *testing.T) {
	tenantID := tenant(t)
	wallet, _ := createWallet(t, tenantID, "create-events")
	for index := int64(1); index <= 3; index++ {
		if _, err := testRepo.Deposit(context.Background(), tenantID, wallet.WalletID, fmt.Sprintf("event-deposit-%d", index), hashMoney(t, index), index, "event"); err != nil {
			t.Fatal(err)
		}
	}
	var eventCount, outboxCount int
	if err := testPool.QueryRow(context.Background(), `SELECT COUNT(*) FROM events WHERE tenant_id = $1 AND aggregate_id = $2`, tenantID, wallet.WalletID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT COUNT(*) FROM outbox_messages WHERE tenant_id = $1`, tenantID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 4 || outboxCount != 4 {
		t.Fatalf("events=%d outbox=%d, want 4 each", eventCount, outboxCount)
	}
	rows, err := testPool.Query(context.Background(), `SELECT aggregate_version FROM events WHERE tenant_id = $1 AND aggregate_id = $2 ORDER BY aggregate_version`, tenantID, wallet.WalletID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	version := int64(1)
	for rows.Next() {
		var got int64
		if err := rows.Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != version {
			t.Fatalf("event version=%d, want %d", got, version)
		}
		version++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRandomDepositsWithdrawalsAndRetriesConserveMoney(t *testing.T) {
	tenantID := tenant(t)
	wallet, _ := createWallet(t, tenantID, "create-property")
	random := rand.New(rand.NewSource(42))
	var model int64
	for step := 0; step < 150; step++ {
		amount := int64(random.Intn(25) + 1)
		key := fmt.Sprintf("property-%d", step)
		hash := hashMoney(t, amount)
		if random.Intn(3) == 0 && model >= amount {
			if _, err := testRepo.Withdraw(context.Background(), tenantID, wallet.WalletID, key, hash, amount, "property"); err != nil {
				t.Fatal(err)
			}
			model -= amount
		} else {
			first, err := testRepo.Deposit(context.Background(), tenantID, wallet.WalletID, key, hash, amount, "property")
			if err != nil {
				t.Fatal(err)
			}
			second, err := testRepo.Deposit(context.Background(), tenantID, wallet.WalletID, key, hash, amount, "property")
			if err != nil || !second.Replayed || string(first.Body) != string(second.Body) {
				t.Fatalf("duplicate property command = %+v, error = %v", second, err)
			}
			model += amount
		}
		balance, err := testRepo.GetBalance(context.Background(), tenantID, wallet.WalletID)
		if err != nil {
			t.Fatal(err)
		}
		if balance.Balance.AvailableMinor != model || balance.Balance.AvailableMinor < 0 {
			t.Fatalf("step %d balance=%d model=%d", step, balance.Balance.AvailableMinor, model)
		}
	}
}

func execWithArgs(query string, tenantID, id identifiers.UUID) error {
	_, err := testPool.Exec(context.Background(), query, tenantID, id)
	return err
}

func stringsContains(value, substring string) bool {
	return len(value) >= len(substring) && (value == substring || containsAt(value, substring))
}

func containsAt(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
