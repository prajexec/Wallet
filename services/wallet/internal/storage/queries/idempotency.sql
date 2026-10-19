-- name: GetIdempotencyRecord :one
SELECT tenant_id, key, request_hash, status, response_status, response_body, created_at
FROM idempotency_records
WHERE tenant_id = $1 AND key = $2;

-- name: AcquireIdempotencyLock :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg('tenant_id')::uuid::text || ':' || sqlc.arg('key')::text, 0));

-- name: InsertIdempotencyRecord :execrows
INSERT INTO idempotency_records (
    tenant_id, key, request_hash, status, response_status, response_body, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (tenant_id, key) DO NOTHING;
