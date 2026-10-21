# ADR 004: Transactional outbox, inbox deduplication, and aggregate ordering

Status: accepted
- **Date:** 2026-10-19

## Context

Database commit and broker publish cannot be one local transaction. Relays may publish duplicates, consumers may crash before ACK, and NATS does not provide a business-level global FIFO guarantee.

## Decision

Commit domain state and outbox atomically. Relay rows using `FOR UPDATE SKIP LOCKED`; publish every event with `Nats-Msg-Id = event_id`. Consumers store `(tenant_id, consumer_name, event_id)` in an inbox and apply only the next aggregate version. Future versions are buffered; persistent gaps alert operators.

## Consequences

- Duplicates are expected and safe rather than exceptional.
- Consumers need inbox, cursor, buffer, and gap-monitoring tables.
- There is no claim of global event ordering; ordering is per tenant aggregate.

## Alternatives considered

- **Publish in the request transaction:** rejected because a broker failure would make commit semantics ambiguous.
- **Global FIFO relay lock:** rejected because it reduces availability and is unnecessary for per-aggregate ordering.
- **Consumer-only dedup in memory:** rejected because a process restart would repeat side effects.
