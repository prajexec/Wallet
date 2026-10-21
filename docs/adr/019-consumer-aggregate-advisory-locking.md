# ADR 019: Consumer aggregate advisory locking for ordered projection delivery

Status: accepted
- **Date:** 2026-10-20

## Context

The ordered projection consumer receives events published from outbox relays via NATS JetStream and applies them deterministically in strict aggregate version order ($1, 2, 3, \dots$). The consumer tracks aggregate progression via `aggregate_cursors` (`last_version`) and temporarily buffers out-of-order future versions in `buffered_events`.

When multiple consumer workers or processes pull messages from the durable JetStream consumer, two concurrent deliveries for the same aggregate may be processed in parallel. For brand-new aggregates (where no row exists yet in `aggregate_cursors`), a row-level lock (`SELECT last_version FROM aggregate_cursors WHERE ... FOR UPDATE`) returns `sql.ErrNoRows`. Because no row is locked, both concurrent transactions observe `last_version = 0`.

If Delivery A carries version 1 and Delivery B carries version 2:
1. Delivery B observes `last_version = 0`, classifies version 2 as `FutureBuffer` (since $2 > 0 + 1$), and writes it to `buffered_events`.
2. Delivery A observes `last_version = 0`, applies version 1, checks `buffered_events` (which was empty when Delivery A read it), and advances `last_version = 1`.
3. Delivery B commits, leaving version 2 stranded in `buffered_events` indefinitely, even though predecessor version 1 was successfully applied.

## Decision

Serialize consumer aggregate processing across concurrent transactions using PostgreSQL transaction-scoped advisory locking (`pg_advisory_xact_lock`):

1. **Lock Acquisition:** Before reading `aggregate_cursors` or `buffered_events`, the consumer acquires an advisory lock on the aggregate:
   ```sql
   SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text || ':' || $2::text || ':' || $3::uuid::text, 0));
   ```
   where `$1` is `tenant_id`, `$2` is `consumer_name`, and `$3` is `aggregate_id`.
2. **Serialization Guarantee:** The lock guarantees that concurrent deliveries for the same aggregate (including uninitialized aggregates) execute strictly sequentially. Delivery B blocks until Delivery A commits its version 1 application and cursor advancement. When Delivery B executes, it reads `last_version = 1`, correctly identifies version 2 as contiguous in-order ($2 = 1 + 1$), and applies it immediately without buffering.
3. **Automatic Cleanup:** The lock is scoped to the database transaction (`xact`) and is released automatically on `tx.Commit()` or `tx.Rollback()`.

## Consequences

- Brand-new aggregates cannot experience the first-event race condition where in-order events are prematurely stranded in `buffered_events`.
- Processing across different aggregates and different tenants remains fully parallelized.
- Key collisions across the 64-bit hash space cause safe, transient serialization without state corruption or correctness loss.

## Alternatives considered

- **Pre-seeding `aggregate_cursors`:** Rejected because aggregates are dynamically discovered upon the arrival of `*.created` events.
- **Immediate post-commit buffer re-drain loop:** Rejected because it introduces complex asynchronous polling loops and potential race conditions between draining and concurrent insertion.
- **Advisory locks on aggregate identity:** Selected for its simplicity, deterministic serialization, and zero schema overhead.
