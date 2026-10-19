# Testing strategy

Correctness tests are layered so that a fast unit suite catches accounting and state-machine errors before database and broker tests exercise crash boundaries.

## Test pyramid

| Layer | Examples | Required checks |
| --- | --- | --- |
| Unit | Money arithmetic, currency validation, balanced postings, idempotency fingerprint, saga transitions | No floats; overflow/negative input; duplicate and conflict cases; every invalid transition rejected. |
| Repository/database | Tenant scoping, composite FKs, immutability triggers, journal transactions, outbox claims | Real PostgreSQL migrations; concurrent writes; rollback leaves no outbox row. |
| Wallet ledger integration | Idempotent deposit/withdraw, account locks, deferred balancing, trigger immutability, replay | Real PostgreSQL through Compose; no mock database is accepted as ledger evidence. |
| Integration | Wallet + transfer + projector with real PostgreSQL and NATS JetStream | Publish/consume, inbox dedup, ordering buffer, read-model watermark, rebuild/promotion. |
| Failure injection | Process kill or injected error at each crash boundary | Duplicate commands/events have no monetary effect; ambiguous outcomes reconcile. |
| State-machine/property | Random command sequences, retries, duplicates, transient/permanent/ambiguous failures | Money conservation, completed transfer equality, no invalid available balance, terminal-state safety. |
| End-to-end | Docker Compose services and public API | Create wallets, deposit, transfer, query authoritative and projected views. |

## Required failure scenarios

1. Duplicate deposit with the same and different request fingerprints.
2. Duplicate transfer creation and concurrent duplicate requests.
3. Concurrent withdrawals against one wallet; exactly the permitted amount succeeds.
4. Relay crash after publish and before outbox mark.
5. Consumer crash before ACK and redelivery through inbox dedup.
6. NATS unavailable during a committed wallet transaction.
7. Wallet unavailable in the middle of a saga.
8. Database restart during reserve, capture, or finalize.
9. Delete a projection, rebuild it, compare balances/checksums with the prior version, and promote.
10. Inject a reconciliation mismatch and prove no automatic balance adjustment occurs.
11. Duplicate a deposit with the same key and verify one journal effect and the exact stored response.
12. Reuse a key with a different payload and verify HTTP 422 without a journal effect.
13. Run at least 50 concurrent withdrawals against one wallet and verify no overspend.
14. Attempt UPDATE, DELETE, and TRUNCATE against each immutable journal/event table and verify database rejection.
15. Insert an intentionally unbalanced transaction and verify the deferred constraint trigger rejects it at commit.
16. Attempt a cross-tenant posting/account reference and verify the composite foreign key rejects it; API reads return 404.
17. Replay all raw postings and compare every wallet's result with the authoritative API balance.
18. Verify every committed wallet mutation has exactly one event and one outbox row with consecutive aggregate versions.

## State-machine/property test

The generator creates wallets, deposits, withdrawals, transfers, retries, duplicate commands, out-of-order events, timeouts, and process failures. The model tracks operation outcomes independently from the implementation. After each generated run and after recovery:

```text
sum(created money) - sum(destroyed money) = 0
completed source capture = completed destination finalization
every available balance >= 0
no operation key has more than one monetary journal effect
```

The test must generate ambiguous outcomes deliberately and assert that the only automatic result is `RECONCILIATION_REQUIRED`, never an automatic compensation based on a timeout alone.

For the Phase 3 ledger property model, generated operations are deposits, withdrawals, same-key retries, different-payload key reuse, and tenant permutations. After every sequence:

```text
no user-wallet available balance < 0
debits = credits for every committed transaction and currency
created money = destroyed money across user and clearing accounts
every idempotency key has at most one monetary effect
```

The Phase 3 integration suite is run with `-race` against the Compose PostgreSQL instance. Tests skip only when the explicit integration database URL is absent from ordinary unit runs; `make test-integration` supplies it and fails on any integration error.

## Go checks

Before every commit touching Go code, run from the repository root:

```text
go vet ./...
go test ./...
go test -race ./...
```

Phase 1 has no Go module yet, so these commands are intentionally not run or claimed as passing. Phase 2 will add the workspace and make these commands meaningful.
