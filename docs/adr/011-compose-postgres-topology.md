# ADR 011: One local PostgreSQL container with isolated databases

- **Status:** Accepted
- **Date:** 2026-10-19

## Context

Local development needs four durable databases without requiring four database servers. The service boundary still must prevent transfer access to wallet schemas.

## Decision

Docker Compose runs one PostgreSQL container with `wallet_source`, `wallet_destination`, `transfer`, and `read` databases. Initialization creates a dedicated `migrator_role` as the owner of every database and creates `wallet_source_role`, `wallet_destination_role`, `transfer_role`, and `projector_role` as non-owner runtime roles. Public `CONNECT` is revoked from service databases, and each runtime role receives only its own database connection plus migration-defined object grants.

## Consequences

- Local startup is lightweight and reproducible.
- A local PostgreSQL outage affects all services, which is acceptable for development only.
- Database isolation and owner separation are enforced by database privileges in addition to Go import boundaries; production may use separate server instances without changing service contracts.

## Alternatives considered

- **Four PostgreSQL containers:** stronger process isolation but heavier local resource use and slower startup.
- **One shared database/schema:** rejected because it makes cross-service table access too easy.
- **One central database role:** rejected because role separation is part of the boundary contract.
