package integration

import (
	"testing"

	"digitalwallet/services/wallet/internal/repository"
	"digitalwallet/shared/identifiers"
)

func TestWalletReserveAndRelease(t *testing.T) {
	if testRepo == nil {
		t.Skip("wallet test repo not initialized")
	}
	ctx := contextForTest(t)
	tenantID := tenant(t)

	// Create wallet
	w, _ := createWallet(t, tenantID, "user-reserve")

	// Deposit $100
	req := moneyRequest(10000, "USD")
	hash, _ := repository.HashRequest(req)
	_, err := testRepo.Deposit(ctx, tenantID, w.WalletID, "dep-1", hash, 10000, "USD")
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}

	bal, err := testRepo.GetBalance(ctx, tenantID, w.WalletID)
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if bal.Balance.CurrentMinor != 10000 || bal.Balance.AvailableMinor != 10000 || bal.Balance.HeldMinor != 0 {
		t.Fatalf("initial balance = %+v", bal.Balance)
	}

	// Reserve $40
	transferID, _ := identifiers.NewUUIDv7()
	opID, _ := identifiers.NewUUIDv7()
	res, err := testRepo.Reserve(ctx, tenantID, w.WalletID, opID, repository.TransferMoneyCommand{
		TransferID:  transferID,
		AmountMinor: 4000,
		Currency:    "USD",
	}, "key-res")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("reserve status code = %d", res.StatusCode)
	}

	bal, err = testRepo.GetBalance(ctx, tenantID, w.WalletID)
	if err != nil {
		t.Fatalf("get balance after reserve: %v", err)
	}
	if bal.Balance.CurrentMinor != 10000 || bal.Balance.AvailableMinor != 6000 || bal.Balance.HeldMinor != 4000 {
		t.Fatalf("balance after reserve = %+v (expected posted=10000, available=6000, held=4000)", bal.Balance)
	}

	// Replay reserve idempotently
	replayed, err := testRepo.Reserve(ctx, tenantID, w.WalletID, opID, repository.TransferMoneyCommand{
		TransferID:  transferID,
		AmountMinor: 4000,
		Currency:    "USD",
	}, "key-res")
	if err != nil {
		t.Fatalf("replayed reserve: %v", err)
	}
	if !replayed.Replayed {
		t.Fatalf("expected replayed = true")
	}

	// Release reservation
	relOpID, _ := identifiers.NewUUIDv7()
	relRes, err := testRepo.Release(ctx, tenantID, w.WalletID, relOpID, repository.TransferMoneyCommand{
		TransferID:        transferID,
		AmountMinor:       4000,
		Currency:          "USD",
		TargetOperationID: opID,
	}, "key-rel")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if relRes.StatusCode != 200 {
		t.Fatalf("release status code = %d", relRes.StatusCode)
	}

	bal, err = testRepo.GetBalance(ctx, tenantID, w.WalletID)
	if err != nil {
		t.Fatalf("get balance after release: %v", err)
	}
	if bal.Balance.CurrentMinor != 10000 || bal.Balance.AvailableMinor != 10000 || bal.Balance.HeldMinor != 0 {
		t.Fatalf("balance after release = %+v (expected posted=10000, available=10000, held=0)", bal.Balance)
	}
}

func TestWalletReserveCaptureAndCompensate(t *testing.T) {
	if testRepo == nil {
		t.Skip("wallet test repo not initialized")
	}
	ctx := contextForTest(t)
	tenantID := tenant(t)

	// Create wallet
	w, _ := createWallet(t, tenantID, "user-cap")

	// Deposit $100
	req := moneyRequest(10000, "USD")
	hash, _ := repository.HashRequest(req)
	_, err := testRepo.Deposit(ctx, tenantID, w.WalletID, "dep-1", hash, 10000, "USD")
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}

	// Reserve $40
	transferID, _ := identifiers.NewUUIDv7()
	resOpID, _ := identifiers.NewUUIDv7()
	_, err = testRepo.Reserve(ctx, tenantID, w.WalletID, resOpID, repository.TransferMoneyCommand{
		TransferID:  transferID,
		AmountMinor: 4000,
		Currency:    "USD",
	}, "key-res-cap")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	// Capture $40
	capOpID, _ := identifiers.NewUUIDv7()
	capRes, err := testRepo.Capture(ctx, tenantID, w.WalletID, capOpID, repository.TransferMoneyCommand{
		TransferID:        transferID,
		AmountMinor:       4000,
		Currency:          "USD",
		TargetOperationID: resOpID,
	}, "key-cap")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if capRes.StatusCode != 200 {
		t.Fatalf("capture status = %d", capRes.StatusCode)
	}

	bal, err := testRepo.GetBalance(ctx, tenantID, w.WalletID)
	if err != nil {
		t.Fatalf("get balance after capture: %v", err)
	}
	if bal.Balance.CurrentMinor != 6000 || bal.Balance.AvailableMinor != 6000 || bal.Balance.HeldMinor != 0 {
		t.Fatalf("balance after capture = %+v (expected posted=6000, available=6000, held=0)", bal.Balance)
	}

	// Compensate $40 (reversal of captured transaction)
	compOpID, _ := identifiers.NewUUIDv7()
	compRes, err := testRepo.Compensate(ctx, tenantID, w.WalletID, compOpID, repository.TransferMoneyCommand{
		TransferID:        transferID,
		AmountMinor:       4000,
		Currency:          "USD",
		TargetOperationID: capOpID,
	}, "key-comp")
	if err != nil {
		t.Fatalf("compensate: %v", err)
	}
	if compRes.StatusCode != 200 {
		t.Fatalf("compensate status = %d", compRes.StatusCode)
	}

	bal, err = testRepo.GetBalance(ctx, tenantID, w.WalletID)
	if err != nil {
		t.Fatalf("get balance after compensate: %v", err)
	}
	if bal.Balance.CurrentMinor != 10000 || bal.Balance.AvailableMinor != 10000 || bal.Balance.HeldMinor != 0 {
		t.Fatalf("balance after compensate = %+v (expected posted=10000, available=10000, held=0)", bal.Balance)
	}
}

func TestWalletPendingCreditAndFinalize(t *testing.T) {
	if testRepo == nil {
		t.Skip("wallet test repo not initialized")
	}
	ctx := contextForTest(t)
	tenantID := tenant(t)

	// Create destination wallet
	w, _ := createWallet(t, tenantID, "user-pending")

	// Create pending credit of $40
	transferID, _ := identifiers.NewUUIDv7()
	pendOpID, _ := identifiers.NewUUIDv7()
	pendRes, err := testRepo.CreatePendingCredit(ctx, tenantID, w.WalletID, pendOpID, repository.TransferMoneyCommand{
		TransferID:  transferID,
		AmountMinor: 4000,
		Currency:    "USD",
	}, "key-pend")
	if err != nil {
		t.Fatalf("pending credit: %v", err)
	}
	if pendRes.StatusCode != 200 {
		t.Fatalf("pending credit status = %d", pendRes.StatusCode)
	}

	// Pending credit must NOT be spendable yet
	bal, err := testRepo.GetBalance(ctx, tenantID, w.WalletID)
	if err != nil {
		t.Fatalf("get balance after pending: %v", err)
	}
	if bal.Balance.CurrentMinor != 0 || bal.Balance.AvailableMinor != 0 {
		t.Fatalf("balance after pending credit = %+v (expected posted=0, available=0)", bal.Balance)
	}

	// Finalize $40
	finOpID, _ := identifiers.NewUUIDv7()
	finRes, err := testRepo.Finalize(ctx, tenantID, w.WalletID, finOpID, repository.TransferMoneyCommand{
		TransferID:        transferID,
		AmountMinor:       4000,
		Currency:          "USD",
		TargetOperationID: pendOpID,
	}, "key-fin")
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if finRes.StatusCode != 200 {
		t.Fatalf("finalize status = %d", finRes.StatusCode)
	}

	// Finalized funds are now posted and available
	bal, err = testRepo.GetBalance(ctx, tenantID, w.WalletID)
	if err != nil {
		t.Fatalf("get balance after finalize: %v", err)
	}
	if bal.Balance.CurrentMinor != 4000 || bal.Balance.AvailableMinor != 4000 || bal.Balance.HeldMinor != 0 {
		t.Fatalf("balance after finalize = %+v (expected posted=4000, available=4000, held=0)", bal.Balance)
	}
}
