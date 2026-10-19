-- +goose Up

ALTER TABLE idempotency_records
    DROP CONSTRAINT idempotency_records_status_check;

ALTER TABLE idempotency_records
    ADD CONSTRAINT idempotency_records_status_check
    CHECK (status IN ('IN_PROGRESS', 'COMPLETED'));

GRANT UPDATE ON idempotency_records TO CURRENT_USER;

-- +goose Down

UPDATE idempotency_records
SET status = 'COMPLETED'
WHERE status = 'IN_PROGRESS';

ALTER TABLE idempotency_records
    DROP CONSTRAINT idempotency_records_status_check;

ALTER TABLE idempotency_records
    ADD CONSTRAINT idempotency_records_status_check
    CHECK (status = 'COMPLETED');

REVOKE UPDATE ON idempotency_records FROM CURRENT_USER;
