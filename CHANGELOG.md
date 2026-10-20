# Changelog

All notable changes to this project are documented here.

## [Unreleased]

## [Phase 4 — Outbox relay and ordered consumption] — 2026-10-20

### Added

- Added leased outbox relay worker in `services/wallet/internal/relay` with SKIP LOCKED claim transactions, deterministic backoff jitter, retry tracking, and column-level update grants.
- Added file-backed JetStream WALLET_EVENTS stream bootstrap and durable projector pull consumer configuration with explicit AckWait, MaxDeliver, and FilterSubject.
- Added pure aggregate version decision state machine in `shared/ordering` (InOrder, DuplicateOrStale, FutureBuffer, GapFillDrain).
- Added transactional ordered consumer runner and gap detection sweeper in `services/projector/internal/consumer` with durable PostgreSQL tables (`inbox_messages`, `aggregate_cursors`, `buffered_events`, `ordering_gaps`, `dead_letter_events`, `applied_events_log`).
- Added internal stats endpoints `GET /internal/v1/stats` on wallet and projector services for outbox/lag/gap/dead-letter observability.
- Added randomized ordering property tests and complete failure injection integration test suite covering 10 scenarios across outbox relay, consumer ordering, gap sweeping, dead-letter recording, and end-to-end ledger/projector totals.

### Phase 3 hardening

- Added dedicated migrator ownership and minimum runtime database grants, including append-only idempotency records and advisory-lock serialization.
- Expanded the role-isolation matrix to cover cross-database access, DDL/trigger changes, table drops, and forbidden updates.
- Made wallet readiness distinguish hard PostgreSQL failure from degraded NATS availability.
- Added deferred-posting and contiguous wallet-event-version regression coverage.

## [Phase 3 — Immutable wallet ledger] — 2026-10-19

### Added

- Added tenant-scoped wallet/accounts, USD journal postings, immutable events, transactional outbox rows, and idempotency records.
- Added database-enforced append-only triggers, deferred double-entry checks, composite tenant validation, account-row locking, bounded serialization retries, and no mutable balance table.
- Added sqlc-generated pgx storage, wallet domain logic, authoritative wallet HTTP endpoints, keyset entry pagination, and real PostgreSQL integration/property tests.
- Added a measured 100,000-posting authoritative SUM read result to the benchmark plan.
- Added a deferred-posting validation optimization that preserves append-time invariant checks without rescanning a large batch for every posting.

### Verification

- `make ci` passed with sqlc freshness, the extended role matrix, unit/race tests, and the Compose-backed integration suite.
- Wallet service manual flow passed for create, deposit, withdraw, authoritative balance, and journal entries; a 100,000-posting SUM read measured 18.098 ms in the documented local run.
- Phase 4 relay/consumer work remains deferred.

## [Phase 2 — Repository foundation] — 2026-10-19

### Added

- Added the four-module Go workspace and shared UUIDv7, integer-money, event-envelope, and HTTP-error contracts.
- Added service configuration validation, structured request logging, request/correlation IDs, graceful shutdown, `/healthz`, and dependency-aware `/readyz` endpoints.
- Added Docker Compose for PostgreSQL, NATS JetStream, two wallet instances, transfer, and projector, with service-specific database roles and baseline migrations.
- Added import-boundary checks, deliberate-violation coverage, role-isolation integration coverage, Make targets, and GitHub Actions CI.

### Verification

- `make ci` passed: documentation rendering/OpenAPI lint, builds, tests, race tests, vet, formatting/shell lint, boundary checks, and role-isolation integration check.
- `docker compose -f deploy/docker-compose.yml up -d --build` passed; PostgreSQL and NATS became healthy and all four service containers became healthy.
- `curl http://localhost:8081/readyz`, `:8082`, `:8083`, and `:8084` each returned HTTP 200 with PostgreSQL and NATS checks `ok`.
- `make migrate` applied the empty baseline migration to all four databases.
- Business tables and migration content remain deferred to later phases.

## [Phase 1 — Contracts and documentation] — 2026-10-19

### Added

- Added the architecture, ledger, event, saga, outbox/inbox, projection, reconciliation, data model, API, testing, benchmarking, and operations contracts.
- Added eight ADRs covering the locked architecture decisions.
- Added Mermaid diagrams for all required system flows and data ownership boundaries.
- Added the initial OpenAPI contract for public and internal endpoints.

### Verification

- `git diff --check` passed for every staged documentation unit.
- OpenAPI YAML parsed successfully with Ruby (`13` paths); a structural documentation check confirmed all required files, Mermaid blocks, diagram constructs, and balanced fences.
- A Mermaid CLI renderer was not installed in the environment, so visual rendering was not claimed; no Go checks apply because Phase 1 contains no Go code.
