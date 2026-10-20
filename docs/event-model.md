# Event model

Events are durable integration contracts. They are emitted only after the owning service commits the state change and its outbox row. The `events.payload` column stores the complete JSON envelope shown below; event payloads are versioned and consumers must tolerate redelivery without inferring ordering from NATS delivery order alone.

## Envelope

```json
{
  "event_id": "0192f4b7-6d92-7a4d-8cb5-1b3f9d8f1111",
  "event_type": "wallet.deposit_posted",
  "schema_version": 1,
  "occurred_at": "2026-10-19T12:00:00.000Z",
  "tenant_id": "0192f4b7-6d92-7a4d-8cb5-1b3f9d8f2222",
  "aggregate_type": "wallet",
  "aggregate_id": "0192f4b7-6d92-7a4d-8cb5-1b3f9d8f3333",
  "aggregate_version": 7,
  "producer": "wallet-source",
  "correlation_id": "0192f4b7-6d92-7a4d-8cb5-1b3f9d8f4444",
  "causation_id": "0192f4b7-6d92-7a4d-8cb5-1b3f9d8f5555",
  "data": {},
  "metadata": {"request_id": "req_01"}
}
```

| Field | Contract |
| --- | --- |
| `event_id` | UUIDv7, globally unique; also the NATS `Nats-Msg-Id`. |
| `event_type` / `schema_version` | Stable name and additive-evolution version. |
| `occurred_at` | UTC commit-side timestamp, not a client-supplied time. |
| `tenant_id` | Required on every event and used in consumer keys. |
| `aggregate_*` | Ordering key and monotonic version within a tenant aggregate. |
| `producer` | Logical service instance that owns the event, such as `wallet-source`; transfer events use `transfer`. |
| `correlation_id` | End-to-end command or transfer correlation. |
| `causation_id` | Immediate command/event that caused this event, when known. |
| `data` | Typed payload; amounts are integer minor units. |
| `metadata` | Non-authoritative tracing and operational context only. |

## Event catalog

| Event | Producer | Aggregate | Meaning |
| --- | --- | --- | --- |
| `wallet.created` | wallet instance | wallet | Wallet and its account set were created. |
| `wallet.deposit_posted` | wallet instance | wallet | A balanced journal entry credited a wallet from an allowed v1 funding source. |
| `wallet.withdrawal_posted` | wallet instance | wallet | A balanced journal entry debited a wallet after an availability check. |
| `wallet.reservation_created` | wallet instance | wallet | Funds were moved into a journal-backed transfer hold. |
| `wallet.reservation_captured` | wallet instance | wallet | A held amount was committed to outbound settlement. |
| `wallet.reservation_released` | wallet instance | wallet | A held amount was returned after a safe, confirmed failure. |
| `wallet.pending_credit_created` | wallet instance | wallet | Destination received a pending credit backed by an inbound settlement account. |
| `wallet.pending_credit_finalized` | wallet instance | wallet | Pending credit became spendable wallet balance. |
| `wallet.compensation_posted` | wallet instance | wallet | An idempotent reversal entry was appended. |
| `transfer.created` | transfer | transfer | Transfer aggregate and saga were accepted. |
| `transfer.step_changed` | transfer | transfer | Saga step entered, retried, succeeded, or failed. |
| `transfer.completed` | transfer | transfer | Source capture and destination finalization both succeeded. |
| `transfer.compensation_pending` | transfer | transfer | Confirmed side effect requires reversal. |
| `transfer.compensated` | transfer | transfer | Reversal completed and transfer is terminal. |
| `transfer.reconciliation_required` | transfer | transfer | Outcome is ambiguous; automatic money movement stops. |

Payloads must include `amount_minor` and `currency` for monetary events. A monetary payload's amount is never a floating-point number. Event names describe facts, not commands.

### Detailed payload schemas

Because projections are constructed purely from event payloads without querying wallet databases, wallet domain event payloads contain all required read-model attributes:

#### `wallet.created` (schema_version: 1)
```json
{
  "wallet_id": "0192f4b7-6d92-7a4d-8cb5-1b3f9d8f3333",
  "account_id": "0192f4b7-6d92-7a4d-8cb5-1b3f9d8f3333",
  "currency": "USD"
}
```

#### `wallet.deposit_posted` / `wallet.withdrawal_posted` (schema_version: 1)
```json
{
  "wallet_id": "0192f4b7-6d92-7a4d-8cb5-1b3f9d8f3333",
  "journal_transaction_id": "0192f4b7-6d92-7a4d-8cb5-1b3f9d8f7777",
  "amount_minor": 10000,
  "currency": "USD",
  "side": "CREDIT",
  "entry_type": "DEPOSIT",
  "reference": "payroll-deposit-2026-10"
}
```

### Schema evolution and unrecognized events

- **Unknown event types:** If a consumer encounters a valid envelope with an unrecognized `event_type`, the consumer does not crash or fail. It logs a counted telemetry metric, skips view mutation, and safely advances the aggregate cursor.
- **Unsupported schema versions:** If an event has a `schema_version` strictly greater than what the running consumer code supports, the worker never guesses or applies partial data. The event is diverted directly to `dead_letter_events` with an explicit incompatibility error, alerting operators to deploy the updated consumer binary.

## Subjects and JetStream mapping

Every envelope maps to a NATS subject:

```text
events.<tenant_id>.<aggregate_type>.<event_type>
```

The event ID is published as `Nats-Msg-Id`. A `WALLET_EVENTS` file-backed stream subscribes to `events.>` with a two-minute duplicate window.

Stream retention is explicitly configured as unlimited (`MaxAge = 0`, file storage) so that full rebuilds can replay the entire historical stream from version 1 (`DeliverAll`). The wallet database `events` table remains the ultimate immutable record of truth; in the event of stream loss, an operator tool can reset outbox publish watermarks to reconstruct the stream.

## Ordering, duplication, and gaps

NATS subjects are partitioned by event type and tenant/aggregate identity as a routing aid, not as the correctness mechanism. Consumers use `(tenant_id, consumer_name, event_id)` in an inbox table as the deduplication backstop.

For each `(tenant_id, consumer_name, aggregate_id)`:

1. apply `aggregate_version = current + 1`;
2. buffer a future version in the reorder buffer;
3. ignore a version already applied, after recording the duplicate in metrics;
4. start a gap timer for a missing version;
5. after the timeout, raise a gap alert and keep the event available for replay/manual inspection.

The consumer acknowledges NATS only after its inbox insert, projection mutation, cursor update, and any buffer changes commit in one database transaction. Redelivery after a crash is therefore harmless. A future event is durable in `buffered_events`, not an in-memory queue. A sweeper writes one deduplicated `ordering_gaps` row after the configured missing-version window; it never skips or fabricates a version. A poison message is retained in `dead_letter_events` after the consumer's `MaxDeliver` limit.
