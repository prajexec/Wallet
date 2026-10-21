# ADR 010: Standard-library HTTP server

Status: accepted
- **Date:** 2026-10-19

## Context

Phase 2 needs small health endpoints, request metadata middleware, structured logging, and graceful shutdown without adding a framework before the API surface exists.

## Decision

Use `net/http` and `http.ServeMux` with method-aware patterns, `log/slog`, and `os/signal` for lifecycle handling. Use pgx's `database/sql` adapter and nats.go only for the required PostgreSQL/NATS protocol clients.

## Consequences

- The foundation has no router framework dependency and is portable across service modules.
- Common HTTP middleware is intentionally duplicated at service boundaries until a narrower shared runtime contract is justified.
- pgx and nats.go are the only non-standard runtime dependencies in Phase 2.

## Alternatives considered

- **chi:** deferred until public/internal routing needs exceed the standard library.
- **Gin/Echo:** rejected as unnecessary framework weight for health-only skeletons.
- **TCP-only dependency checks:** rejected because readiness must validate the actual PostgreSQL and NATS clients, not only open ports.
