# ADR 007: Enforced module and database boundaries

- **Status:** Accepted
- **Date:** 2026-10-19

## Context

The transfer service must orchestrate wallet behavior without becoming a hidden central ledger through direct table access. Shared code should remain narrow and contracts should be portable.

## Decision

Use a Go workspace with `services/wallet`, `services/transfer`, `services/projector`, and a narrow `shared` module for events, money, identifiers, and errors. Use separate PostgreSQL runtime roles per service plus a dedicated migrator owner role. Add CI that fails when the transfer module imports wallet storage packages.

## Consequences

- Cross-service behavior uses internal HTTP and events rather than shared repositories.
- Some types are intentionally duplicated at boundaries or generated from contracts.
- CI, database grants, and object ownership become part of correctness, not just style.

## Alternatives considered

- **One Go module with unrestricted internal imports:** rejected because package visibility alone is too easy to bypass.
- **Shared ORM/domain repository:** rejected because it couples service ownership and violates the no-central-ledger boundary.
