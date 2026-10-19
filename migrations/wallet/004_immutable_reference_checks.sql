-- +goose Up

-- PostgreSQL's FK insert check takes a KEY SHARE lock on the referenced row,
-- which requires UPDATE privilege on an append-only table. The wallet role is
-- intentionally INSERT/SELECT-only on immutable tables, so these two
-- tenant-scoped reference checks are equivalent BEFORE INSERT triggers.
ALTER TABLE journal_postings
    DROP CONSTRAINT journal_postings_tenant_id_transaction_id_fkey;
ALTER TABLE outbox_messages
    DROP CONSTRAINT outbox_messages_tenant_id_event_id_fkey;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION wallet_assert_immutable_reference()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_TABLE_NAME = 'journal_postings' THEN
        IF NOT EXISTS (
            SELECT 1 FROM journal_transactions
            WHERE tenant_id = NEW.tenant_id AND id = NEW.transaction_id
        ) THEN
            RAISE EXCEPTION 'journal posting % references missing transaction % for tenant %',
                NEW.id, NEW.transaction_id, NEW.tenant_id
                USING ERRCODE = '23503';
        END IF;
    ELSIF TG_TABLE_NAME = 'outbox_messages' THEN
        IF NOT EXISTS (
            SELECT 1 FROM events
            WHERE tenant_id = NEW.tenant_id AND event_id = NEW.event_id
        ) THEN
            RAISE EXCEPTION 'outbox message % references missing event % for tenant %',
                NEW.id, NEW.event_id, NEW.tenant_id
                USING ERRCODE = '23503';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER journal_postings_transaction_reference
    BEFORE INSERT ON journal_postings
    FOR EACH ROW EXECUTE FUNCTION wallet_assert_immutable_reference();
CREATE TRIGGER outbox_messages_event_reference
    BEFORE INSERT ON outbox_messages
    FOR EACH ROW EXECUTE FUNCTION wallet_assert_immutable_reference();

-- +goose Down

DROP TRIGGER IF EXISTS journal_postings_transaction_reference ON journal_postings;
DROP TRIGGER IF EXISTS outbox_messages_event_reference ON outbox_messages;
DROP FUNCTION IF EXISTS wallet_assert_immutable_reference();
ALTER TABLE journal_postings
    ADD CONSTRAINT journal_postings_tenant_id_transaction_id_fkey
    FOREIGN KEY (tenant_id, transaction_id) REFERENCES journal_transactions (tenant_id, id);
ALTER TABLE outbox_messages
    ADD CONSTRAINT outbox_messages_tenant_id_event_id_fkey
    FOREIGN KEY (tenant_id, event_id) REFERENCES events (tenant_id, event_id);
