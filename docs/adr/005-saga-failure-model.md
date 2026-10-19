# ADR 005: Explicit saga failure classification

- **Status:** Accepted
- **Date:** 2026-10-19

## Context

A timeout does not prove that a remote side effect failed. Treating every error as compensation can create a second monetary effect. Treating every error as retryable can repeat a permanent failure.

## Decision

Classify failures as transient, permanent after confirmed side effect, or ambiguous. Transient failures use bounded retries with step timeouts and backoff. Confirmed side effects that cannot continue enter `COMPENSATION_PENDING`, then an idempotent, rate-limited reversal. Ambiguous outcomes enter `RECONCILIATION_REQUIRED` with a durable exception and no automatic compensation until status is known.

## Consequences

- The saga has more visible states and operational tooling.
- Operation IDs and status lookup endpoints are mandatory.
- Operators can see why money movement stopped instead of relying on a best-effort guess.

## Alternatives considered

- **Always retry:** rejected for permanent errors and duplicate side effects.
- **Always compensate after timeout:** rejected because the original request may have committed.
- **Distributed transaction/2PC:** rejected for the locked service architecture and operational complexity.
