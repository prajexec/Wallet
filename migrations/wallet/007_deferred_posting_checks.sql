-- +goose Up

-- Restore deferred validation for postings added to an existing transaction,
-- while keeping the marker table initialized only once per database transaction.
DROP TRIGGER IF EXISTS journal_postings_balanced ON journal_postings;

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
    IF current_setting('wallet.ledger_marker_initialized', true) IS DISTINCT FROM 'on' THEN
        CREATE TEMP TABLE IF NOT EXISTS wallet_checked_journal_transactions (
            tenant_id UUID NOT NULL,
            transaction_id UUID NOT NULL,
            PRIMARY KEY (tenant_id, transaction_id)
        ) ON COMMIT DELETE ROWS;
        PERFORM set_config('wallet.ledger_marker_initialized', 'on', true);
    END IF;

    IF TG_TABLE_NAME = 'journal_transactions' THEN
        checked_tenant := NEW.tenant_id;
        checked_transaction := NEW.id;
    ELSE
        checked_tenant := COALESCE(NEW.tenant_id, OLD.tenant_id);
        checked_transaction := COALESCE(NEW.transaction_id, OLD.transaction_id);
    END IF;

    INSERT INTO wallet_checked_journal_transactions (tenant_id, transaction_id)
    VALUES (checked_tenant, checked_transaction)
    ON CONFLICT DO NOTHING;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM journal_postings
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

CREATE CONSTRAINT TRIGGER journal_postings_balanced
    AFTER INSERT OR UPDATE OR DELETE ON journal_postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallet_assert_journal_balanced();

-- +goose Down

DROP TRIGGER IF EXISTS journal_postings_balanced ON journal_postings;
