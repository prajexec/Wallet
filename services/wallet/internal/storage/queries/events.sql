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

-- name: ClaimOutboxMessages :many
WITH candidates AS (
    SELECT o.tenant_id, o.id
    FROM outbox_messages AS o
    WHERE o.published_at IS NULL
      AND o.next_attempt_at <= now()
      AND (o.locked_until IS NULL OR o.locked_until < now())
    ORDER BY o.created_at, o.id
    LIMIT $1
    FOR UPDATE OF o SKIP LOCKED
), claimed AS (
    UPDATE outbox_messages AS o
    SET locked_until = $2
    FROM candidates AS c
    WHERE o.tenant_id = c.tenant_id
      AND o.id = c.id
    RETURNING o.tenant_id, o.id, o.event_id, o.created_at, o.attempts, o.locked_until
)
SELECT c.tenant_id, c.id, c.event_id, c.created_at, c.attempts, c.locked_until,
       e.aggregate_type, e.event_type, e.payload
FROM claimed AS c
JOIN events AS e
  ON e.tenant_id = c.tenant_id
 AND e.event_id = c.event_id
ORDER BY c.created_at, c.id;

-- name: MarkOutboxPublished :execrows
UPDATE outbox_messages
SET published_at = $3,
    locked_until = NULL,
    last_error = NULL
WHERE tenant_id = $1
  AND id = $2
  AND published_at IS NULL
  AND locked_until = $4;

-- name: MarkOutboxFailed :execrows
UPDATE outbox_messages
SET attempts = attempts + 1,
    last_error = $4,
    next_attempt_at = $5,
    locked_until = NULL
WHERE tenant_id = $1
  AND id = $2
  AND published_at IS NULL
  AND locked_until = $3;
