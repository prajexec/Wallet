-- +goose Up

CREATE TABLE accounts (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('user_wallet', 'system_clearing')),
    name TEXT NOT NULL,
    currency TEXT NOT NULL CHECK (currency = 'USD'),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name, currency)
);

CREATE TABLE wallets (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    account_id UUID NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('ACTIVE', 'CLOSED')),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, account_id),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id)
);

CREATE TABLE journal_transactions (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    operation_type TEXT NOT NULL,
    external_reference TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id)
);

CREATE TABLE journal_postings (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    transaction_id UUID NOT NULL,
    account_id UUID NOT NULL,
    side TEXT NOT NULL CHECK (side IN ('DEBIT', 'CREDIT')),
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency TEXT NOT NULL CHECK (currency = 'USD'),
    entry_type TEXT NOT NULL,
    transfer_id UUID,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, transaction_id) REFERENCES journal_transactions (tenant_id, id),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id)
);

CREATE INDEX journal_postings_account_balance_idx
    ON journal_postings (tenant_id, account_id, currency);

CREATE INDEX journal_postings_transaction_idx
    ON journal_postings (tenant_id, transaction_id);

CREATE TABLE events (
    tenant_id UUID NOT NULL,
    event_id UUID NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    aggregate_version BIGINT NOT NULL CHECK (aggregate_version > 0),
    event_type TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    correlation_id UUID NOT NULL,
    causation_id UUID,
    occurred_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    PRIMARY KEY (tenant_id, event_id),
    UNIQUE (tenant_id, aggregate_id, aggregate_version)
);

CREATE TABLE outbox_messages (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    event_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, event_id),
    FOREIGN KEY (tenant_id, event_id) REFERENCES events (tenant_id, event_id)
);

CREATE INDEX outbox_messages_pending_idx
    ON outbox_messages (tenant_id, next_attempt_at, created_at)
    WHERE published_at IS NULL;

CREATE TABLE idempotency_records (
    tenant_id UUID NOT NULL,
    key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('COMPLETED')),
    response_status INTEGER NOT NULL CHECK (response_status BETWEEN 200 AND 599),
    response_body JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, key)
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION wallet_reject_immutable_change()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only; % is not permitted', TG_TABLE_NAME, TG_OP
        USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER journal_transactions_immutable_row
    BEFORE UPDATE OR DELETE ON journal_transactions
    FOR EACH ROW EXECUTE FUNCTION wallet_reject_immutable_change();
CREATE TRIGGER journal_transactions_immutable_truncate
    BEFORE TRUNCATE ON journal_transactions
    FOR EACH STATEMENT EXECUTE FUNCTION wallet_reject_immutable_change();

CREATE TRIGGER journal_postings_immutable_row
    BEFORE UPDATE OR DELETE ON journal_postings
    FOR EACH ROW EXECUTE FUNCTION wallet_reject_immutable_change();
CREATE TRIGGER journal_postings_immutable_truncate
    BEFORE TRUNCATE ON journal_postings
    FOR EACH STATEMENT EXECUTE FUNCTION wallet_reject_immutable_change();

CREATE TRIGGER events_immutable_row
    BEFORE UPDATE OR DELETE ON events
    FOR EACH ROW EXECUTE FUNCTION wallet_reject_immutable_change();
CREATE TRIGGER events_immutable_truncate
    BEFORE TRUNCATE ON events
    FOR EACH STATEMENT EXECUTE FUNCTION wallet_reject_immutable_change();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION wallet_assert_journal_balanced()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    checked_tenant UUID;
    checked_transaction UUID;
    posting_currency TEXT;
    debit_total BIGINT;
    credit_total BIGINT;
BEGIN
    IF TG_TABLE_NAME = 'journal_transactions' THEN
        checked_tenant := NEW.tenant_id;
        checked_transaction := NEW.id;
    ELSE
        checked_tenant := COALESCE(NEW.tenant_id, OLD.tenant_id);
        checked_transaction := COALESCE(NEW.transaction_id, OLD.transaction_id);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM journal_postings
        WHERE tenant_id = checked_tenant AND transaction_id = checked_transaction
    ) THEN
        RAISE EXCEPTION 'journal transaction % has no postings', checked_transaction
            USING ERRCODE = '23514';
    END IF;

    FOR posting_currency IN
        SELECT DISTINCT currency
        FROM journal_postings
        WHERE tenant_id = checked_tenant AND transaction_id = checked_transaction
    LOOP
        SELECT
            COALESCE(SUM(amount_minor) FILTER (WHERE side = 'DEBIT'), 0),
            COALESCE(SUM(amount_minor) FILTER (WHERE side = 'CREDIT'), 0)
        INTO debit_total, credit_total
        FROM journal_postings
        WHERE tenant_id = checked_tenant
          AND transaction_id = checked_transaction
          AND currency = posting_currency;

        IF debit_total <> credit_total OR debit_total <= 0 THEN
            RAISE EXCEPTION 'journal transaction % is unbalanced for currency %: debit %, credit %',
                checked_transaction, posting_currency, debit_total, credit_total
                USING ERRCODE = '23514';
        END IF;
    END LOOP;

    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER journal_transactions_balanced
    AFTER INSERT ON journal_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallet_assert_journal_balanced();
CREATE CONSTRAINT TRIGGER journal_postings_balanced
    AFTER INSERT OR UPDATE OR DELETE ON journal_postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallet_assert_journal_balanced();

-- The application role can append and read but cannot mutate journal history.
-- Ownership is retained by the service role for migration lifecycle; database
-- triggers reject owner-level UPDATE, DELETE, and TRUNCATE as well.
REVOKE ALL ON accounts, wallets, journal_transactions, journal_postings, events,
    outbox_messages, idempotency_records FROM PUBLIC;
GRANT SELECT, INSERT ON accounts, wallets, journal_transactions, journal_postings,
    events, outbox_messages, idempotency_records TO CURRENT_USER;
REVOKE UPDATE, DELETE, TRUNCATE ON journal_transactions, journal_postings, events FROM CURRENT_USER;

-- +goose Down

DROP TRIGGER IF EXISTS journal_transactions_balanced ON journal_transactions;
DROP TRIGGER IF EXISTS journal_postings_balanced ON journal_postings;
DROP TRIGGER IF EXISTS journal_transactions_immutable_row ON journal_transactions;
DROP TRIGGER IF EXISTS journal_transactions_immutable_truncate ON journal_transactions;
DROP TRIGGER IF EXISTS journal_postings_immutable_row ON journal_postings;
DROP TRIGGER IF EXISTS journal_postings_immutable_truncate ON journal_postings;
DROP TRIGGER IF EXISTS events_immutable_row ON events;
DROP TRIGGER IF EXISTS events_immutable_truncate ON events;
DROP FUNCTION IF EXISTS wallet_assert_journal_balanced();
DROP FUNCTION IF EXISTS wallet_reject_immutable_change();
DROP TABLE IF EXISTS idempotency_records;
DROP TABLE IF EXISTS outbox_messages;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS journal_postings;
DROP TABLE IF EXISTS journal_transactions;
DROP TABLE IF EXISTS wallets;
DROP TABLE IF EXISTS accounts;
