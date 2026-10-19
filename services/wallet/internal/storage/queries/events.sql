-- name: GetNextAggregateVersion :one
SELECT (COALESCE(MAX(aggregate_version), 0) + 1)::bigint AS next_version
FROM events
WHERE tenant_id = $1 AND aggregate_id = $2;

-- name: InsertEvent :one
INSERT INTO events (
    tenant_id, event_id, aggregate_type, aggregate_id, aggregate_version,
    event_type, schema_version, correlation_id, causation_id, occurred_at, payload
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
RETURNING tenant_id, event_id, aggregate_type, aggregate_id, aggregate_version,
          event_type, schema_version, correlation_id, causation_id, occurred_at, payload;

-- name: InsertOutboxMessage :one
INSERT INTO outbox_messages (
    tenant_id, id, event_id, created_at, published_at, attempts, next_attempt_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING tenant_id, id, event_id, created_at, published_at, attempts, next_attempt_at;
