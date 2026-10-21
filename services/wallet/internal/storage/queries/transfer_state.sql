-- name: GetWalletOperation :one
SELECT tenant_id, operation_id, wallet_id, transfer_id, operation_kind,
       target_operation_id, request_hash, amount_minor, currency, status,
       journal_transaction_id, event_id, created_at, updated_at
FROM wallet_operations
WHERE tenant_id = $1 AND operation_id = $2;

-- name: InsertWalletOperation :one
INSERT INTO wallet_operations (
    tenant_id, operation_id, wallet_id, transfer_id, operation_kind,
    target_operation_id, request_hash, amount_minor, currency, status,
    journal_transaction_id, event_id, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'PENDING', NULL, NULL, $10, $10)
RETURNING tenant_id, operation_id, wallet_id, transfer_id, operation_kind,
          target_operation_id, request_hash, amount_minor, currency, status,
          journal_transaction_id, event_id, created_at, updated_at;

-- name: CompleteWalletOperation :one
UPDATE wallet_operations
SET status = 'COMPLETED', journal_transaction_id = $3, event_id = $4, updated_at = $5
WHERE tenant_id = $1 AND operation_id = $2
RETURNING tenant_id, operation_id, wallet_id, transfer_id, operation_kind,
          target_operation_id, request_hash, amount_minor, currency, status,
          journal_transaction_id, event_id, created_at, updated_at;

-- name: GetHold :one
SELECT tenant_id, hold_operation_id, wallet_id, transfer_id, amount_minor,
       currency, status, created_at, updated_at
FROM wallet_holds
WHERE tenant_id = $1 AND hold_operation_id = $2;

-- name: GetHoldByTransfer :one
SELECT tenant_id, hold_operation_id, wallet_id, transfer_id, amount_minor,
       currency, status, created_at, updated_at
FROM wallet_holds
WHERE tenant_id = $1 AND transfer_id = $2 AND wallet_id = $3;

-- name: InsertHold :one
INSERT INTO wallet_holds (
    tenant_id, hold_operation_id, wallet_id, transfer_id, amount_minor,
    currency, status, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, 'RESERVED', $7, $7)
RETURNING tenant_id, hold_operation_id, wallet_id, transfer_id, amount_minor,
          currency, status, created_at, updated_at;

-- name: TransitionHold :one
UPDATE wallet_holds
SET status = $3, updated_at = $4
WHERE tenant_id = $1 AND hold_operation_id = $2 AND status = $5
RETURNING tenant_id, hold_operation_id, wallet_id, transfer_id, amount_minor,
          currency, status, created_at, updated_at;

-- name: GetActiveHoldAmount :one
SELECT COALESCE(SUM(amount_minor), 0)::bigint
FROM wallet_holds
WHERE tenant_id = $1 AND wallet_id = $2 AND currency = $3 AND status = 'RESERVED';

-- name: GetPendingCredit :one
SELECT tenant_id, pending_operation_id, wallet_id, transfer_id, amount_minor,
       currency, status, created_at, updated_at
FROM wallet_pending_credits
WHERE tenant_id = $1 AND pending_operation_id = $2;

-- name: GetPendingCreditByTransfer :one
SELECT tenant_id, pending_operation_id, wallet_id, transfer_id, amount_minor,
       currency, status, created_at, updated_at
FROM wallet_pending_credits
WHERE tenant_id = $1 AND transfer_id = $2 AND wallet_id = $3;

-- name: InsertPendingCredit :one
INSERT INTO wallet_pending_credits (
    tenant_id, pending_operation_id, wallet_id, transfer_id, amount_minor,
    currency, status, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, 'PENDING', $7, $7)
RETURNING tenant_id, pending_operation_id, wallet_id, transfer_id, amount_minor,
          currency, status, created_at, updated_at;

-- name: TransitionPendingCredit :one
UPDATE wallet_pending_credits
SET status = $3, updated_at = $4
WHERE tenant_id = $1 AND pending_operation_id = $2 AND status = $5
RETURNING tenant_id, pending_operation_id, wallet_id, transfer_id, amount_minor,
          currency, status, created_at, updated_at;

-- name: GetWalletBalanceWithVersion :one
SELECT COALESCE(SUM(CASE WHEN jp.side = 'CREDIT' THEN jp.amount_minor ELSE -jp.amount_minor END), 0)::bigint AS posted_balance_minor,
       COALESCE((SELECT SUM(amount_minor) FROM wallet_holds wh
                 WHERE wh.tenant_id = $1 AND wh.wallet_id = $2
                   AND wh.currency = $4 AND wh.status = 'RESERVED'), 0)::bigint AS held_balance_minor,
       (COALESCE(SUM(CASE WHEN jp.side = 'CREDIT' THEN jp.amount_minor ELSE -jp.amount_minor END), 0)
         - COALESCE((SELECT SUM(amount_minor) FROM wallet_holds wh
                    WHERE wh.tenant_id = $1 AND wh.wallet_id = $2
                      AND wh.currency = $4 AND wh.status = 'RESERVED'), 0))::bigint AS available_balance_minor,
       (SELECT COALESCE(MAX(e.aggregate_version), 1)::bigint FROM events e
        WHERE e.tenant_id = $1 AND e.aggregate_id = $3) AS as_of_version
FROM journal_postings jp
WHERE jp.tenant_id = $1 AND jp.account_id = $2 AND jp.currency = $4;
