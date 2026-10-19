# ADR 015: Owner-separated migrations and minimum runtime grants

- **Status:** Accepted
- **Date:** 2026-10-19

## Context

The Phase 3 wallet schema contains immutable journal, event, and outbox tables.
Using a service login as the PostgreSQL database owner lets the application
change schema objects even when table DML is restricted. The previous
idempotency protocol also required `UPDATE` on `idempotency_records` to move a
claim from `IN_PROGRESS` to `COMPLETED`.

## Decision

Create one dedicated `migrator_role` for the local PostgreSQL topology and make
it the owner of all service databases, the `public` schemas, and migration-
created objects. Goose runs with database-specific migrator URLs. Runtime
service roles retain `CONNECT` only on their own database and receive explicit
table grants from service migrations; they do not receive schema `CREATE` or
ownership privileges.

The wallet role may read and append wallet data. Wallet writes use a
transaction-scoped advisory lock, so `accounts` and `wallets` also remain
INSERT/SELECT-only. Immutable journal/event tables and
`idempotency_records` have no runtime `UPDATE`, `DELETE`, `TRUNCATE`, or DDL
privileges.

Idempotency records are append-only. A wallet transaction first takes a
transaction-scoped advisory lock derived from `(tenant_id, key)`, reads an
existing completed record, and otherwise inserts the completed response once
the journal transaction is ready to commit. A rollback releases the lock and
removes the response, so a retry can safely perform the command.

## Consequences

- Database ownership and runtime access are separate security boundaries.
- A schema change requires the migrator credential and an explicit migration;
  service credentials cannot disable triggers, drop triggers/tables, or update
  journal postings.
- Same-key requests serialize on the advisory lock and replay the one committed
  response without an idempotency-row update.
- Existing deployments need an administrator-owned ownership transfer before
  the hardening migration can run; local disposable volumes must be recreated
  so the init script creates the new owner layout.

## Alternatives considered

- **Keep service roles as database owners:** rejected because DDL ownership
  bypasses the intended minimum-grant boundary.
- **Keep a committed `IN_PROGRESS` row and update it:** rejected because it
  requires runtime `UPDATE` on an idempotency table and leaves a more complex
  recovery state.
- **Use a process-local lock:** rejected because it does not serialize multiple
  wallet instances or processes.
