# ADR 021: Payload-Only CQRS Projections and Schema Evolution

- **Status:** Accepted
- **Date:** 2026-10-20
- **Deciders:** Digital Wallet Core Team
- **Consulted:** Architecture, Security, Data Engineering
- **Informed:** All Engineering

## Context and Problem Statement

Service boundaries strictly prohibit the projector service from connecting to or querying wallet database tables (enforced by network isolation, distinct database credentials, and boundary check scripts). Therefore, the projector must construct all read-model projections (`wallet_balance_view` and `wallet_entry_view`) exclusively from the event payloads delivered via JetStream. We must ensure event payloads are complete, self-contained, and safely handle additive schema evolution and unrecognized events.

## Decision Drivers

- Enforce strict database isolation: no cross-database queries or backchannel lookups by the projector.
- Support additive schema changes over time without breaking running projection consumers.
- Prevent data corruption caused by consumers guessing the meaning of unsupported payload formats.
- Ensure event payloads carry sufficient metadata for both balance calculation and human-readable ledger inspection.

## Decision Outcome

Chosen Solution: **Payload-Only Projections with Strict Schema Version Gating**.

### 1. Payload Audit and Additions
The `wallet.deposit_posted` and `wallet.withdrawal_posted` domain events are enriched with:
- `wallet_id`: UUIDv7 aggregate identifier;
- `journal_transaction_id`: UUIDv7 transaction reference;
- `amount_minor`: positive integer minor units;
- `currency`: ISO 4217 code (`USD`);
- `side`: `"CREDIT"` or `"DEBIT"`;
- `entry_type`: business classification (e.g. `"DEPOSIT"`, `"WITHDRAWAL"`);
- `reference`: optional external transaction reference string.

### 2. Unrecognized Event Types
If the projector consumer encounters a valid envelope with an unrecognized `event_type` (for example, future events or administrative annotations), the consumer:
1. Logs a telemetry metric;
2. Skips view mutation;
3. Safely advances the aggregate cursor to prevent gap alarms.

### 3. Schema Version Evolution
If an event payload presents a `schema_version` strictly greater than the maximum version supported by the running projection worker:
1. The consumer never guesses or partially applies the event;
2. The event is immediately diverted to `dead_letter_events` with an explicit incompatibility error;
3. Processing for that aggregate pauses (preserving ordering) until operators deploy an updated projector binary that understands the new schema version.

## Consequences

### Positive
- Strict physical separation between write databases and read databases is preserved.
- Projections can be rebuilt entirely from event streams without wallet database access.
- Safe failure modes for future event types and schema evolutions.

### Negative
- Wallet events carry slightly larger payloads (reference strings and posting sides).
