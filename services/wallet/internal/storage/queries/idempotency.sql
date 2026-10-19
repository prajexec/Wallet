-- name: GetIdempotencyRecord :one
SELECT tenant_id, key, request_hash, status, response_status, response_body, created_at
FROM idempotency_records
WHERE tenant_id = $1 AND key = $2;

-- name: InsertIdempotencyRecord :execrows
INSERT INTO idempotency_records (
    tenant_id, key, request_hash, status, response_status, response_body, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (tenant_id, key) DO NOTHING;

-- name: CompleteIdempotencyRecord :exec
UPDATE idempotency_records
SET status = $3,
    response_status = $4,
    response_body = $5
WHERE tenant_id = $1 AND key = $2;
