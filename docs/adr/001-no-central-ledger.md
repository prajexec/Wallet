# ADR 001: No central ledger

Status: accepted
- **Date:** 2026-10-19

## Context

Wallet accounts must remain owned by their wallet service instance. A cross-service transfer needs coordination without making a central ledger the source of truth or a single availability bottleneck.

## Decision

Run one wallet binary as two logical instances, `wallet-source` and `wallet-destination`, with separate PostgreSQL databases. Each instance owns its accounts and journal. The transfer service coordinates them through a saga and never accesses wallet schemas.

## Consequences

- Cross-database atomicity is replaced by explicit reserve, pending-credit, capture, finalize, compensation, and reconciliation states.
- Service boundaries are enforceable with database roles, Go module boundaries, and CI import checks.
- Global balances require reconciliation evidence rather than a central transaction.

## Alternatives considered

- **Central ledger service:** rejected because it violates ownership isolation and becomes a central availability/throughput dependency.
- **Shared wallet database:** rejected because it weakens service ownership and makes accidental cross-service writes easy.
- **Distributed transaction coordinator:** rejected for v1; saga outcomes are explicit and operationally inspectable.
