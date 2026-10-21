# Product Requirements Document

## Document status

| Field | Value |
| --- | --- |
| Product | Digital Wallet Backend |
| Release scope | v1 completed implementation |
| Repository milestone | `phase-8-benchmarks` |
| Primary users | API clients, operators, and platform engineers |
| Source of truth | Running code, migrations, OpenAPI, tests, and benchmark artifacts |
| Currency | USD integer minor units only |

This PRD describes the finished v1 product represented by this repository. It is
not a proposal for the next feature set. Where a capability is deliberately
outside v1, it is listed as a non-goal instead of being implied by the
architecture.

## 1. Product summary

The product is a correctness-first wallet backend for tenant-scoped USD wallet
accounts and wallet-to-wallet transfers. It provides authoritative balances from
an immutable double-entry journal, durable event publication, rebuildable read
models, a cross-wallet transfer saga, reconciliation workflows for ambiguous
outcomes, and operator tools for inspection and recovery.

The system is intentionally split into isolated service boundaries:

- **Wallet service:** owns wallet identity, accounts, journal postings,
  idempotency records, transfer-side state, and its transactional outbox.
- **Transfer service:** owns transfer aggregates, saga steps, retries, and
  reconciliation exceptions. It never queries wallet databases directly.
- **Projector service:** consumes events and builds disposable CQRS read models;
  it never becomes the authority for money.
- **NATS JetStream:** provides durable event delivery and replay support.
- **PostgreSQL:** stores the durable state within each service boundary.

## 2. Problem statement

Wallet systems must remain correct when requests are duplicated, services fail
between a database commit and an acknowledgement, events arrive out of order,
and a cross-wallet operation times out after its side effect may have happened.
The product solves this by making journal state authoritative, making commands
idempotent, persisting delivery state, and routing ambiguous outcomes to an
operator-visible reconciliation lifecycle rather than guessing or silently
adjusting money.

## 3. Goals

### Product goals

1. Create tenant-scoped USD wallets and expose authoritative balances.
2. Accept deposits and withdrawals without permitting negative available funds.
3. Support idempotent cross-wallet transfers through a durable saga.
4. Publish committed wallet facts through a transactional outbox.
5. Serve read-optimized balances and entries without replacing journal authority.
6. Rebuild and verify read models without mutating wallet money.
7. Detect ambiguous or divergent facts and give operators an explicit evidence-
   based resolution workflow.
8. Provide contention-first benchmark evidence for the six defined workloads.

### Engineering goals

- Keep database and package ownership boundaries mechanically enforceable.
- Make immutable money history and journal balancing database-enforced.
- Keep retries bounded and distinguish transient, permanent, absent, and
  ambiguous outcomes.
- Make integration evidence reproducible against isolated PostgreSQL and NATS
  test resources.

## 4. Non-goals and fixed v1 boundaries

The following are deliberately not part of v1:

- multi-currency balances, FX, fees, or exchange-rate management;
- card, ACH, wire, blockchain, or other external payment rails;
- authentication, KYC/AML, customer identity, or entitlement policy;
- multi-region deployment, Kubernetes, Kafka, Redis, Temporal, or a central
  ledger;
- automatic balance repair, compensating entries chosen by a heuristic, or
  blind compensation after an ambiguous timeout;
- mutable balance snapshots as the financial authority;
- direct transfer-service access to wallet schemas or databases.

These exclusions are product scope boundaries, not missing implementation tasks.

## 5. Actors and primary journeys

### API client

1. Create a wallet with a tenant-scoped idempotency key.
2. Deposit or withdraw integer USD minor units using a stable idempotency key.
3. Read the authoritative balance or immutable journal entries.
4. Create a transfer and poll its durable state.
5. Read a projected balance when lower-latency or read-model pagination is useful.

### Operator

1. Inspect a wallet balance, holds, journal entries, or replay checks.
2. Inspect transfer state, step attempts, operation IDs, and recorded errors.
3. List reconciliation exceptions and investigation notes.
4. Claim an exception, collect evidence, and choose an explicit resolution action.
5. Inspect or retry outbox delivery without deleting committed facts.
6. Rebuild, verify, promote, roll back, or retire a projector slot.

### Platform engineer

1. Start the isolated local stack and apply migrations explicitly.
2. Run unit, race, boundary, role-isolation, and real integration checks.
3. Run the contention-first benchmark harness and record p50/p95/p99 and
   throughput results.

## 6. Functional requirements

### Wallets and money commands

| ID | Requirement | Evidence |
| --- | --- | --- |
| WAL-01 | Wallets are tenant-scoped and use UUIDv7 identifiers. | Wallet API and composite database keys |
| WAL-02 | Money is represented as positive integer USD minor units. | `shared/money`, request validation, migrations |
| WAL-03 | Every journal transaction balances debits and credits. | Deferred database checks and ledger tests |
| WAL-04 | Journal transactions, postings, and domain events are append-only. | Database triggers and integration tests |
| WAL-05 | Available balance equals posted balance minus active holds. | Wallet balance implementation and transfer-state migration |
| WAL-06 | Duplicate commands return the stored result; a reused key with a different fingerprint is rejected. | Idempotency records and wallet integration tests |
| WAL-07 | Concurrent withdrawals serialize per wallet and cannot overspend. | Advisory-lock implementation and concurrency tests |

### Events and delivery

| ID | Requirement | Evidence |
| --- | --- | --- |
| EVT-01 | A committed wallet mutation creates its event and outbox row in the same transaction. | Wallet repository and outbox tests |
| EVT-02 | Relay claims use leases and `FOR UPDATE SKIP LOCKED`. | Relay implementation and migration grants |
| EVT-03 | A row is marked published only after JetStream PubAck. | Relay implementation and crash-after-publish test |
| EVT-04 | Duplicate publication is safe through event/message identity and consumer deduplication. | NATS message IDs, inbox, and integration tests |
| EVT-05 | Events for an aggregate are applied in order, buffered when future, and dead-lettered when unsupported or poisoned. | Shared ordering and projector consumer tests |

### Projections

| ID | Requirement | Evidence |
| --- | --- | --- |
| PRJ-01 | Projection folds use event payloads and never read wallet databases. | Projector boundary and fold tests |
| PRJ-02 | Public reads identify their projection watermark and are explicitly non-authoritative. | Projector API |
| PRJ-03 | Read-your-writes waits are bounded by a requested minimum version and timeout. | `min_version`/`wait_ms` API behavior |
| PRJ-04 | Rebuilds target an inactive fixed slot and verify before promotion. | `projectorctl` and projection integration tests |
| PRJ-05 | Promotion, rollback, and retirement are explicit operator actions. | Projector operations and CLI |

### Transfers

| ID | Requirement | Evidence |
| --- | --- | --- |
| TRF-01 | Transfer creation is tenant-scoped and idempotent, returning `202 Accepted`. | Transfer API and repository |
| TRF-02 | The saga progresses through reserve, pending credit, capture, and finalize. | Saga service and integration tests |
| TRF-03 | Holds and pending credits are state-only until the corresponding journal event. | Wallet transfer primitives |
| TRF-04 | Capture and finalize preserve double-entry conservation across source, destination, and clearing accounts. | Ledger invariants and reconciliation checks |
| TRF-05 | Transient failures retry within bounded limits; permanent failures can fail or compensate according to the confirmed state. | Saga failure tests |
| TRF-06 | Ambiguous outcomes enter `RECONCILIATION_REQUIRED` and are never blindly compensated. | Wallet client classification and reconciliation tests |

### Reconciliation and operations

| ID | Requirement | Evidence |
| --- | --- | --- |
| REC-01 | Exceptions begin `OPEN`, can be claimed as `INVESTIGATING`, and end as `RESOLVED` or non-monetary `ACCEPTED_WITH_EVIDENCE`. | Lifecycle migration and integration tests |
| REC-02 | Opening evidence and resolution evidence are immutable; investigation notes are append-only. | Database trigger and notes table |
| REC-03 | Monetary resolution requires transfer facts and evidence to agree. | Resolution constraints and repository validation |
| REC-04 | Reconciliation detects and records mismatches; it never auto-adjusts balances. | Reconciliation service and operator docs |
| OPS-01 | Operators can inspect wallet, transfer, outbox, projection, and exception state. | `walletctl`, `transferctl`, `outboxctl`, `projectorctl` |

## 7. Financial and consistency invariants

The following are product invariants, not merely implementation preferences:

- No journal transaction may have unequal total debits and credits.
- No wallet may expose a negative available balance.
- A duplicate idempotency key cannot create a second financial effect.
- A projector mismatch cannot change authoritative wallet rows.
- A transfer's source capture, destination finalize, and transfer amount must
  agree; clearing accounts must return to zero after a completed flow.
- An ambiguous external or internal side effect must stop progression until the
  operation is confirmed, absent, or explicitly resolved by an operator.
- Terminal reconciliation exceptions cannot be edited or reopened in place.
- Newer unsupported event schema versions are not guessed or interpreted.

## 8. Interfaces and service surfaces

The machine-readable contract is [`api/openapi.yaml`](../api/openapi.yaml).
The human-readable endpoint guide is [`api.md`](api.md).

### Public listeners

| Service | Local port | Main surfaces |
| --- | ---: | --- |
| wallet-source | `8081` | wallet create, balance, entries, deposit, withdraw |
| wallet-destination | `8082` | same wallet API on the destination database |
| transfer | `8083` | transfer create and transfer inspection |
| projector | `8084` | projected balance and entry reads |

### Internal listeners

Internal routes are token-protected and isolated from public listeners. Local
Compose publishes them only on loopback: wallet source `8181`, wallet
destination `8182`, transfer `8183`, and projector `8184`.

Internal routes are for operation commands, verification, reconciliation, and
telemetry. They are not a substitute for public tenant authorization.

## 9. Non-functional requirements

### Correctness

- authoritative money state must be reconstructible from immutable journal rows;
- database constraints and triggers must reject invalid writes, not only tests;
- recovery paths must preserve evidence and avoid silent repair.

### Reliability

- committed work must survive NATS outages through the outbox;
- consumer crashes before ACK must be safe to redeliver;
- rebuilds must preserve the active read slot until a verified candidate exists;
- operator recovery must be explicit, auditable, and tenant-scoped.

### Security and isolation

- runtime services use restricted, service-specific database roles;
- migration ownership is separate from runtime write access;
- transfer code cannot import wallet internals or connect to wallet databases;
- internal HTTP requires `X-Internal-Token` and remains unavailable publicly;
- secrets and non-local credentials must never be committed.

### Performance evidence

The benchmark harness measures six contention-first workloads. Results are
diagnostic evidence tied to a commit, local Compose configuration, dataset size,
and run parameters; they are not production SLAs. See
[`benchmarking.md`](benchmarking.md) and [`.ecc/benchmarks/phase8.json`](../.ecc/benchmarks/phase8.json).

## 10. Release and acceptance status

All eight roadmap phases are implemented and tagged in the current repository.
The final acceptance gate is:

```text
make ci
```

The benchmark module has its own checks:

```text
cd benchmarks
GOWORK=off go test ./...
GOWORK=off go vet ./...
```

The current local acceptance evidence is summarized in
[`project-status.md`](project-status.md). This status does not claim production
readiness for the non-goals listed above, especially authentication, KYC/AML,
external payment rails, and deployment hardening.

## 11. Future product decisions

These are intentionally open rather than silently included in v1:

- Which identity, authentication, tenant administration, and authorization model
  should protect public APIs?
- Which external funding and payout rails, if any, should be added?
- What retention, privacy, audit-export, and regulatory requirements apply to
  journal, event, and operator evidence?
- What production SLOs should replace the local diagnostic benchmark targets?
- Which deployment topology, observability stack, and disaster-recovery policy
  should be supported beyond the local Compose environment?
