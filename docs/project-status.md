# Project status

## Current state

The eight-phase v1 roadmap is complete in the current checkout. `main` points to
the immutable `phase-8-benchmarks` tag, and the working tree is expected to remain
clean after verification.

| Phase | Tag | Commit in this checkout | Scope |
| ---: | --- | --- | --- |
| 1 | `phase-1-contracts` | `77d5a31` | Contracts, schemas, ADRs, and Mermaid specifications |
| 2 | `phase-2-foundation` | `4cce1a3` | Local Compose stack, migrations, and health/readiness endpoints |
| 3 | `phase-3-ledger` | `757d504` | Immutable double-entry journal and authoritative balances |
| 4 | `phase-4-outbox` | `3f6278f` | Transactional outbox relay and ordered event consumption |
| 5 | `phase-5-projections` | `054f0a1` | CQRS read models, ordering, and dual-slot rebuilds |
| 6 | `phase-6-transfer` | `629ce60` | Cross-wallet transfer saga and wallet transfer primitives |
| 7 | `phase-7-reconciliation` | `a7b2035` | Reconciliation lifecycle and operator tooling |
| 8 | `phase-8-benchmarks` | `369a7f5` | Contention-first benchmark harness and recorded results |

## Verification gate

Run the repository gate from the root:

```text
make ci
```

The gate covers:

- documentation rendering and OpenAPI lint;
- build, unit tests, race tests, vet, and formatting checks;
- sqlc freshness;
- package import boundaries;
- database role isolation;
- real PostgreSQL and NATS integration suites using isolated test databases and
  subjects.

The standalone benchmark module is checked separately because it is intentionally
outside the four-module Go workspace:

```text
cd benchmarks
GOWORK=off go test ./...
GOWORK=off go vet ./...
```

## Phase 8 evidence

The recorded benchmark run contains three runs for each of the six workloads:

- hot-wallet withdrawals;
- even-wallet deposits;
- transfer saga;
- outbox relay;
- projection catch-up;
- reconciliation.

The machine-readable result set is
`.ecc/benchmarks/phase8.json`; the human-readable summary is
[`benchmarking.md`](benchmarking.md). The results are local diagnostic evidence,
not production SLOs. They must always be interpreted together with the
correctness checks and the pinned commit/configuration described in the report.

## Known product boundaries

Completion of the repository roadmap does not imply that the system includes:

- public authentication or authorization;
- KYC/AML or regulated customer onboarding;
- external funding or payout rails;
- production secrets management, multi-region disaster recovery, or Kubernetes;
- formal regulatory retention and audit-export policy.

Those are future product and platform decisions, not undocumented promises of
the completed v1.

## Documentation ownership

- Product scope and requirements: [`product-requirements.md`](product-requirements.md)
- Architecture and service ownership: [`architecture.md`](architecture.md)
- HTTP contract: [`api.md`](api.md) and [`../api/openapi.yaml`](../api/openapi.yaml)
- Operational procedures: [`operations.md`](operations.md)
- Financial invariants: [`ledger-invariants.md`](ledger-invariants.md)
- Event and delivery behavior: [`event-model.md`](event-model.md) and
  [`outbox-inbox.md`](outbox-inbox.md)
- Transfer lifecycle: [`saga.md`](saga.md)
- Reconciliation lifecycle: [`reconciliation.md`](reconciliation.md)
- Performance evidence: [`benchmarking.md`](benchmarking.md)
- Architectural decisions: [`adr/README.md`](adr/README.md)
