package saga

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"digitalwallet/services/transfer/internal/repository"
	"digitalwallet/services/transfer/internal/walletclient"
	"digitalwallet/shared/identifiers"
)

func getTestDB(t *testing.T) *sql.DB {
	dsn := os.Getenv("TRANSFER_TEST_MIGRATOR_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TRANSFER_TEST_DATABASE_URL")
	}
	if dsn == "" {
		dsn = "postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/transfer_test?sslmode=disable"
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("cannot open transfer test db: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("cannot ping transfer test db: %v", err)
	}
	return db
}

func nextID(ids identifiers.Generator) identifiers.UUID {
	id, _ := ids.Generate()
	return id
}

func createTestTransfer(t *testing.T, repo *repository.Repository, tenantID, sourceID, destID identifiers.UUID) repository.Transfer {
	ctx := context.Background()
	key := fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())
	hash := "test-hash-" + key
	in := repository.Transfer{
		SourceWalletID:      sourceID,
		DestinationWalletID: destID,
		AmountMinor:         5000,
		Currency:            "USD",
		Reference:           "test transfer",
	}
	tr, _, err := repo.Create(ctx, tenantID, key, hash, in)
	if err != nil {
		t.Fatalf("failed to create test transfer: %v", err)
	}
	return tr
}

func TestSagaHappyPath(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	sourceID, _ := ids.Generate()
	destID, _ := ids.Generate()
	repo := repository.New(db, ids)
	tr := createTestTransfer(t, repo, tenantID, sourceID, destID)

	var sourceCalls, destCalls []string
	var mu sync.Mutex

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sourceCalls = append(sourceCalls, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(walletclient.Response{
			OperationID: nextID(ids),
			Status:      "SUCCESS",
		})
	}))
	defer sourceServer.Close()

	destServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		destCalls = append(destCalls, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(walletclient.Response{
			OperationID: nextID(ids),
			Status:      "SUCCESS",
		})
	}))
	defer destServer.Close()

	srcClient := walletclient.New(sourceServer.URL, "token", sourceServer.Client())
	dstClient := walletclient.New(destServer.URL, "token", destServer.Client())

	s := New(repo, srcClient, dstClient, 3, 2*time.Second)

	err := s.Run(context.Background(), tenantID, tr.TransferID)
	if err != nil {
		t.Fatalf("saga run failed: %v", err)
	}

	res, err := repo.Get(context.Background(), tenantID, tr.TransferID)
	if err != nil {
		t.Fatalf("failed to get transfer: %v", err)
	}
	if res.State != "COMPLETED" {
		t.Fatalf("transfer state = %s, want COMPLETED", res.State)
	}

	steps, err := repo.Steps(context.Background(), tenantID, tr.TransferID)
	if err != nil {
		t.Fatalf("failed to get steps: %v", err)
	}
	completedSteps := 0
	for _, st := range steps {
		if st.State == "COMPLETED" {
			completedSteps++
		}
	}
	if completedSteps != 4 {
		t.Fatalf("completed steps = %d, want 4", completedSteps)
	}
}

func TestSagaReserveFailurePermanent(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	sourceID, _ := ids.Generate()
	destID, _ := ids.Generate()
	repo := repository.New(db, ids)
	tr := createTestTransfer(t, repo, tenantID, sourceID, destID)

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict) // 409 Insufficient funds
		_, _ = w.Write([]byte(`{"error":"insufficient funds"}`))
	}))
	defer sourceServer.Close()

	destCalled := false
	destServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destCalled = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer destServer.Close()

	srcClient := walletclient.New(sourceServer.URL, "token", sourceServer.Client())
	dstClient := walletclient.New(destServer.URL, "token", destServer.Client())

	s := New(repo, srcClient, dstClient, 2, 2*time.Second)

	err := s.Run(context.Background(), tenantID, tr.TransferID)
	if err == nil {
		t.Fatalf("expected error from failed reserve")
	}

	if destCalled {
		t.Fatalf("destination should not have been called after reserve failure")
	}

	res, err := repo.Get(context.Background(), tenantID, tr.TransferID)
	if err != nil {
		t.Fatalf("failed to get transfer: %v", err)
	}
	if res.State != "FAILED" {
		t.Fatalf("transfer state = %s, want FAILED", res.State)
	}
}

func TestSagaPendingCreditFailureCompensatesReserve(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	sourceID, _ := ids.Generate()
	destID, _ := ids.Generate()
	repo := repository.New(db, ids)
	tr := createTestTransfer(t, repo, tenantID, sourceID, destID)

	var released atomic.Bool

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/wallets/"+sourceID.String()+"/releases" {
			released.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(walletclient.Response{
			OperationID: nextID(ids),
			Status:      "SUCCESS",
		})
	}))
	defer sourceServer.Close()

	destServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // 404 Destination wallet not found
		_, _ = w.Write([]byte(`{"error":"destination wallet not found"}`))
	}))
	defer destServer.Close()

	srcClient := walletclient.New(sourceServer.URL, "token", sourceServer.Client())
	dstClient := walletclient.New(destServer.URL, "token", destServer.Client())

	s := New(repo, srcClient, dstClient, 2, 2*time.Second)

	err := s.Run(context.Background(), tenantID, tr.TransferID)
	if err != nil {
		t.Fatalf("compensation should succeed cleanly, got: %v", err)
	}

	if !released.Load() {
		t.Fatalf("expected release to be called on source wallet")
	}

	res, err := repo.Get(context.Background(), tenantID, tr.TransferID)
	if err != nil {
		t.Fatalf("failed to get transfer: %v", err)
	}
	if res.State != "COMPENSATED" {
		t.Fatalf("transfer state = %s, want COMPENSATED", res.State)
	}
}

func TestSagaFinalizeFailureCompensatesCapture(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	sourceID, _ := ids.Generate()
	destID, _ := ids.Generate()
	repo := repository.New(db, ids)
	tr := createTestTransfer(t, repo, tenantID, sourceID, destID)

	var compensated atomic.Bool

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/wallets/"+sourceID.String()+"/compensations" {
			compensated.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(walletclient.Response{
			OperationID: nextID(ids),
			Status:      "SUCCESS",
		})
	}))
	defer sourceServer.Close()

	destServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/wallets/"+destID.String()+"/finalizations" {
			w.WriteHeader(http.StatusUnprocessableEntity) // 422 Permanent error
			_, _ = w.Write([]byte(`{"error":"cannot finalize"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(walletclient.Response{
			OperationID: nextID(ids),
			Status:      "SUCCESS",
		})
	}))
	defer destServer.Close()

	srcClient := walletclient.New(sourceServer.URL, "token", sourceServer.Client())
	dstClient := walletclient.New(destServer.URL, "token", destServer.Client())

	s := New(repo, srcClient, dstClient, 2, 2*time.Second)

	err := s.Run(context.Background(), tenantID, tr.TransferID)
	if err != nil {
		t.Fatalf("compensate capture should succeed cleanly, got: %v", err)
	}

	if !compensated.Load() {
		t.Fatalf("expected compensation to be called on source wallet")
	}

	res, err := repo.Get(context.Background(), tenantID, tr.TransferID)
	if err != nil {
		t.Fatalf("failed to get transfer: %v", err)
	}
	if res.State != "COMPENSATED" {
		t.Fatalf("transfer state = %s, want COMPENSATED", res.State)
	}
}

func TestSagaAmbiguousFailureMarksReconciliationRequired(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	sourceID, _ := ids.Generate()
	destID, _ := ids.Generate()
	repo := repository.New(db, ids)
	tr := createTestTransfer(t, repo, tenantID, sourceID, destID)

	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/wallets/"+sourceID.String()+"/captures" {
			// Simulate network partition/timeout by closing connection
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(walletclient.Response{
			OperationID: nextID(ids),
			Status:      "SUCCESS",
		})
	}))
	defer sourceServer.Close()

	destServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(walletclient.Response{
			OperationID: nextID(ids),
			Status:      "SUCCESS",
		})
	}))
	defer destServer.Close()

	srcClient := walletclient.New(sourceServer.URL, "token", sourceServer.Client())
	dstClient := walletclient.New(destServer.URL, "token", destServer.Client())

	s := New(repo, srcClient, dstClient, 1, 200*time.Millisecond)

	err := s.Run(context.Background(), tenantID, tr.TransferID)
	if err == nil {
		t.Fatalf("expected error on ambiguous failure")
	}

	res, err := repo.Get(context.Background(), tenantID, tr.TransferID)
	if err != nil {
		t.Fatalf("failed to get transfer: %v", err)
	}
	if res.State != "RECONCILIATION_REQUIRED" {
		t.Fatalf("transfer state = %s, want RECONCILIATION_REQUIRED", res.State)
	}
}

func TestSagaIdempotentCompletion(t *testing.T) {
	db := getTestDB(t)
	defer db.Close()

	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	sourceID, _ := ids.Generate()
	destID, _ := ids.Generate()
	repo := repository.New(db, ids)
	tr := createTestTransfer(t, repo, tenantID, sourceID, destID)

	_ = repo.SetState(context.Background(), tenantID, tr.TransferID, "COMPLETED")

	serverCalled := false
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalled = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sourceServer.Close()

	srcClient := walletclient.New(sourceServer.URL, "token", sourceServer.Client())
	dstClient := walletclient.New(sourceServer.URL, "token", sourceServer.Client())

	s := New(repo, srcClient, dstClient, 1, time.Second)

	err := s.Run(context.Background(), tenantID, tr.TransferID)
	if err != nil {
		t.Fatalf("expected nil for already completed transfer, got %v", err)
	}
	if serverCalled {
		t.Fatalf("server should not have been called for completed transfer")
	}
}
