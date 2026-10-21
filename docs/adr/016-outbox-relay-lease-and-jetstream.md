# ADR 016: Leased outbox relay and idempotent JetStream bootstrap

Status: accepted
- **Date:** 2026-10-19

## Context

The outbox must publish without holding PostgreSQL row locks across a network call. Multiple relays are expected, and a process can crash after publish but before marking the row delivered.

## Decision

Use a short claim transaction with `FOR UPDATE SKIP LOCKED`, ordered by `(created_at, id)`, that sets `locked_until`. Publish outside the transaction with a bounded timeout, `Nats-Msg-Id = event_id`, and wait for PubAck. Success sets `published_at`; failure clears the lease, records `last_error`, increments `attempts`, and schedules exponential backoff with jitter. An expired lease can be reclaimed. Configure the file-backed `WALLET_EVENTS` stream and durable explicit-ACK pull consumer idempotently at startup.

## Consequences

- No network call holds a database row lock.
- A crash after publish causes at-least-once redelivery; JetStream duplicate suppression and consumer inbox dedup make it safe.
- Lease duration must exceed the bounded publish timeout and is operationally observable.
- There is no global FIFO guarantee; ordering is per tenant aggregate in the consumer.

## Alternatives considered

- **Hold row locks during publish:** rejected because NATS outages would block database writers.
- **Delete after publish:** rejected because a crash would lose delivery evidence.
- **Process-local relay mutex:** rejected because multiple service processes must coordinate through PostgreSQL.
