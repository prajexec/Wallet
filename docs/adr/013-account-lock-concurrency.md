# ADR 013: Account-row locking for wallet writes

- **Status:** Accepted
- **Date:** 2026-10-19

## Context

There is intentionally no mutable balance row. A withdrawal must therefore serialize against other withdrawals while computing the balance from journal postings. A lock-free read-then-append sequence could overspend a user wallet.

## Decision

Within every wallet mutation transaction, lock the wallet's user account row with `SELECT ... FOR UPDATE`, compute the journal-derived balance, check available funds, append postings, then append the event, outbox row, and idempotency response. The events table also enforces unique `(tenant_id, aggregate_id, aggregate_version)` as a second safety net. Serialization/deadlock errors receive a small bounded retry budget.

## Consequences

- Writes to one wallet serialize and contention is visible in lock-wait metrics.
- Balance reads remain reproducible from postings and no cache invalidation path is needed.
- A missed lock or version race fails loudly with a database uniqueness error instead of silently creating a second effect.

## Alternatives considered

- **Mutable balance row lock:** rejected because no balance cache is allowed in this phase.
- **`pg_advisory_xact_lock`:** viable, but account-row locking makes ownership and lock inspection visible in PostgreSQL and naturally protects the account row.
- **Serializable isolation only:** rejected as the sole mechanism because retry behavior and aggregate ownership would be less explicit.
