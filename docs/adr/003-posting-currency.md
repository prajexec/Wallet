# ADR 003: Currency belongs on every posting

Status: accepted
- **Date:** 2026-10-19

## Context

Putting currency only on a transaction permits an accidental mixed-currency posting and makes future multi-currency validation ambiguous. v1 is intentionally USD-only but the data model should not encode a weaker invariant.

## Decision

Every posting stores `currency`; every balanced transaction validates debit and credit totals per currency. v1 accepts only `USD`, with integer minor units.

## Consequences

- A future currency can be introduced with explicit rules rather than a silent schema reinterpretation.
- Every API/event/posting carries currency even when it is always USD in v1.
- A transaction cannot balance across different currencies.

## Alternatives considered

- **Currency only on journal transaction:** rejected because it loses a posting-level invariant.
- **Floating-point decimal values:** rejected because binary floating point is unsafe for money.
