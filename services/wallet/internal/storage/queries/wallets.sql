-- name: InsertWallet :one
INSERT INTO wallets (
    tenant_id, id, account_id, status, created_at
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING tenant_id, id, account_id, status, created_at;

-- name: GetWallet :one
SELECT tenant_id, id, account_id, status, created_at
FROM wallets
WHERE tenant_id = $1 AND id = $2;

-- name: AcquireWalletLock :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg('lock_key')::text, 0));

-- name: GetWalletForRead :one
SELECT tenant_id, id, account_id, status, created_at
FROM wallets
WHERE tenant_id = $1 AND id = $2
;
