-- +goose Up

CREATE TABLE projection_versions (
    projection_name TEXT PRIMARY KEY CHECK (projection_name = 'wallet'),
    active_slot CHAR(1) NOT NULL CHECK (active_slot IN ('a', 'b')),
    previous_slot CHAR(1) NOT NULL CHECK (previous_slot IN ('a', 'b')),
    promoted_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (active_slot <> previous_slot)
);

CREATE TABLE projection_rebuilds (
    rebuild_id UUID PRIMARY KEY,
    projection_name TEXT NOT NULL CHECK (projection_name = 'wallet'),
    target_slot CHAR(1) NOT NULL CHECK (target_slot IN ('a', 'b')),
    state TEXT NOT NULL CHECK (state IN ('RUNNING', 'CATCHING_UP', 'VERIFYING', 'READY', 'PROMOTED', 'FAILED', 'ABORTED')),
    validation_report JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX projection_rebuilds_one_running_idx
    ON projection_rebuilds (projection_name)
    WHERE state IN ('RUNNING', 'CATCHING_UP', 'VERIFYING', 'READY');

CREATE TABLE wallet_balance_view (
    projection_slot CHAR(1) NOT NULL CHECK (projection_slot IN ('a', 'b')),
    tenant_id UUID NOT NULL,
    wallet_id UUID NOT NULL,
    currency TEXT NOT NULL CHECK (currency = 'USD'),
    posted_balance_minor BIGINT NOT NULL,
    held_balance_minor BIGINT NOT NULL DEFAULT 0 CHECK (held_balance_minor >= 0),
    available_balance_minor BIGINT NOT NULL CHECK (available_balance_minor >= 0),
    last_applied_version BIGINT NOT NULL CHECK (last_applied_version > 0),
    source_event_id UUID NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (projection_slot, tenant_id, wallet_id, currency)
);

CREATE INDEX wallet_balance_view_wallet_idx
    ON wallet_balance_view (tenant_id, wallet_id, currency, projection_slot);

CREATE TABLE wallet_entry_view (
    projection_slot CHAR(1) NOT NULL CHECK (projection_slot IN ('a', 'b')),
    tenant_id UUID NOT NULL,
    wallet_id UUID NOT NULL,
    entry_version BIGINT NOT NULL CHECK (entry_version > 0),
    aggregate_version BIGINT NOT NULL CHECK (aggregate_version > 0),
    event_id UUID NOT NULL,
    journal_transaction_id UUID NOT NULL,
    entry_type TEXT NOT NULL,
    side TEXT NOT NULL CHECK (side IN ('DEBIT', 'CREDIT')),
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency TEXT NOT NULL CHECK (currency = 'USD'),
    reference TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (projection_slot, tenant_id, wallet_id, entry_version),
    UNIQUE (projection_slot, tenant_id, event_id),
    UNIQUE (projection_slot, tenant_id, wallet_id, journal_transaction_id)
);

CREATE INDEX wallet_entry_view_keyset_idx
    ON wallet_entry_view (projection_slot, tenant_id, wallet_id, entry_version);

INSERT INTO projection_versions (projection_name, active_slot, previous_slot, promoted_at, updated_at)
VALUES ('wallet', 'a', 'b', now(), now());

GRANT SELECT, INSERT, UPDATE ON projection_versions TO projector_role;
GRANT SELECT, INSERT, UPDATE ON projection_rebuilds TO projector_role;
GRANT SELECT, INSERT, UPDATE, DELETE ON wallet_balance_view, wallet_entry_view TO projector_role;

-- +goose Down

DROP TABLE IF EXISTS wallet_entry_view;
DROP TABLE IF EXISTS wallet_balance_view;
DROP INDEX IF EXISTS projection_rebuilds_one_running_idx;
DROP TABLE IF EXISTS projection_rebuilds;
DROP TABLE IF EXISTS projection_versions;
