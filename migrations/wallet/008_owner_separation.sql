-- +goose Up

-- Migrations run as the database owner, while the wallet service role is a
-- non-owner runtime principal. This migration is intentionally explicit about
-- the two wallet instances because the same directory is applied to both.
ALTER SCHEMA public OWNER TO migrator_role;
ALTER TABLE accounts OWNER TO migrator_role;
ALTER TABLE wallets OWNER TO migrator_role;
ALTER TABLE journal_transactions OWNER TO migrator_role;
ALTER TABLE journal_postings OWNER TO migrator_role;
ALTER TABLE events OWNER TO migrator_role;
ALTER TABLE outbox_messages OWNER TO migrator_role;
ALTER TABLE idempotency_records OWNER TO migrator_role;
ALTER FUNCTION wallet_reject_immutable_change() OWNER TO migrator_role;
ALTER FUNCTION wallet_assert_journal_balanced() OWNER TO migrator_role;
ALTER FUNCTION wallet_assert_immutable_reference() OWNER TO migrator_role;

-- +goose StatementBegin
DO $$
DECLARE
    runtime_role name;
BEGIN
    CASE current_database()
        WHEN 'wallet_source', 'wallet_source_test' THEN runtime_role := 'wallet_source_role';
        WHEN 'wallet_destination', 'wallet_destination_test' THEN runtime_role := 'wallet_destination_role';
        ELSE
            RAISE EXCEPTION 'wallet migrations cannot run against database %', current_database();
    END CASE;

    EXECUTE format('REVOKE ALL ON SCHEMA public FROM %I', runtime_role);
    EXECUTE format('GRANT USAGE ON SCHEMA public TO %I', runtime_role);
    EXECUTE format('REVOKE CREATE ON SCHEMA public FROM %I', runtime_role);
    EXECUTE format('REVOKE TEMP ON DATABASE %I FROM PUBLIC', current_database());
    EXECUTE format('GRANT TEMP ON DATABASE %I TO %I', current_database(), runtime_role);

    EXECUTE format(
        'REVOKE ALL ON TABLE accounts, wallets, journal_transactions, journal_postings, events, outbox_messages, idempotency_records FROM %I',
        runtime_role
    );
    EXECUTE format(
        'GRANT SELECT, INSERT ON TABLE accounts, wallets, journal_transactions, journal_postings, events, outbox_messages, idempotency_records TO %I',
        runtime_role
    );
    EXECUTE format(
        'REVOKE UPDATE, DELETE, TRUNCATE ON TABLE journal_transactions, journal_postings, events, idempotency_records FROM %I',
        runtime_role
    );
END;
$$;
-- +goose StatementEnd

-- Idempotency records are append-only. A transaction-scoped advisory lock on
-- (tenant_id, key) replaces the old IN_PROGRESS row update protocol.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM idempotency_records WHERE status = 'IN_PROGRESS') THEN
        RAISE EXCEPTION 'cannot remove IN_PROGRESS idempotency status while records remain';
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE idempotency_records
    DROP CONSTRAINT idempotency_records_status_check;
ALTER TABLE idempotency_records
    ADD CONSTRAINT idempotency_records_status_check
    CHECK (status = 'COMPLETED');

-- +goose Down

ALTER TABLE idempotency_records
    DROP CONSTRAINT idempotency_records_status_check;
ALTER TABLE idempotency_records
    ADD CONSTRAINT idempotency_records_status_check
    CHECK (status IN ('IN_PROGRESS', 'COMPLETED'));

ALTER TABLE accounts OWNER TO migrator_role;
ALTER TABLE wallets OWNER TO migrator_role;
ALTER TABLE journal_transactions OWNER TO migrator_role;
ALTER TABLE journal_postings OWNER TO migrator_role;
ALTER TABLE events OWNER TO migrator_role;
ALTER TABLE outbox_messages OWNER TO migrator_role;
ALTER TABLE idempotency_records OWNER TO migrator_role;
