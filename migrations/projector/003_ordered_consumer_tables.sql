-- +goose Up

CREATE TABLE inbox_messages (
    tenant_id UUID NOT NULL,
    consumer_name TEXT NOT NULL,
    event_id UUID NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    aggregate_version BIGINT NOT NULL CHECK (aggregate_version > 0),
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, consumer_name, event_id)
);

CREATE INDEX inbox_messages_aggregate_idx
    ON inbox_messages (tenant_id, consumer_name, aggregate_id, aggregate_version);

CREATE TABLE aggregate_cursors (
    tenant_id UUID NOT NULL,
    consumer_name TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    last_version BIGINT NOT NULL CHECK (last_version >= 0),
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, consumer_name, aggregate_id)
);

CREATE TABLE buffered_events (
    tenant_id UUID NOT NULL,
    consumer_name TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    aggregate_version BIGINT NOT NULL CHECK (aggregate_version > 0),
    event_id UUID NOT NULL,
    aggregate_type TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, consumer_name, aggregate_id, aggregate_version),
    UNIQUE (tenant_id, consumer_name, event_id)
);

CREATE TABLE ordering_gaps (
    tenant_id UUID NOT NULL,
    consumer_name TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    expected_version BIGINT NOT NULL CHECK (expected_version > 0),
    status TEXT NOT NULL CHECK (status IN ('OPEN', 'RESOLVED')),
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_alerted_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, consumer_name, aggregate_id, expected_version)
);

CREATE TABLE dead_letter_events (
    tenant_id UUID NOT NULL,
    consumer_name TEXT NOT NULL,
    event_id UUID NOT NULL,
    aggregate_id UUID NOT NULL,
    aggregate_version BIGINT NOT NULL,
    payload JSONB NOT NULL,
    error TEXT NOT NULL,
    delivery_count INTEGER NOT NULL CHECK (delivery_count > 0),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, consumer_name, event_id)
);

CREATE TABLE applied_events_log (
    tenant_id UUID NOT NULL,
    consumer_name TEXT NOT NULL,
    event_id UUID NOT NULL,
    aggregate_id UUID NOT NULL,
    aggregate_version BIGINT NOT NULL,
    event_type TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, consumer_name, event_id),
    UNIQUE (tenant_id, consumer_name, aggregate_id, aggregate_version)
);

GRANT SELECT, INSERT ON inbox_messages TO projector_role;
GRANT SELECT, INSERT, UPDATE ON aggregate_cursors TO projector_role;
GRANT SELECT, INSERT, UPDATE, DELETE ON buffered_events TO projector_role;
GRANT SELECT, INSERT, UPDATE ON ordering_gaps TO projector_role;
GRANT SELECT, INSERT ON dead_letter_events, applied_events_log TO projector_role;

-- +goose Down

DROP TABLE IF EXISTS applied_events_log;
DROP TABLE IF EXISTS dead_letter_events;
DROP TABLE IF EXISTS ordering_gaps;
DROP TABLE IF EXISTS buffered_events;
DROP TABLE IF EXISTS aggregate_cursors;
DROP TABLE IF EXISTS inbox_messages;
