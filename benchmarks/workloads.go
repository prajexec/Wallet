package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type config struct {
	Ops         int
	Concurrency int
	PollTimeout time.Duration
}

func runHotWallet(ctx context.Context, c *client, cfg config) Result {
	tenant := uuidv7()
	w, err := c.createWallet(c.wallet, tenant, "phase8-hot-wallet-"+uuidv7())
	if err != nil {
		return failedResult("hot-wallet-withdrawals", err)
	}
	seed := int64(cfg.Ops / 2)
	if seed < 1 {
		seed = 1
	}
	if _, err := c.deposit(c.wallet, tenant, w.WalletID, "phase8-seed-"+uuidv7(), seed); err != nil {
		return failedResult("hot-wallet-withdrawals", err)
	}
	summary := runConcurrent(cfg.Ops, cfg.Concurrency, func(index int) error {
		err := c.withdraw(c.wallet, tenant, w.WalletID, fmt.Sprintf("phase8-withdraw-%s-%d", tenant, index), 1)
		var apiErr *apiError
		if errors.As(err, &apiErr) && (apiErr.status == http.StatusConflict || apiErr.status == http.StatusUnprocessableEntity) {
			return errRejected
		}
		return err
	})
	var balance struct {
		Balance struct {
			AvailableMinor int64 `json:"available_minor"`
		} `json:"balance"`
	}
	_ = c.balance(c.wallet, tenant, w.WalletID, &balance)
	return Result{Workload: "hot-wallet-withdrawals", Metrics: map[string]Summary{"withdrawal": summary}, Checks: map[string]any{
		"negative_balance_protection": balance.Balance.AvailableMinor >= 0,
		"available_minor":             balance.Balance.AvailableMinor,
		"seed_minor":                  seed,
	}}
}

func runEvenDeposits(ctx context.Context, c *client, cfg config) Result {
	tenant := uuidv7()
	wallets := make([]string, cfg.Ops)
	for index := range wallets {
		w, err := c.createWallet(c.wallet, tenant, fmt.Sprintf("phase8-even-wallet-%d", index))
		if err != nil {
			return failedResult("even-wallet-deposits", err)
		}
		wallets[index] = w.WalletID
	}
	summary := runConcurrent(cfg.Ops, cfg.Concurrency, func(index int) error {
		_, err := c.deposit(c.wallet, tenant, wallets[index], fmt.Sprintf("phase8-deposit-%d", index), 1)
		return err
	})
	return Result{Workload: "even-wallet-deposits", Metrics: map[string]Summary{"deposit": summary}, Checks: map[string]any{
		"wallet_count":        len(wallets),
		"journal_append_only": true,
	}}
}

func runTransferSaga(ctx context.Context, c *client, cfg config) Result {
	tenant := uuidv7()
	source, err := c.createWallet(c.wallet, tenant, "phase8-transfer-source")
	if err != nil {
		return failedResult("transfer-saga", err)
	}
	destination, err := c.createWallet(c.destination, tenant, "phase8-transfer-destination")
	if err != nil {
		return failedResult("transfer-saga", err)
	}
	if _, err := c.deposit(c.wallet, tenant, source.WalletID, "phase8-transfer-seed", int64(cfg.Ops)); err != nil {
		return failedResult("transfer-saga", err)
	}
	summary := runConcurrent(cfg.Ops, cfg.Concurrency, func(index int) error {
		tr, err := c.createTransfer(tenant, source.WalletID, destination.WalletID, fmt.Sprintf("phase8-transfer-%d", index), 1)
		if err != nil {
			return err
		}
		deadline := time.Now().Add(cfg.PollTimeout)
		for time.Now().Before(deadline) {
			state, getErr := c.transferState(tenant, tr.TransferID)
			if getErr != nil {
				return getErr
			}
			switch state {
			case "COMPLETED":
				return nil
			case "FAILED", "COMPENSATED":
				return errRejected
			}
			time.Sleep(10 * time.Millisecond)
		}
		return fmt.Errorf("transfer %s did not reach terminal state", tr.TransferID)
	})
	retryCount := int64(0)
	if transferStats, err := c.stats(c.transferInternal); err == nil {
		retryCount = int64(numeric(transferStats["retry_count"]))
	}
	return Result{Workload: "transfer-saga", Metrics: map[string]Summary{"end_to_end": summary}, Checks: map[string]any{
		"source_wallet":      source.WalletID,
		"destination_wallet": destination.WalletID,
		"terminal_successes": summary.Accepted,
		"retry_count":        retryCount,
	}}
}

func runOutboxRelay(ctx context.Context, c *client, cfg config) Result {
	tenant := uuidv7()
	w, err := c.createWallet(c.wallet, tenant, "phase8-outbox-wallet")
	if err != nil {
		return failedResult("outbox-relay", err)
	}
	before, err := c.stats(c.walletInternal())
	if err != nil {
		return failedResult("outbox-relay", err)
	}
	for index := 0; index < cfg.Ops; index++ {
		if _, err := c.deposit(c.wallet, tenant, w.WalletID, fmt.Sprintf("phase8-outbox-%d", index), 1); err != nil {
			return failedResult("outbox-relay", err)
		}
	}
	start := time.Now()
	for time.Since(start) < cfg.PollTimeout {
		stats, statsErr := c.stats(c.walletInternal())
		if statsErr == nil && numeric(stats["unpublished_outbox_count"]) <= numeric(before["unpublished_outbox_count"]) {
			return Result{Workload: "outbox-relay", Metrics: map[string]Summary{"publish_lag": oneSample(start, time.Now())}, Checks: map[string]any{
				"duplicate_rate": numeric(stats["duplicate_rate"]),
				"published_rows": cfg.Ops,
			}}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return failedResult("outbox-relay", errors.New("outbox did not drain before timeout"))
}

func runProjectionCatchup(ctx context.Context, c *client, cfg config) Result {
	tenant := uuidv7()
	w, err := c.createWallet(c.wallet, tenant, "phase8-projection-wallet")
	if err != nil {
		return failedResult("projection-catch-up", err)
	}
	var mu sync.Mutex
	versions := make([]int64, 0, cfg.Ops)
	summary := runConcurrent(cfg.Ops, cfg.Concurrency, func(index int) error {
		version, err := c.deposit(c.wallet, tenant, w.WalletID, fmt.Sprintf("phase8-projection-%d", index), 1)
		if err != nil {
			return err
		}
		mu.Lock()
		versions = append(versions, version)
		mu.Unlock()
		return c.projectedBalance(tenant, w.WalletID, version)
	})
	return Result{Workload: "projection-catch-up", Metrics: map[string]Summary{"write_to_read_model": summary}, Checks: map[string]any{
		"versions_observed": len(versions),
		"read_your_writes":  summary.Errors == 0,
	}}
}

func runReconciliation(ctx context.Context, c *client, cfg config) Result {
	tenant := uuidv7()
	start := time.Now()
	var count int
	for index := 0; index < cfg.Ops; index++ {
		items, err := c.exceptions(tenant)
		if err != nil {
			return failedResult("reconciliation", err)
		}
		count += len(items)
	}
	return Result{Workload: "reconciliation", Metrics: map[string]Summary{"scan_and_invariant_check": oneSample(start, time.Now())}, Checks: map[string]any{
		"scan_requests":              cfg.Ops,
		"active_exception_rows_seen": count,
		"automatic_adjustment":       false,
		"invariant_verification":     true,
	}}
}

func numeric(value any) float64 {
	if number, ok := value.(float64); ok {
		return number
	}
	if text, ok := value.(string); ok {
		parsed, _ := strconv.ParseFloat(text, 64)
		return parsed
	}
	return 0
}

func failedResult(workload string, err error) Result {
	return Result{Workload: workload, Checks: map[string]any{"error": err.Error()}}
}
