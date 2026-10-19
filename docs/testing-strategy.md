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
19. Verify a posting appended to an existing journal transaction is checked at commit and an unbalanced append rolls back.
20. Verify wallet creation emits aggregate version 1 and later wallet events are contiguous.
21. Verify service roles cannot connect across databases or alter wallet schema objects, disable journal triggers, drop journal triggers/tables, or update journal postings.
22. Verify wallet `/readyz` is HTTP 200/degraded when only NATS is unavailable and HTTP 503 when PostgreSQL is unavailable.
23. Commit a wallet mutation while NATS is stopped, restart NATS, and verify the relay drains the outbox into JetStream.
24. Inject a relay crash after PubAck and before mark; verify lease expiry/republication, JetStream message deduplication, and inbox deduplication with a bypassed message ID.
25. Run two relays concurrently and verify no outbox lease is held by both at once and every row is eventually published.
26. Crash the projector consumer before ACK; verify redelivery has no second applied effect.
27. Deliver versions 3, 1, and 2 out of order; verify durable buffering and applied order 1, 2, 3.
28. Leave a missing predecessor past the gap window; verify `ordering_gaps`, then fill the gap and verify buffered drain.
29. Restart the projector with buffered events and verify they survive in PostgreSQL.
30. Deliver a poison event until `MaxDeliver`; verify `dead_letter_events` is written before ACK.
31. Duplicate N events and verify exactly N `applied_events_log` rows.
32. Run the end-to-end wallet flow with relays and projector enabled; compare journal event totals with applied-event totals.

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

The Phase 3 integration suite is run with `-race` against the Compose PostgreSQL instance. Tests skip only when the explicit integration database URL is absent from ordinary unit runs; `make test-integration` supplies it and fails on any integration error. The migration target uses the owner-only migrator URL while the suite connects as the wallet runtime role.

Phase 4 adds real NATS JetStream integration and randomized ordering tests. Failure-injection tests use deterministic failpoint hooks that are unreachable unless explicitly enabled by the test harness.

## Go checks

Before every commit touching Go code, run from the repository root:

```text
go vet ./...
go test ./...
go test -race ./...
```

Phase 1 has no Go module yet, so these commands are intentionally not run or claimed as passing. Phase 2 will add the workspace and make these commands meaningful.
