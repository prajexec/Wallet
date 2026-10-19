-- +goose Up

ALTER TABLE outbox_messages
    ADD COLUMN last_error TEXT,
    ADD COLUMN locked_until TIMESTAMPTZ;

-- The relay claims with a short lease and updates only delivery metadata. The
-- wallet role cannot update journal, event, account, wallet, or idempotency
-- history.
-- +goose StatementBegin
DO $$
DECLARE
    runtime_role name;
BEGIN
    CASE current_database()
        WHEN 'wallet_source' THEN runtime_role := 'wallet_source_role';
        WHEN 'wallet_destination' THEN runtime_role := 'wallet_destination_role';
        ELSE RAISE EXCEPTION 'wallet migration cannot run against database %', current_database();
    END CASE;
    EXECUTE format('REVOKE UPDATE ON outbox_messages FROM %I', runtime_role);
    EXECUTE format('GRANT UPDATE (published_at, attempts, next_attempt_at, locked_until, last_error) ON outbox_messages TO %I', runtime_role);
END;
$$;
-- +goose StatementEnd

-- +goose Down

ALTER TABLE outbox_messages
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS locked_until;
