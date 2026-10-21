-- +goose Up

CREATE TABLE transfers (
    tenant_id UUID NOT NULL,
    transfer_id UUID NOT NULL,
    source_wallet_id UUID NOT NULL,
    destination_wallet_id UUID NOT NULL,
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency TEXT NOT NULL CHECK (currency = 'USD'),
    reference TEXT,
    state TEXT NOT NULL CHECK (state IN ('CREATED', 'RESERVING', 'RESERVED', 'PENDING_CREDIT', 'PENDING_CREDITED', 'CAPTURING', 'CAPTURED', 'FINALIZING', 'COMPLETED', 'FAILED', 'COMPENSATION_PENDING', 'COMPENSATING', 'COMPENSATED', 'RECONCILIATION_REQUIRED')),
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, transfer_id)
);

CREATE TABLE transfer_steps (
    tenant_id UUID NOT NULL,
    transfer_id UUID NOT NULL,
    step_name TEXT NOT NULL CHECK (step_name IN ('RESERVE', 'PENDING_CREDIT', 'CAPTURE', 'FINALIZE', 'RELEASE', 'COMPENSATE')),
    state TEXT NOT NULL CHECK (state IN ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'AMBIGUOUS')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    operation_id UUID NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    last_error TEXT,
    PRIMARY KEY (tenant_id, transfer_id, step_name),
    FOREIGN KEY (tenant_id, transfer_id) REFERENCES transfers (tenant_id, transfer_id)
);

CREATE TABLE transfer_operations (
    tenant_id UUID NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    transfer_id UUID NOT NULL,
    response_body JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, idempotency_key),
    FOREIGN KEY (tenant_id, transfer_id) REFERENCES transfers (tenant_id, transfer_id)
);

CREATE TABLE transfer_events (
    tenant_id UUID NOT NULL,
    event_id UUID NOT NULL,
    transfer_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    aggregate_version BIGINT NOT NULL CHECK (aggregate_version > 0),
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, event_id),
    UNIQUE (tenant_id, transfer_id, aggregate_version),
    FOREIGN KEY (tenant_id, transfer_id) REFERENCES transfers (tenant_id, transfer_id)
);

CREATE TABLE reconciliation_exceptions (
    tenant_id UUID NOT NULL,
    exception_id UUID NOT NULL,
    transfer_id UUID NOT NULL,
    category TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('OPEN', 'RESOLVED')),
    evidence JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, exception_id),
    FOREIGN KEY (tenant_id, transfer_id) REFERENCES transfers (tenant_id, transfer_id)
);

ALTER TABLE transfers OWNER TO migrator_role;
ALTER TABLE transfer_steps OWNER TO migrator_role;
ALTER TABLE transfer_operations OWNER TO migrator_role;
ALTER TABLE transfer_events OWNER TO migrator_role;
ALTER TABLE reconciliation_exceptions OWNER TO migrator_role;
REVOKE ALL ON TABLE transfers, transfer_steps, transfer_operations, transfer_events, reconciliation_exceptions FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON TABLE transfers, transfer_steps, transfer_operations, transfer_events, reconciliation_exceptions TO transfer_role;

-- +goose Down

DROP TABLE IF EXISTS reconciliation_exceptions;
DROP TABLE IF EXISTS transfer_events;
DROP TABLE IF EXISTS transfer_operations;
DROP TABLE IF EXISTS transfer_steps;
DROP TABLE IF EXISTS transfers;
