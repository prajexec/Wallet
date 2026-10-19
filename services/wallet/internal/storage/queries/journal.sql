-- name: InsertJournalTransaction :one
INSERT INTO journal_transactions (
    tenant_id, id, operation_type, external_reference, created_at
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING tenant_id, id, operation_type, external_reference, created_at;

-- name: InsertJournalPosting :exec
INSERT INTO journal_postings (
    tenant_id, id, transaction_id, account_id, side, amount_minor,
    currency, entry_type, transfer_id, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
);

-- name: ListWalletEntries :many
SELECT
    jt.id AS transaction_id,
    jt.operation_type,
    jt.external_reference,
    jt.created_at AS transaction_created_at,
    jsonb_agg(
        jsonb_build_object(
            'id', jp.id,
            'account_id', jp.account_id,
            'side', jp.side,
            'amount_minor', jp.amount_minor,
            'currency', jp.currency,
            'entry_type', jp.entry_type,
            'transfer_id', jp.transfer_id,
            'created_at', jp.created_at
        ) ORDER BY jp.id
    ) AS postings
FROM journal_transactions AS jt
JOIN journal_postings AS jp
  ON jp.tenant_id = jt.tenant_id AND jp.transaction_id = jt.id
WHERE jt.tenant_id = $1
  AND jp.account_id = $2
GROUP BY jt.id, jt.operation_type, jt.external_reference, jt.created_at
ORDER BY jt.created_at DESC, jt.id DESC
LIMIT $3;

-- name: ListWalletEntriesBefore :many
SELECT
    jt.id AS transaction_id,
    jt.operation_type,
    jt.external_reference,
    jt.created_at AS transaction_created_at,
    jsonb_agg(
        jsonb_build_object(
            'id', jp.id,
            'account_id', jp.account_id,
            'side', jp.side,
            'amount_minor', jp.amount_minor,
            'currency', jp.currency,
            'entry_type', jp.entry_type,
            'transfer_id', jp.transfer_id,
            'created_at', jp.created_at
        ) ORDER BY jp.id
    ) AS postings
FROM journal_transactions AS jt
JOIN journal_postings AS jp
  ON jp.tenant_id = jt.tenant_id AND jp.transaction_id = jt.id
WHERE jt.tenant_id = $1
  AND jp.account_id = $2
  AND (jt.created_at, jt.id) < ($3, $4)
GROUP BY jt.id, jt.operation_type, jt.external_reference, jt.created_at
ORDER BY jt.created_at DESC, jt.id DESC
LIMIT $5;

-- name: ListPostingsForAccount :many
SELECT tenant_id, id, transaction_id, account_id, side, amount_minor,
       currency, entry_type, transfer_id, created_at
FROM journal_postings
WHERE tenant_id = $1 AND account_id = $2
ORDER BY created_at ASC, id ASC;
