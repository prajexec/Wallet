# ADR 012: Duplicate HTTP middleware at service boundaries

Status: accepted
- **Date:** 2026-10-19

## Context

The three services currently need the same small HTTP foundation: request and correlation IDs, structured request logging, health endpoints, and graceful shutdown. The shared module is intentionally narrow and is limited to events, money, identifiers, and errors. Moving runtime middleware into it would create a broader cross-service dependency before the public and internal HTTP contracts have stabilized.

## Decision

Keep the HTTP middleware and health-server implementation duplicated inside each service's `internal/server` package. Each service owns its configuration and lifecycle wiring. Revisit this decision when at least two of the following are true:

1. the HTTP behavior has remained contract-compatible through the transfer and projector phases;
2. the duplicated implementation has accumulated a measured defect or security drift;
3. a shared package can expose a narrow, service-agnostic API without importing service configuration, storage, or domain code;
4. the reduction in maintained code is greater than the new module coupling and release coordination cost.

Any extraction requires a new ADR or an amendment to this one, plus boundary and race-test evidence for all services.

## Consequences

- Phase 3 can evolve wallet request behavior without changing the other service modules.
- Security fixes must be applied in three locations until the revisit condition is met.
- Repetition is visible and testable rather than hidden behind a broad shared runtime package.

## Alternatives considered

- **Shared `httpserver` package:** deferred because it would widen the locked narrow shared module.
- **Third-party router/middleware framework:** rejected for the current health-only foundation and dependency budget.
- **Copy only a generated template:** rejected because generated runtime code would obscure ownership and complicate debugging.
