# ADR 013: Transaction-scoped advisory locking for wallet writes

- **Status:** Superseded by [ADR 018](018-advisory-lock-runtime-isolation.md)
- **Date:** 2026-10-19

## Context

There is intentionally no mutable balance row. A withdrawal must therefore serialize against other withdrawals while computing the balance from journal postings. A lock-free read-then-append sequence could overspend a user wallet.

## Decision

Within every wallet mutation transaction, take `pg_advisory_xact_lock` keyed by `(tenant_id, wallet_id)`, read the wallet/account, compute the journal-derived balance, check available funds, append postings, then append the event, outbox row, and idempotency response. This preserves INSERT/SELECT-only runtime grants on `accounts` and `wallets` while serializing wallet writes. The events table also enforces unique `(tenant_id, aggregate_id, aggregate_version)` as a second safety net. Serialization/deadlock errors receive a small bounded retry budget.

## Consequences

- Writes to one wallet serialize and contention is visible in advisory-lock wait metrics.
- Balance reads remain reproducible from postings and no cache invalidation path is needed.
- A missed lock or version race fails loudly with a database uniqueness error instead of silently creating a second effect.

## Alternatives considered

- **Mutable balance row lock:** rejected because no balance cache is allowed in this phase.
- **Account-row `SELECT ... FOR UPDATE`:** rejected during owner-separation hardening because PostgreSQL requires UPDATE privilege for the runtime role.
- **`pg_advisory_xact_lock`:** selected because it provides transaction-scoped serialization without granting runtime UPDATE or ownership privileges.
- **Serializable isolation only:** rejected as the sole mechanism because retry behavior and aggregate ownership would be less explicit.
