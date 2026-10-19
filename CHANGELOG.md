# Changelog

All notable changes to this project are documented here.

## [Unreleased]

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
