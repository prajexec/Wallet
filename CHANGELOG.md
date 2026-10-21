# Changelog

All notable changes to this project are documented here.

## [Unreleased]

### Documentation

- Added a source-grounded product requirements document for the completed v1
  wallet platform.
- Added a project-status and acceptance-evidence page plus an indexed ADR
  catalog.
- Updated README, API, and operations documentation for the finished transfer,
  reconciliation, benchmark, and internal-listener surfaces.

## [Phase 8 — Contention-first benchmarks] — 2026-10-21

### Added

- Added executable contention-first benchmark harness in `benchmarks/` with CLI runner supporting `-ops`, `-concurrency`, `-repeats`, and `-workloads`.
- Implemented all six workloads defined in `docs/benchmarking.md`: `hot-wallet-withdrawals`, `even-wallet-deposits`, `transfer-saga`, `outbox-relay`, `projection-catch-up`, and `reconciliation`.
- Added service-level telemetry for outbox duplicate publish rate on wallet `/internal/v1/stats`.
- Added `/internal/v1/stats` endpoint to transfer service exposing total steps and aggregate step retry counts.
- Added Makefile target `make benchmark ARGS="..."`.
- Executed full 3-repeat contention benchmark run (50 ops, concurrency 16) with zero errors and recorded results in `.ecc/benchmarks/phase8.json` and `docs/benchmarking.md`.

## [Phase 7 — Reconciliation and operations tooling] — 2026-10-21

### Added

- Added immutable reconciliation-exception lifecycle fields and append-only investigation notes.
- Added wallet and transfer operator CLIs for authoritative balance inspection, journal replay checks, saga inspection, exception handling, and outbox inspection.
- Added explicit reconciliation decision logic that queries stable wallet operation IDs and never adjusts balances automatically.
- Added journal balancing and cross-wallet conservation check helpers.

## [Phase 6 — Transfer saga and cross-wallet orchestration] — 2026-10-21

### Added

- Added durable transfer saga state machine in `services/transfer/internal/saga` orchestrating multi-phase wallet transfers across source and destination wallets (`CREATED` -> `RESERVED` -> `PENDING_CREDITED` -> `CAPTURED` -> `COMPLETED`).
- Added internal wallet transfer primitives on `INTERNAL_HTTP_ADDR` guarded by `X-Internal-Token`: reservations, pending credits, captures, finalizations, releases, compensations, and operation status lookup (`/internal/v1/wallets/{wallet_id}/*` and `/internal/v1/operations/{operation_id}`).
- Added wallet hold tracking (`wallet_holds`) where active reservations deduct available balance (`available = posted - holds`) without polluting double-entry journal postings until capture.
- Added wallet pending credits (`wallet_pending_credits`) holding incoming transfer funds unspendable until destination finalization.
- Added wallet double-entry journal capture (`DEBIT user_wallet`, `CREDIT outbound_clearing`) and finalization (`DEBIT inbound_clearing`, `CREDIT user_wallet`), plus idempotent compensation reversals (`DEBIT outbound_clearing`, `CREDIT user_wallet`).
- Added transfer service HTTP API exposing `POST /v1/transfers` (with `Idempotency-Key` and `X-Tenant-ID` validation, returning HTTP 202 Accepted) and `GET /v1/transfers/{transfer_id}`.
- Added typed `walletclient` with response classification (`Success`, `Transient`, `Permanent`, `Ambiguous`) preventing premature compensation upon ambiguous timeouts.
- Added durable reconciliation exception queue (`reconciliation_exceptions`) when step outcomes are ambiguous or timeouts occur.
- Added comprehensive unit and integration test coverage with `-race -count=1` for transfer saga transitions, wallet hold mechanics, idempotency replay, and failure compensation.

### Architecture

- Transfer service coordinates cross-wallet movement without direct cross-database queries, interacting exclusively through internal token-protected HTTP endpoints on isolated networks.
- Holds and pending credits remain state-only entries until captured or finalized, maintaining ledger immutability and preventing false balance mutations.
- Ambiguous outcomes (e.g. timeouts after dispatch) transition transfers to `RECONCILIATION_REQUIRED` and queue operator exceptions rather than triggering blind compensation.

### Verification

- Verified via `make docs-check build test test-race vet lint check-boundaries role-isolation test-integration`.
- All integration suites passed with `-race -count=1` against real PostgreSQL databases (`wallet_source_test`, `wallet_destination_test`, `transfer_test`, `read_test`).
- End-to-end live transfer flow executed on Docker Compose stack: source wallet creation, $100 deposit, destination wallet creation, $35 transfer completed by saga, final source balance $65, final destination balance $35, and transfer idempotency replay returned HTTP 202 with `Idempotency-Replayed: true`.

## [Phase 5 — CQRS projections] — 2026-10-21

### Added

- Added payload-only wallet projection folds for balances and immutable entry history, including aggregate-version watermarks, unknown-event no-op advancement, unsupported-schema rejection, and overflow/negative-balance validation.
- Added dedicated read-database projection tables with fixed `a`/`b` slots, an atomic `projection_versions` pointer, rebuild metadata, and restricted projector-role grants. Runtime DDL is not used for rebuilds.
- Added projector read APIs for active-slot balances and keyset-paginated entries. Responses expose `projection_slot`, `read_model_consistent_through`, and `authoritative: false`; balance reads support bounded `min_version`/`wait_ms` read-your-writes waits.
- Added enriched wallet command/event contracts with `aggregate_version`, authoritative wallet verification pages, and cross-tenant verification support for projection validation.
- Added `projectorctl` rebuild, status, promote, rollback, retire, and development-only active-slot wipe operations.

### Architecture

- Projector consumers run independently for slots `a` and `b` (`projector-balance-a` and `projector-balance-b`) while public reads resolve the active slot in the same SQL statement as the view query.
- Rebuilds clear and replay the inactive slot from persisted projector event envelopes, compare candidate balances and versions with wallet verification snapshots, and record `READY`/`FAILED` evidence without repairing mismatches automatically.
- Wallet source and destination remain the same binary with separate databases and roles; projector code consumes event payloads and never queries wallet databases directly.

### Verification

- `INTERNAL_HTTP_ADDR=127.0.0.1:0 make ci` passed with Mermaid/OpenAPI checks, builds, unit tests, race tests, vet, lint, sqlc freshness, import-boundary checks, live role isolation, and Compose-backed integration tests.
- Compose end-to-end flow passed after `make up && make migrate`: wallet creation returned `aggregate_version: 1`; a 5,000-minor-unit deposit returned `aggregate_version: 2`; the projector returned balance `5000`, `projection_slot: "a"`, and `read_model_consistent_through: 2`.
- Read-your-writes passed with `min_version=2`, returning HTTP 200 with watermark `2` and the projected balance `5000`.
- `projectorctl status` reported `NONE` before the demo rebuild. Rebuild verification targeted inactive slot `b` and returned `READY`; status recorded the rebuild and the active pointer remained `a|b`, proving active reads continued from slot `a` with balance `5000` and watermark `2`.
- The projector integration suite includes an isolated balance/entry fold test and runs with dedicated test databases, `WALLET_EVENTS_TEST`, and `events-test` subjects.

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
