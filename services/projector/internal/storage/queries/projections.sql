-- name: GetProjectionVersion :one
SELECT projection_name, active_slot, previous_slot, promoted_at, updated_at
FROM projection_versions
WHERE projection_name = $1;

-- name: GetActiveWalletBalance :one
SELECT
    wbv.projection_slot,
    wbv.tenant_id,
    wbv.wallet_id,
    wbv.currency,
    wbv.posted_balance_minor,
    wbv.held_balance_minor,
    wbv.available_balance_minor,
    wbv.last_applied_version,
    wbv.source_event_id,
    wbv.updated_at
FROM wallet_balance_view AS wbv
JOIN projection_versions AS pv
  ON pv.projection_name = 'wallet' AND pv.active_slot = wbv.projection_slot
WHERE wbv.tenant_id = $1 AND wbv.wallet_id = $2 AND wbv.currency = $3;

-- name: ListActiveWalletEntries :many
SELECT
    wev.projection_slot,
    wev.tenant_id,
    wev.wallet_id,
    wev.entry_version,
    wev.aggregate_version,
    wev.event_id,
    wev.journal_transaction_id,
    wev.entry_type,
    wev.side,
    wev.amount_minor,
    wev.currency,
    wev.reference,
    wev.occurred_at
FROM wallet_entry_view AS wev
JOIN projection_versions AS pv
  ON pv.projection_name = 'wallet' AND pv.active_slot = wev.projection_slot
WHERE wev.tenant_id = $1
  AND wev.wallet_id = $2
  AND wev.entry_version > $3
ORDER BY wev.entry_version ASC
LIMIT $4;

-- name: UpsertWalletBalance :exec
INSERT INTO wallet_balance_view (
    projection_slot, tenant_id, wallet_id, currency, posted_balance_minor,
    held_balance_minor, available_balance_minor, last_applied_version,
    source_event_id, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (projection_slot, tenant_id, wallet_id, currency)
DO UPDATE SET
    posted_balance_minor = EXCLUDED.posted_balance_minor,
    held_balance_minor = EXCLUDED.held_balance_minor,
    available_balance_minor = EXCLUDED.available_balance_minor,
    last_applied_version = EXCLUDED.last_applied_version,
    source_event_id = EXCLUDED.source_event_id,
    updated_at = EXCLUDED.updated_at;

-- name: GetWalletBalanceForSlot :one
SELECT projection_slot, tenant_id, wallet_id, currency, posted_balance_minor,
       held_balance_minor, available_balance_minor, last_applied_version,
       source_event_id, updated_at
FROM wallet_balance_view
WHERE projection_slot = $1 AND tenant_id = $2 AND wallet_id = $3 AND currency = $4;

-- name: GetNextEntryVersion :one
SELECT (COALESCE(MAX(entry_version), 0) + 1)::bigint
FROM wallet_entry_view
WHERE projection_slot = $1 AND tenant_id = $2 AND wallet_id = $3;

-- name: InsertWalletEntry :exec
INSERT INTO wallet_entry_view (
    projection_slot, tenant_id, wallet_id, entry_version, aggregate_version,
    event_id, journal_transaction_id, entry_type, side, amount_minor,
    currency, reference, occurred_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (projection_slot, tenant_id, event_id) DO NOTHING;

-- name: DeleteWalletEntrySlot :exec
DELETE FROM wallet_entry_view WHERE projection_slot = $1;

-- name: DeleteWalletBalanceSlot :exec
DELETE FROM wallet_balance_view WHERE projection_slot = $1;

-- name: UpdateProjectionVersion :exec
UPDATE projection_versions
SET active_slot = $2, previous_slot = $3, promoted_at = $4, updated_at = $4
WHERE projection_name = $1;

-- name: InsertProjectionRebuild :exec
INSERT INTO projection_rebuilds (rebuild_id, projection_name, target_slot, state, validation_report, started_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6);

-- name: UpdateProjectionRebuild :exec
UPDATE projection_rebuilds
SET state = $2, validation_report = $3, updated_at = $4, completed_at = $5
WHERE rebuild_id = $1;

-- name: GetProjectionRebuild :one
SELECT rebuild_id, projection_name, target_slot, state, validation_report, started_at, updated_at, completed_at
FROM projection_rebuilds
WHERE rebuild_id = $1;
