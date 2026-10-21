# ADR 014: sqlc-generated wallet storage

Status: accepted
- **Date:** 2026-10-19

## Context

The wallet repository needs tenant-scoped SQL for journal writes, idempotency, balances, and keyset history queries. ORM behavior would obscure transaction boundaries and make the financial SQL harder to review.

## Decision

Use sqlc with PostgreSQL and `pgx/v5` to generate the wallet query types and methods from checked-in SQL queries. Generated code is committed, `make sqlc` regenerates it, and CI runs generation followed by a clean-tree check. Repository orchestration remains handwritten so lock ordering, retries, and the one-transaction event/outbox contract stay visible.

## Consequences

- SQL remains reviewable and compile-time checked without an ORM.
- Generated files must be kept fresh and should not be edited manually.
- Query changes require the sqlc toolchain and database schema contract to be available.

## Alternatives considered

- **ORM:** rejected because implicit writes and transaction behavior are unsuitable for the immutable journal boundary.
- **Handwritten `database/sql` everywhere:** rejected because query result and parameter drift would be caught later than compile time.
- **Runtime schema introspection:** rejected for reproducible builds and CI determinism.
