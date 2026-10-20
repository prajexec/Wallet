-- name: InsertWallet :one
INSERT INTO wallets (
    tenant_id, id, account_id, status, created_at
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING tenant_id, id, account_id, status, created_at;

-- name: GetWallet :one
SELECT
    w.tenant_id,
    w.id,
    w.account_id,
    w.status,
    w.created_at,
    (
        SELECT COALESCE(MAX(e.aggregate_version), 1)::bigint
        FROM events AS e
        WHERE e.tenant_id = w.tenant_id AND e.aggregate_id = w.id
    ) AS aggregate_version
FROM wallets AS w
WHERE w.tenant_id = $1 AND w.id = $2;

-- name: AcquireWalletLock :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg('lock_key')::text, 0));

-- name: GetWalletForRead :one
SELECT tenant_id, id, account_id, status, created_at
FROM wallets
WHERE tenant_id = $1 AND id = $2;

-- name: ListWalletVerificationRows :many
SELECT
    w.tenant_id,
    w.id AS wallet_id,
    a.currency,
    COALESCE((
        SELECT SUM(CASE WHEN jp.side = 'CREDIT' THEN jp.amount_minor ELSE -jp.amount_minor END)
        FROM journal_postings AS jp
        WHERE jp.tenant_id = w.tenant_id AND jp.account_id = w.account_id AND jp.currency = a.currency
    ), 0)::bigint AS posted_balance_minor,
    COALESCE((
        SELECT MAX(e.aggregate_version)
        FROM events AS e
        WHERE e.tenant_id = w.tenant_id AND e.aggregate_id = w.id
    ), 1)::bigint AS as_of_version
FROM wallets AS w
JOIN accounts AS a ON a.tenant_id = w.tenant_id AND a.id = w.account_id
WHERE ($1::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR (w.tenant_id, w.id) > ($1::uuid, $2::uuid))
ORDER BY w.tenant_id ASC, w.id ASC
LIMIT $3;
