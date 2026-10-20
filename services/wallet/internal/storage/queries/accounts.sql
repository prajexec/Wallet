-- name: InsertAccount :exec
INSERT INTO accounts (
    tenant_id, id, kind, name, currency, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (tenant_id, name, currency) DO NOTHING;

-- name: GetAccountByName :one
SELECT tenant_id, id, kind, name, currency, created_at
FROM accounts
WHERE tenant_id = $1 AND name = $2 AND currency = $3;

-- name: GetAccountBalance :one
SELECT COALESCE(
    SUM(CASE WHEN side = 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0
)::bigint AS posted_balance_minor
FROM journal_postings
WHERE tenant_id = $1 AND account_id = $2 AND currency = $3;

-- name: GetAccountBalanceWithVersion :one
SELECT COALESCE(
    SUM(CASE WHEN side = 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0
)::bigint AS posted_balance_minor,
(
    SELECT COALESCE(MAX(e.aggregate_version), 1)::bigint
    FROM events AS e
    WHERE e.tenant_id = $1 AND e.aggregate_id = $3
) AS as_of_version
FROM journal_postings
WHERE tenant_id = $1 AND account_id = $2 AND currency = $4;
