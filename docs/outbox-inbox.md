# Outbox and inbox

## Transactional outbox flow

The wallet or transfer write transaction stores business state, the domain event, and the outbox row in one PostgreSQL transaction. The relay publishes committed outbox rows to NATS JetStream using `Nats-Msg-Id: event_id`.

```mermaid
sequenceDiagram
    autonumber
    participant API as Service API
    participant DB as PostgreSQL
    participant O as Outbox relay
    participant N as NATS JetStream
    participant C as Consumer
    participant I as Inbox / projection DB

    API->>DB: BEGIN
    API->>DB: append journal/aggregate + domain event
    API->>DB: INSERT outbox(event_id, payload, status=pending)
    API->>DB: COMMIT
    O->>DB: short claim tx: SELECT ... SKIP LOCKED
    DB-->>O: claim rows and set locked_until
    O->>DB: COMMIT claim
    O->>N: publish(subject, Nats-Msg-Id=event_id)
    N-->>O: publish ACK
    O->>DB: mark published_at / clear lease
    N->>C: deliver event
    C->>I: BEGIN and insert inbox key
    C->>I: apply projection and cursor atomically
    C->>I: COMMIT
    C-->>N: ACK

    Note over O,N: relay crashes after publish
    O--xDB: no mark delivered
    N->>C: redelivery of same event_id
    C->>I: inbox unique key already exists
    C-->>N: ACK without monetary effect
```

There is no global FIFO guarantee. Multiple relays may claim rows concurrently. The event's aggregate ID and version, the inbox, and consumer ordering logic provide correctness.

## Relay algorithm

1. Start a short claim transaction.
2. Select `published_at IS NULL` and `next_attempt_at <= now()` rows where `locked_until IS NULL OR locked_until < now()`, with `FOR UPDATE SKIP LOCKED`, ordered by `created_at, id`.
3. Set `locked_until = now() + relay lease` and commit. No row lock crosses a network call.
4. Publish with `Nats-Msg-Id = event_id` and wait for the JetStream PubAck.
5. On PubAck, set `published_at` and clear `locked_until`. On failure, increment `attempts`, store `last_error`, clear the lease, and schedule exponential backoff with bounded jitter.
6. Do not delete the row; retain delivery metadata for inspection and replay.

The lease is an operational coordination field, not the correctness mechanism. If a process dies after publish and before marking delivered, the lease expires and the event is republished with the same `Nats-Msg-Id`; JetStream duplicate suppression and the consumer inbox make this safe. A publish timeout is not treated as proof that the message was absent.

The relay uses a bounded publish timeout shorter than the lease. Two relays can claim different rows concurrently; `SKIP LOCKED` prevents the same non-expired lease from being claimed twice.

## Consumer algorithm and gap detection

```mermaid
flowchart TD
    Receive[Receive event] --> Inbox{Inbox key already exists?}
    Inbox -->|yes| AckDuplicate[Record duplicate metric and ACK]
    Inbox -->|no| Version{event version vs current}
    Version -->|current + 1| Apply[Insert inbox + apply projection + advance cursor]
    Version -->|<= current| MarkApplied[Insert inbox as already applied; ACK]
    Version -->|> current + 1| Buffer[Persist buffered_events row]
    Buffer --> Timer[Persist first_seen_at / refresh gap timer]
    Timer --> Gap{Gap timeout elapsed?}
    Gap -->|no| Wait[Wait for missing version]
    Gap -->|yes| Alert[Insert ordering_gaps; alert; keep event]
    Apply --> Drain[Drain buffered current + 1 events]
    Drain --> Ack[Commit then ACK]
    AckDuplicate --> End[Done]
    MarkApplied --> End
    Alert --> End
    Wait --> End
    Ack --> End
```

The inbox key is `(tenant_id, consumer_name, event_id)`. Aggregate ordering state is keyed by `(tenant_id, consumer_name, aggregate_id)`. A consumer never advances a cursor over an unresolved gap. After `MaxDeliver`, the consumer stores the poison event and error in `dead_letter_events` before acknowledging it.

## Crash scenarios

| Crash point | Durable result | Safe recovery |
| --- | --- | --- |
| Before business commit | No aggregate, event, or outbox row | Client retries the idempotent command. |
| After commit, before relay | Pending outbox row | Relay claims and publishes later. |
| After NATS publish, before outbox mark | Event may be published twice | JetStream message ID and inbox dedup make duplicate harmless. |
| Consumer before inbox commit | No consumer effect | NATS redelivers. |
| Consumer after projection commit, before ACK | Inbox/cursor already committed | Redelivery sees inbox key and ACKs without reapplying. |
| Future event before missing event | Buffer row | Missing version replay or gap alert; no out-of-order projection. |
