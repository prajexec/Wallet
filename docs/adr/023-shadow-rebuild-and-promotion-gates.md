# ADR 023: Shadow Consumer Rebuild and Zero-Data-Loss Promotion Gates

Status: accepted
- **Date:** 2026-10-20
- **Deciders:** Digital Wallet Core Team
- **Consulted:** Architecture, Reliability, Operations
- **Informed:** All Engineering

## Context and Problem Statement

When rebuilding projection read models (due to new business projection fields, corruption recovery, or schema version upgrades), active live traffic continues to write new events. Replaying historical events must not interfere with current reads, must not duplicate or lose events occurring during the rebuild, and must not promote an unhealthy or incomplete candidate projection.

## Decision Drivers

- Live read traffic must continue uninterrupted from the active projection slot.
- Live write events arriving during the rebuild must be captured and applied without race conditions.
- Strict promotion gates must guarantee that the candidate slot is completely caught up, defect-free, and verified before promotion.
- JetStream event retention must accommodate full replay from aggregate sequence 1.

## Decision Outcome

Chosen Solution: **Shadow Consumer Rebuild with Four Strict Promotion Gates**.

### 1. Unlimited Stream Retention
The `WALLET_EVENTS` JetStream stream retention policy is configured with `MaxAge = 0` (unlimited retention on file storage), allowing consumers to request `DeliverAll`. This is asserted programmatically during stream bootstrap.

### 2. Shadow Consumer Replay
1. The rebuild coordinator wipes the inactive candidate slot rows in `wallet_balance_view` and `wallet_entry_view`.
2. A durable pull consumer for the candidate slot (`projector-balance-<slot>`) is created with `DeliverAll`.
3. The standard transactional ordered runner processes events from sequence 1 into the candidate slot.
4. When historical events are caught up, the candidate consumer continues consuming live events alongside the active slot consumer.

### 3. Promotion Gates
All four gates must be satisfied before promotion can proceed:
1. **Backlog Gate:** Candidate consumer backlog is zero (`pending == 0`, `ack_pending == 0`, `buffered_events == 0`) and has remained zero throughout a 2-second stability quiet period.
2. **Defect Gate:** Candidate consumer has zero open gaps (`open_gap_count == 0`) and zero dead letters (`dead_letter_count == 0`).
3. **Freshness Gate:** Full cross-tenant verification against authoritative wallet instances passed within the last 30 seconds.
4. **Concurrency Lock:** Only one rebuild process can run at any given time (enforced via `projection_rebuilds` status check and row locking).

### 4. Post-Promotion Grace Window
Following promotion, the previous slot consumer is kept running for a grace window (e.g. 10 minutes) to allow immediate, zero-downtime rollback via `projectorctl rollback` if an operational issue is discovered. Explicit decommissioning occurs via `projectorctl retire`.

## Consequences

### Positive
- Rebuilds run completely online without read downtime or locking active tables.
- Promotion is blocked if any gap, dead letter, or divergence exists.
- Instant rollback is available throughout the grace window.

### Negative
- Running two live consumers concurrently during rebuild and grace window increases database I/O and JetStream consumption.
