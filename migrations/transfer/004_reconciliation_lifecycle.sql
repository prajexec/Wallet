-- +goose Up

ALTER TABLE reconciliation_exceptions
    DROP CONSTRAINT IF EXISTS reconciliation_exceptions_status_check;

ALTER TABLE reconciliation_exceptions
    ADD COLUMN is_monetary BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN owner TEXT,
    ADD COLUMN resolution_action TEXT,
    ADD COLUMN resolved_by TEXT,
    ADD COLUMN resolved_at TIMESTAMPTZ,
    ADD COLUMN resolution_evidence JSONB,
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE reconciliation_exceptions
    ADD CONSTRAINT reconciliation_exceptions_status_check
        CHECK (status IN ('OPEN', 'INVESTIGATING', 'RESOLVED', 'ACCEPTED_WITH_EVIDENCE')),
    ADD CONSTRAINT reconciliation_exceptions_resolution_action_check
        CHECK (resolution_action IS NULL OR resolution_action IN ('RESUME_SAGA', 'COMPENSATE', 'NO_ACTION', 'ACCEPT')),
    ADD CONSTRAINT reconciliation_exceptions_terminal_fields_check
        CHECK (status NOT IN ('RESOLVED', 'ACCEPTED_WITH_EVIDENCE') OR
               (resolution_action IS NOT NULL AND resolved_by IS NOT NULL AND resolved_at IS NOT NULL AND resolution_evidence IS NOT NULL)),
    ADD CONSTRAINT reconciliation_exceptions_acceptance_check
        CHECK (status <> 'ACCEPTED_WITH_EVIDENCE' OR (is_monetary = FALSE AND resolution_action = 'ACCEPT')),
    ADD CONSTRAINT reconciliation_exceptions_owner_check
        CHECK (status = 'OPEN' OR btrim(owner) <> '');

CREATE UNIQUE INDEX reconciliation_exceptions_active_key
    ON reconciliation_exceptions (tenant_id, transfer_id, category)
    WHERE status IN ('OPEN', 'INVESTIGATING');

CREATE INDEX reconciliation_exceptions_work_queue_idx
    ON reconciliation_exceptions (status, updated_at, tenant_id, exception_id)
    WHERE status IN ('OPEN', 'INVESTIGATING');

CREATE TABLE reconciliation_exception_notes (
    tenant_id UUID NOT NULL,
    note_id UUID NOT NULL,
    exception_id UUID NOT NULL,
    owner TEXT NOT NULL,
    evidence JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, note_id),
    FOREIGN KEY (tenant_id, exception_id) REFERENCES reconciliation_exceptions (tenant_id, exception_id)
);

ALTER TABLE reconciliation_exception_notes OWNER TO migrator_role;
REVOKE ALL ON reconciliation_exception_notes FROM PUBLIC;
GRANT SELECT, INSERT ON reconciliation_exception_notes TO transfer_role;

-- Opening and resolution evidence are write-once. Investigation evidence is
-- appended to reconciliation_exception_notes instead of mutating the opening row.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION transfer_guard_reconciliation_exception()
RETURNS TRIGGER
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $$
BEGIN
	IF TG_OP = 'INSERT' AND NEW.status <> 'OPEN' THEN
		RAISE EXCEPTION 'reconciliation exceptions must start in OPEN state';
	END IF;
	IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'reconciliation exceptions are immutable and cannot be deleted';
    END IF;
    IF TG_OP = 'UPDATE' AND (
        NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR
        NEW.exception_id IS DISTINCT FROM OLD.exception_id OR
        NEW.transfer_id IS DISTINCT FROM OLD.transfer_id OR
        NEW.category IS DISTINCT FROM OLD.category OR
        NEW.is_monetary IS DISTINCT FROM OLD.is_monetary OR
        NEW.evidence IS DISTINCT FROM OLD.evidence OR
        NEW.created_at IS DISTINCT FROM OLD.created_at OR
        (OLD.resolution_evidence IS NOT NULL AND NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence)
    ) THEN
        RAISE EXCEPTION 'reconciliation exception evidence and identity are immutable';
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.status IN ('RESOLVED', 'ACCEPTED_WITH_EVIDENCE') THEN
        RAISE EXCEPTION 'terminal reconciliation exceptions cannot be modified';
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.status = 'OPEN' AND NEW.status NOT IN ('OPEN', 'INVESTIGATING') THEN
        RAISE EXCEPTION 'invalid reconciliation exception transition % -> %', OLD.status, NEW.status;
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.status = 'INVESTIGATING' AND NEW.status NOT IN ('INVESTIGATING', 'RESOLVED', 'ACCEPTED_WITH_EVIDENCE') THEN
        RAISE EXCEPTION 'invalid reconciliation exception transition % -> %', OLD.status, NEW.status;
    END IF;
	IF TG_OP = 'UPDATE' AND NEW.status IN ('RESOLVED', 'ACCEPTED_WITH_EVIDENCE') AND OLD.status NOT IN ('RESOLVED', 'ACCEPTED_WITH_EVIDENCE') THEN
		NEW.resolved_at := now();
	END IF;
	IF TG_OP = 'UPDATE' AND NEW.status = 'RESOLVED' AND NEW.is_monetary THEN
		IF NOT (NEW.resolution_evidence ? 'transfer_state' AND NEW.resolution_evidence ? 'amount_minor' AND NEW.resolution_evidence ? 'currency') THEN
			RAISE EXCEPTION 'monetary resolution evidence is incomplete';
		END IF;
		IF NEW.resolution_evidence->>'amount_minor' IS DISTINCT FROM (SELECT amount_minor::text FROM transfers WHERE tenant_id=NEW.tenant_id AND transfer_id=NEW.transfer_id) OR
		   NEW.resolution_evidence->>'currency' IS DISTINCT FROM (SELECT currency FROM transfers WHERE tenant_id=NEW.tenant_id AND transfer_id=NEW.transfer_id) OR
		   NEW.resolution_evidence->>'transfer_state' IS DISTINCT FROM (SELECT state FROM transfers WHERE tenant_id=NEW.tenant_id AND transfer_id=NEW.transfer_id) THEN
			RAISE EXCEPTION 'monetary resolution evidence does not match the transfer';
		END IF;
	END IF;
    IF TG_OP = 'UPDATE' THEN
        NEW.updated_at := now();
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER reconciliation_exception_guard
    BEFORE UPDATE OR DELETE ON reconciliation_exceptions
    FOR EACH ROW EXECUTE FUNCTION transfer_guard_reconciliation_exception();

ALTER TABLE reconciliation_exceptions OWNER TO migrator_role;
ALTER FUNCTION transfer_guard_reconciliation_exception() OWNER TO migrator_role;
REVOKE ALL ON reconciliation_exceptions FROM transfer_role;
GRANT SELECT, INSERT ON reconciliation_exceptions TO transfer_role;
GRANT UPDATE (owner, status, resolution_action, resolved_by, resolved_at, resolution_evidence, updated_at)
    ON reconciliation_exceptions TO transfer_role;

-- +goose Down

DROP TRIGGER IF EXISTS reconciliation_exception_guard ON reconciliation_exceptions;
DROP FUNCTION IF EXISTS transfer_guard_reconciliation_exception();
DROP TABLE IF EXISTS reconciliation_exception_notes;
DROP INDEX IF EXISTS reconciliation_exceptions_work_queue_idx;
DROP INDEX IF EXISTS reconciliation_exceptions_active_key;
ALTER TABLE reconciliation_exceptions
    DROP CONSTRAINT IF EXISTS reconciliation_exceptions_owner_check,
    DROP CONSTRAINT IF EXISTS reconciliation_exceptions_acceptance_check,
    DROP CONSTRAINT IF EXISTS reconciliation_exceptions_terminal_fields_check,
    DROP CONSTRAINT IF EXISTS reconciliation_exceptions_resolution_action_check,
    DROP CONSTRAINT IF EXISTS reconciliation_exceptions_status_check;
ALTER TABLE reconciliation_exceptions
    DROP COLUMN IF EXISTS is_monetary,
    DROP COLUMN IF EXISTS owner,
    DROP COLUMN IF EXISTS resolution_action,
    DROP COLUMN IF EXISTS resolved_by,
    DROP COLUMN IF EXISTS resolved_at,
    DROP COLUMN IF EXISTS resolution_evidence,
    DROP COLUMN IF EXISTS updated_at;
ALTER TABLE reconciliation_exceptions
    ADD CONSTRAINT reconciliation_exceptions_status_check CHECK (status IN ('OPEN', 'RESOLVED'));
