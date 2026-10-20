-- +goose Up

-- Wallet writes now serialize with pg_advisory_xact_lock, so runtime roles do
-- not need UPDATE privilege for SELECT ... FOR UPDATE.
-- +goose StatementBegin
DO $$
DECLARE
    runtime_role name;
BEGIN
    CASE current_database()
        WHEN 'wallet_source', 'wallet_source_test' THEN runtime_role := 'wallet_source_role';
        WHEN 'wallet_destination', 'wallet_destination_test' THEN runtime_role := 'wallet_destination_role';
        ELSE RAISE EXCEPTION 'wallet migration cannot run against database %', current_database();
    END CASE;
    EXECUTE format('REVOKE UPDATE ON TABLE accounts, wallets FROM %I', runtime_role);
END;
$$;
-- +goose StatementEnd

-- +goose Down

-- No UPDATE grant is restored: the advisory-lock contract is the supported
-- runtime locking mechanism.
