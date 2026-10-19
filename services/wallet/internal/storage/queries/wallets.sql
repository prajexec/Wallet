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

-- name: GetWalletForUpdate :one
SELECT tenant_id, id, account_id, status, created_at
FROM wallets
WHERE tenant_id = $1 AND id = $2
FOR UPDATE;
