# Digital Wallet Backend

Fintech-grade digital wallet backend built around an immutable, double-entry journal. The system uses two logical wallet instances (`wallet-source` and `wallet-destination`), a transfer-service saga, a transactional outbox, NATS JetStream delivery, and rebuildable CQRS read models.

Correctness is the primary goal. Balances are reproducible from the journal; duplicate commands and events cannot create money; ambiguous side effects are handed to reconciliation rather than guessed at.

## Scope

The v1 scope is deliberately narrow:

- one currency: USD;
- wallet-to-wallet transfers only;
- integer minor units, never floating point;
- Go services using `net/http` or `chi`, `slog`, PostgreSQL, NATS JetStream, `sqlc`, and `goose` or `golang-migrate`;
- no fees, FX, external rails, auth/KYC, multi-region deployment, Kubernetes, Kafka, Redis, Temporal, or central ledger.

## Architecture at a glance

```mermaid
flowchart LR
    Client[Client / caller]
    Transfer[Transfer service<br/>saga + transfer DB]
    Source[Wallet service: source<br/>wallet DB A]
    Destination[Wallet service: destination<br/>wallet DB B]
    NATS[(NATS JetStream)]
    Projector[Projector service<br/>read DB]
    Read[(CQRS read models)]

    Client --> Transfer
    Transfer -->|reserve / capture| Source
    Transfer -->|pending credit / finalize| Destination
    Source -->|outbox events| NATS
    Destination -->|outbox events| NATS
    Transfer -->|saga events| NATS
    NATS --> Projector
    Projector --> Read
    Client -->|authoritative or read-model query| Source
    Client -->|query| Read
```

Each wallet instance owns its own accounts and journal. The transfer service coordinates the cross-database workflow but has no access to wallet schemas. The projector consumes events and owns only read models.

## Documentation index

| Document | Purpose |
| --- | --- |
| [Product requirements](docs/product-requirements.md) | Product scope, user journeys, requirements, invariants, and explicit non-goals |
| [Project status](docs/project-status.md) | Completed phase/tag matrix, verification gate, evidence, and known boundaries |
| [Architecture](docs/architecture.md) | System context, containers, responsibilities, and ownership |
| [Ledger invariants](docs/ledger-invariants.md) | Double-entry rules, balance formulas, and safety invariants |
| [Event model](docs/event-model.md) | Event envelope, catalog, ordering, and deduplication |
| [Saga](docs/saga.md) | Transfer state machine, retries, compensation, and reconciliation |
| [Outbox and inbox](docs/outbox-inbox.md) | Durable publication and consumer crash safety |
| [CQRS projections](docs/cqrs-projections.md) | Read models, cursors, rebuild, validation, and promotion |
| [Reconciliation](docs/reconciliation.md) | Checks, exception lifecycle, and operator runbook |
| [Data model](docs/data-model.md) | ER diagrams for wallet, transfer, and read databases |
| [API](docs/api.md) / [OpenAPI](api/openapi.yaml) | Public and internal HTTP contracts |
| [Testing strategy](docs/testing-strategy.md) | Unit, integration, failure-injection, and state-machine testing |
| [Benchmarking](docs/benchmarking.md) | Contention-first benchmark plan and result template |
| [Operations](docs/operations.md) | Local run, environment, migrations, CLIs, and troubleshooting |
| [ADRs](docs/adr/README.md) | Indexed architecture decision records |

## Phase plan

1. Contracts and documentation — complete (`phase-1-contracts`).
2. Repository foundation and local infrastructure — complete (`phase-2-foundation`).
3. Immutable wallet ledger — complete (`phase-3-ledger`).
4. Outbox and event consumption — complete (`phase-4-outbox`).
5. CQRS projections — complete (`phase-5-projections`).
6. Transfer saga — complete (`phase-6-transfer`).
7. Reconciliation and operations tooling — complete (`phase-7-reconciliation`).
8. Contention-first benchmarks — complete (`phase-8-benchmarks`).

## Quickstart

The completed v1 stack runs locally through Docker Compose. From the repository
root:

```text
make up
make migrate
curl http://localhost:8081/readyz
curl http://localhost:8082/readyz
curl http://localhost:8083/readyz
curl http://localhost:8084/readyz
```

The service health endpoints are:

| Service | URL |
| --- | --- |
| wallet-source | <http://localhost:8081/readyz> |
| wallet-destination | <http://localhost:8082/readyz> |
| transfer | <http://localhost:8083/readyz> |
| projector | <http://localhost:8084/readyz> |

Stop the local stack with `make down`. The Compose credentials are local-development values only; do not reuse them outside the local stack.

## Verification and benchmarks

Run the full repository gate:

```text
make ci
```

Run the Phase 8 benchmark harness against the running stack:

```text
make benchmark ARGS="-ops=50 -concurrency=16 -repeats=3"
```

The benchmark command updates [`docs/benchmarking.md`](docs/benchmarking.md)
and writes machine-readable results to
[`.ecc/benchmarks/phase8.json`](.ecc/benchmarks/phase8.json). The benchmark
module is outside the four-module workspace, so its standalone checks are:

```text
cd benchmarks
GOWORK=off go test ./...
GOWORK=off go vet ./...
```

For a route-level view, start with [`docs/api.md`](docs/api.md). For operating
the local stack or recovering from an ambiguous transfer, use
[`docs/operations.md`](docs/operations.md) and
[`docs/reconciliation.md`](docs/reconciliation.md).

## Phase 1 acceptance (historical)

- All required contract documents and ADRs exist.
- Required Mermaid diagrams are included in the relevant documents.
- Public and internal endpoint shapes, idempotency semantics, event ordering, saga failure handling, and projection rebuild protocol are written down before implementation.
- The Phase 1 contract commits intentionally contained no application dependencies or generated code.

## Phase 2 foundation

- The Go workspace contains the shared contract module and three service modules.
- Local Compose starts one PostgreSQL server with four isolated databases, NATS JetStream, two wallet instances, transfer, and projector.
- Runtime readiness probes PostgreSQL and NATS; migrations are explicit and wallet tables remain deferred to Phase 3.

## Phase 2 acceptance

- `make ci` passes from the clean repository tree.
- Compose starts PostgreSQL, NATS JetStream, both wallet instances, transfer, and projector; all service `/readyz` endpoints report healthy.
- The transfer role cannot connect to either wallet database, and the boundary checker rejects a deliberate forbidden import.

## Phase 3 ledger acceptance

- Wallet balances are authoritative sums over immutable postings; no mutable balance row exists.
- Deposits, withdrawals, duplicate idempotency keys, tenant scoping, deferred balancing, immutable-table triggers, event/outbox writes, replay checks, and concurrent withdrawal safety are covered by real PostgreSQL tests.
- `make sqlc` generates the committed pgx storage package and `make sqlc-check` verifies freshness.
- A dedicated migrator owns service databases and migration-created objects; runtime service roles have minimum database/table grants and cannot alter journal schema.
- Wallet idempotency records are append-only with transaction-scoped same-key serialization, and wallet `/readyz` keeps NATS degradation distinct from PostgreSQL failure.
