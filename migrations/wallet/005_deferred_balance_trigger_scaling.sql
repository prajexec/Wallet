-- +goose Up

-- Constraint triggers fire once per queued row at commit. The marker table
-- makes the expensive aggregate check run once per transaction/currency
-- target per database transaction instead of once per posting. This keeps the
-- deferred invariant safe for large journal batches without introducing a
-- mutable balance cache.
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
    CREATE TEMP TABLE IF NOT EXISTS wallet_checked_journal_transactions (
        tenant_id UUID NOT NULL,
        transaction_id UUID NOT NULL,
        PRIMARY KEY (tenant_id, transaction_id)
    ) ON COMMIT DELETE ROWS;

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

-- +goose Down

-- Recreate the original trigger function without the transaction marker.
-- A down migration is provided for schema reversibility; Phase 3 forward
-- deployments keep the scaled implementation above.
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
    IF NOT EXISTS (SELECT 1 FROM journal_postings WHERE tenant_id = checked_tenant AND transaction_id = checked_transaction) THEN
        RAISE EXCEPTION 'journal transaction % has no postings', checked_transaction USING ERRCODE = '23514';
    END IF;
    FOR posting_currency IN SELECT DISTINCT currency FROM journal_postings WHERE tenant_id = checked_tenant AND transaction_id = checked_transaction LOOP
        SELECT COALESCE(SUM(amount_minor) FILTER (WHERE side = 'DEBIT'), 0), COALESCE(SUM(amount_minor) FILTER (WHERE side = 'CREDIT'), 0)
        INTO debit_total, credit_total
        FROM journal_postings
        WHERE tenant_id = checked_tenant AND transaction_id = checked_transaction AND currency = posting_currency;
        IF debit_total <> credit_total OR debit_total <= 0 THEN
            RAISE EXCEPTION 'journal transaction % is unbalanced for currency %: debit %, credit %', checked_transaction, posting_currency, debit_total, credit_total USING ERRCODE = '23514';
        END IF;
    END LOOP;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
