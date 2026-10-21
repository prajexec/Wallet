# ADR 017: Shared pure ordering decisions, service-owned persistence

Status: accepted
- **Date:** 2026-10-19

## Context

Every consumer needs the same version decision rules, but inboxes, cursors, buffers, gaps, dead letters, and handlers belong to the consuming service database. A shared database or NATS package would weaken module boundaries.

## Decision

Put the pure decision function and event subject/envelope mapping in `shared`, with no PostgreSQL or NATS dependency. The projector owns the generic ordered-consumer runner, its database transaction, and its trivial applied-event handler. The function distinguishes in-order, duplicate/stale, future/buffer, and gap-fill/drain decisions.

## Consequences

- The ordering state machine is unit/property-testable without infrastructure.
- Database durability and ACK timing remain visible in the projector module.
- Wallet and transfer producers share subjects and envelope rules without importing projector internals.

## Alternatives considered

- **In-memory reorder buffer:** rejected because an ACKed message could be lost on process restart.
- **Shared consumer storage package:** rejected because the projector owns read-database schema and migration boundaries.
- **NATS ordering as correctness:** rejected because delivery order is not the aggregate-version contract.

## Addendum (2026-10-20)

See [ADR 019](019-consumer-aggregate-advisory-locking.md) for aggregate-level advisory locking (`pg_advisory_xact_lock`) required to serialize concurrent consumer deliveries for brand-new aggregates prior to cursor creation.
