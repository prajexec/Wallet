-- +goose Up

CREATE TABLE wallet_operations (
    tenant_id UUID NOT NULL,
    operation_id UUID NOT NULL,
    wallet_id UUID NOT NULL,
    transfer_id UUID NOT NULL,
    operation_kind TEXT NOT NULL CHECK (operation_kind IN ('RESERVE', 'PENDING_CREDIT', 'CAPTURE', 'FINALIZE', 'RELEASE', 'COMPENSATE')),
    target_operation_id UUID,
    request_hash TEXT NOT NULL,
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency TEXT NOT NULL CHECK (currency = 'USD'),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'COMPLETED', 'FAILED')),
    journal_transaction_id UUID,
    event_id UUID,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, operation_id),
    FOREIGN KEY (tenant_id, wallet_id) REFERENCES wallets (tenant_id, id),
    FOREIGN KEY (tenant_id, target_operation_id) REFERENCES wallet_operations (tenant_id, operation_id)
);

CREATE UNIQUE INDEX wallet_operations_effect_idx
    ON wallet_operations (tenant_id, transfer_id, wallet_id, operation_kind)
    WHERE operation_kind IN ('RESERVE', 'PENDING_CREDIT', 'CAPTURE', 'FINALIZE');

CREATE UNIQUE INDEX wallet_operations_compensation_idx
    ON wallet_operations (tenant_id, operation_kind, target_operation_id)
    WHERE operation_kind IN ('RELEASE', 'COMPENSATE');

CREATE TABLE wallet_holds (
    tenant_id UUID NOT NULL,
    hold_operation_id UUID NOT NULL,
    wallet_id UUID NOT NULL,
    transfer_id UUID NOT NULL,
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency TEXT NOT NULL CHECK (currency = 'USD'),
    status TEXT NOT NULL CHECK (status IN ('RESERVED', 'CAPTURED', 'RELEASED')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, hold_operation_id),
    FOREIGN KEY (tenant_id, hold_operation_id) REFERENCES wallet_operations (tenant_id, operation_id),
    FOREIGN KEY (tenant_id, wallet_id) REFERENCES wallets (tenant_id, id),
    UNIQUE (tenant_id, transfer_id, wallet_id)
);

CREATE INDEX wallet_holds_active_wallet_idx
    ON wallet_holds (tenant_id, wallet_id, currency)
    WHERE status = 'RESERVED';

CREATE TABLE wallet_pending_credits (
    tenant_id UUID NOT NULL,
    pending_operation_id UUID NOT NULL,
    wallet_id UUID NOT NULL,
    transfer_id UUID NOT NULL,
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency TEXT NOT NULL CHECK (currency = 'USD'),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'FINALIZED', 'CANCELLED')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, pending_operation_id),
    FOREIGN KEY (tenant_id, pending_operation_id) REFERENCES wallet_operations (tenant_id, operation_id),
    FOREIGN KEY (tenant_id, wallet_id) REFERENCES wallets (tenant_id, id),
    UNIQUE (tenant_id, transfer_id, wallet_id)
);

CREATE INDEX wallet_pending_credits_active_wallet_idx
    ON wallet_pending_credits (tenant_id, wallet_id, currency)
    WHERE status = 'PENDING';

CREATE INDEX journal_postings_transfer_idx
    ON journal_postings (tenant_id, transfer_id, created_at)
    WHERE transfer_id IS NOT NULL;

ALTER TABLE wallet_operations OWNER TO migrator_role;
ALTER TABLE wallet_holds OWNER TO migrator_role;
ALTER TABLE wallet_pending_credits OWNER TO migrator_role;

-- Runtime roles may append operation/state rows and perform only the explicit
-- state transitions used by wallet commands.
-- +goose StatementBegin
DO $$
DECLARE
    runtime_role name;
BEGIN
    CASE current_database()
        WHEN 'wallet_source', 'wallet_source_test' THEN runtime_role := 'wallet_source_role';
        WHEN 'wallet_destination', 'wallet_destination_test' THEN runtime_role := 'wallet_destination_role';
        ELSE RAISE EXCEPTION 'wallet migrations cannot run against database %', current_database();
    END CASE;

    EXECUTE format('REVOKE ALL ON TABLE wallet_operations, wallet_holds, wallet_pending_credits FROM %I', runtime_role);
    EXECUTE format('GRANT SELECT, INSERT ON TABLE wallet_operations, wallet_holds, wallet_pending_credits TO %I', runtime_role);
    EXECUTE format('GRANT UPDATE (status, journal_transaction_id, event_id, updated_at) ON wallet_operations TO %I', runtime_role);
    EXECUTE format('GRANT UPDATE (status, updated_at) ON wallet_holds, wallet_pending_credits TO %I', runtime_role);
END;
$$;
-- +goose StatementEnd

-- +goose Down

DROP INDEX IF EXISTS journal_postings_transfer_idx;
DROP TABLE IF EXISTS wallet_pending_credits;
DROP TABLE IF EXISTS wallet_holds;
DROP TABLE IF EXISTS wallet_operations;
