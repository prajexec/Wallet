# ADR-018: Database advisory locking for runtime isolation and aggregate serialization

Status: accepted
- **Date:** 2026-10-20

## Context

Under Phase 3 hardening and owner separation (ADR 015), database schema objects are owned exclusively by `migrator_role`. Runtime service roles (such as `wallet_source_role` and `wallet_destination_role`) operate with least-privilege permissions: they are granted only `SELECT` and `INSERT` privileges on `accounts`, `wallets`, `journal_transactions`, `journal_postings`, `events`, and `idempotency_records`, with column-restricted `UPDATE` granted only for outbox delivery metadata.

In PostgreSQL, row-level locking via `SELECT ... FOR UPDATE` requires `UPDATE` privilege on the target table. Because runtime roles possess no `UPDATE` privilege on `accounts` or `wallets`, executing `SELECT ... FOR UPDATE` yields a PostgreSQL permission denied error (`42501`). Furthermore, no mutable balance cache row exists, as balances are derived directly from the immutable journal.

Therefore, concurrent mutations on the same wallet aggregate (e.g., concurrent withdrawals, deposits, and idempotency checks) must be serialized using a mechanism that operates under least-privilege permissions without requiring table-level `UPDATE` grants.

## Decision

Use PostgreSQL transaction-scoped advisory locks (`pg_advisory_xact_lock`) for serializing aggregate operations:

1. **Transaction-Scoped Lifecycle:** All wallet mutations obtain `pg_advisory_xact_lock(...)` at the beginning of the mutation transaction. The lock is automatically released by PostgreSQL when the transaction commits or rolls back, preventing lock leakage in the event of worker crashes or unhandled panics.
2. **Key Derivation:** The 64-bit integer advisory lock key is derived deterministically from the composite identifier using PostgreSQL's native hash extension:
   ```sql
   SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text || ':' || $2::text, 0));
   ```
   For wallet operations, the key concatenates `$1` (`tenant_id`) and `$2` (`wallet_id`).
3. **Collision Behavior:** 64-bit hashing via `hashtextextended` distributes keys across a $2^{64}$ address space. In the improbable event of a hash collision between two distinct wallets, the transactions temporarily serialize against each other rather than corrupting state. Correctness is strictly preserved; only concurrent latency on the collided pair would be temporarily affected.
4. **Lock Timeout Policy:** Transactions enforce a bounded lock timeout (`lock_timeout = '2s'`) to prevent indefinite stalls under unexpected deadlocks or heavy lock contention. Timeouts trigger standard retry backoff or abort with `409 Conflict`.
5. **Runtime Grant Isolation:** Runtime roles are granted `EXECUTE` on advisory lock functions (`pg_advisory_xact_lock`, `pg_advisory_unlock`, etc.) without granting any table mutation or schema modification privileges.

## Consequences

- Runtime roles maintain strict least-privilege security posture (no `UPDATE` grants on ledger or aggregate tables).
- Concurrent withdrawals on the same wallet serialize cleanly, preventing double-spending and overdrawing of funds without requiring a mutable balance row.
- Transactions remain self-contained, ensuring complete rollback upon conflict or error without orphaned lock state.

## Alternatives considered

- **Row-level locking (`SELECT ... FOR UPDATE` on accounts/wallets):** Rejected because it requires granting `UPDATE` privilege to runtime roles, violating owner-separation hardening requirements.
- **Explicit session-level advisory locks (`pg_advisory_lock`):** Rejected because session locks require manual unlock calls and leak indefinitely if connections are abruptly severed or pooled.
- **Optimistic concurrency control with retry loops only:** Rejected for high-contention withdrawal operations, as concurrent balance checks without serialization cause excessive serializability aborts and potential overspend races.
