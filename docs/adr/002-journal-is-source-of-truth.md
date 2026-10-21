# ADR 002: Journal is the only balance source of truth

Status: accepted
- **Date:** 2026-10-19

## Context

Mutable balance rows are fast but can diverge from the history that produced them. Financial recovery requires deterministic replay and an auditable append-only record.

## Decision

Wallet writes append balanced journal transactions, postings, a domain event, and an outbox row in one database transaction. They never update a mutable balance row. Authoritative balance reads sum the journal. The projector owns disposable `wallet_balance_view` read models.

## Consequences

- Replays can reproduce balances and detect corruption.
- Authoritative reads cost more than a single row lookup and need indexes/controlled queries.
- Read models can be deleted, rebuilt, checksum-validated, and promoted independently.

## Alternatives considered

- **Mutable balance as authority:** rejected because a missed update can hide monetary divergence.
- **Event store only without relational journal postings:** rejected because SQL constraints and explicit double-entry queries are useful safety mechanisms.
