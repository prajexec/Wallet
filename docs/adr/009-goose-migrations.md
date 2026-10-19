# ADR 009: Goose for service migrations

- **Status:** Accepted
- **Date:** 2026-10-19

## Context

Each service owns a separate database or logical database boundary. Phase 2 needs repeatable migration directories and Make targets before business tables are introduced.

## Decision

Use Goose CLI pinned by `GOOSE_VERSION` in the Makefile. Keep migration directories per service boundary: wallet migrations are applied independently to source and destination databases; transfer and projector/read have their own directories. Phase 2 contains empty baseline migrations.

## Consequences

- Migration history is explicit and reviewable in Git.
- Local setup requires the pinned Goose command to download or use its Go module cache.
- Business migrations must be run against the correct service role/database and cannot assume cross-database transactions.

## Alternatives considered

- **golang-migrate:** viable, but Goose was chosen for its simple SQL-first CLI and embedded version table workflow.
- **Application startup migrations:** rejected because startup order and role privileges should not silently mutate durable schemas.
- **One shared migration directory:** rejected because it weakens service ownership.
