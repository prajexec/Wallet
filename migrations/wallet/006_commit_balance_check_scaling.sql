-- +goose Up

-- The journal transaction constraint trigger already runs after all postings in
-- the transaction. Keep one deferred check per journal transaction rather than
-- queueing one aggregate scan per posting in a large batch.
DROP TRIGGER IF EXISTS journal_postings_balanced ON journal_postings;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION wallet_assert_journal_balanced()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    posting_currency TEXT;
    debit_total BIGINT;
    credit_total BIGINT;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM journal_postings
        WHERE tenant_id = NEW.tenant_id AND transaction_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'journal transaction % has no postings', NEW.id
            USING ERRCODE = '23514';
    END IF;

    FOR posting_currency IN
        SELECT DISTINCT currency
        FROM journal_postings
        WHERE tenant_id = NEW.tenant_id AND transaction_id = NEW.id
    LOOP
        SELECT
            COALESCE(SUM(amount_minor) FILTER (WHERE side = 'DEBIT'), 0),
            COALESCE(SUM(amount_minor) FILTER (WHERE side = 'CREDIT'), 0)
        INTO debit_total, credit_total
        FROM journal_postings
        WHERE tenant_id = NEW.tenant_id
          AND transaction_id = NEW.id
          AND currency = posting_currency;

        IF debit_total <> credit_total OR debit_total <= 0 THEN
            RAISE EXCEPTION 'journal transaction % is unbalanced for currency %: debit %, credit %',
                NEW.id, posting_currency, debit_total, credit_total
                USING ERRCODE = '23514';
        END IF;
    END LOOP;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down

-- The original per-posting constraint trigger is intentionally not restored;
-- migration 005's marker implementation remains the compatibility fallback.
