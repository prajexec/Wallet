# Transfer saga

The transfer service coordinates a wallet-to-wallet transfer across two independent wallet databases. It owns the transfer state machine, step attempts, timeout classification, and operator-visible outcome. It never writes wallet tables directly.

## State machine

```mermaid
stateDiagram-v2
    [*] --> CREATED
    CREATED --> RESERVING: dispatch reserve
    RESERVING --> RESERVED: reserve success
    RESERVING --> RESERVING: transient failure / backoff / retry
    RESERVING --> FAILED: permanent failure before side effect
    RESERVED --> PENDING_CREDIT: dispatch pending credit
    PENDING_CREDIT --> PENDING_CREDIT: transient failure / backoff / retry
    PENDING_CREDIT --> PENDING_CREDITED: pending credit success
    PENDING_CREDIT --> COMPENSATION_PENDING: permanent failure after reserve
    PENDING_CREDIT --> RECONCILIATION_REQUIRED: ambiguous outcome
    PENDING_CREDITED --> CAPTURING: dispatch capture
    CAPTURING --> CAPTURED: capture success
    CAPTURING --> CAPTURING: transient failure / backoff / retry
    CAPTURING --> RECONCILIATION_REQUIRED: timeout / ambiguous capture
    CAPTURING --> COMPENSATION_PENDING: confirmed permanent failure
    CAPTURED --> FINALIZING: dispatch destination finalize
    FINALIZING --> COMPLETED: finalize success
    FINALIZING --> FINALIZING: transient failure / backoff / retry
    FINALIZING --> COMPENSATION_PENDING: permanent failure after capture
    FINALIZING --> RECONCILIATION_REQUIRED: ambiguous outcome
    COMPENSATION_PENDING --> COMPENSATING: rate-limited reversal dispatch
    COMPENSATING --> COMPENSATED: idempotent reversal success
    COMPENSATING --> COMPENSATING: transient compensation failure / retry
    COMPENSATING --> RECONCILIATION_REQUIRED: ambiguous reversal
    RECONCILIATION_REQUIRED --> CAPTURING: status absent / resume capture
    RECONCILIATION_REQUIRED --> FINALIZING: capture confirmed / resume finalize
    RECONCILIATION_REQUIRED --> COMPENSATION_PENDING: confirmed side effect cannot continue
    COMPLETED --> [*]
    FAILED --> [*]
    COMPENSATED --> [*]

    note right of RESERVING
      Retry edges are bounded by
      per-step timeout and max retries.
    end note
    note right of COMPENSATION_PENDING
      Compensation is allowed only
      after a side effect is confirmed.
    end note
```

Retry edges are intentionally distinct from compensation and reconciliation edges. A slow dependency is not evidence that money moved or did not move.

## Step contract

| Step | Request owner | Side effect | Success evidence | Retry policy | Failure after side effect |
| --- | --- | --- | --- | --- | --- |
| Create | transfer | Transfer row and saga created | Idempotent transfer response | N/A | Return original result |
| Reserve | source wallet | Journal-backed source hold | Wallet command response plus event | Exponential backoff, bounded attempts | Reversal only if confirmed |
| Pending credit | destination wallet | Journal-backed pending destination credit | Wallet command response plus event | Exponential backoff, bounded attempts | Compensate confirmed reserve |
| Capture | source wallet | Held amount committed to outbound settlement | Capture response plus event | Retry only when outcome is known transient | Timeout is reconciliation, not compensation |
| Finalize | destination wallet | Pending credit becomes spendable | Finalize response plus event | Exponential backoff, bounded attempts | Compensation of confirmed source capture and destination state as needed |
| Compensate | affected wallet(s) | Idempotent reversal journal entry | Reversal response plus event | Rate-limited, bounded worker retries | Reconciliation exception |

Every step carries a stable operation ID derived from transfer ID and step name. Wallet endpoints treat that operation ID as an idempotency key. A retried request must return the original result, not append a second entry.

## Failure classification

| Classification | Examples | Action |
| --- | --- | --- |
| Transient | HTTP 503, connection reset before response, known retryable database serialization failure | Retry with per-step timeout, exponential backoff, jitter, and max attempts. Do not compensate only because it is slow. |
| Permanent, confirmed side effect | Destination rejects a known-valid request after source reserve/capture is confirmed | Set `COMPENSATION_PENDING`, perform an idempotent reversal, then `COMPENSATED`; rate-limit compensation. |
| Ambiguous | Capture timeout, connection lost after request could have committed, wallet restart during response | Set `RECONCILIATION_REQUIRED`, write `reconciliation_exceptions`, and stop automatic money movement until status is known. |

## Happy path

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant T as Transfer service
    participant S as Wallet source
    participant D as Wallet destination
    participant J as NATS / projector

    C->>T: POST /v1/transfers (idempotency key)
    T-->>C: 202 CREATED + transfer_id
    T->>S: reserve(transfer_id, amount)
    S-->>T: RESERVED + journal event
    T->>D: pending_credit(transfer_id, amount)
    D-->>T: PENDING_CREDITED + journal event
    T->>S: capture(transfer_id, amount)
    S-->>T: CAPTURED + journal event
    T->>D: finalize(transfer_id, amount)
    D-->>T: FINALIZED + journal event
    T->>T: mark COMPLETED + emit transfer.completed
    S-->>J: source outbox events
    D-->>J: destination outbox events
    T-->>C: GET /v1/transfers/{transfer_id} => COMPLETED
```

## Failure paths

```mermaid
sequenceDiagram
    autonumber
    participant T as Transfer service
    participant S as Wallet source
    participant D as Wallet destination
    participant R as Reconciliation worker

    T->>S: reserve
    S-->>T: RESERVED (confirmed)
    T->>D: pending_credit
    D-->>T: permanent failure (confirmed, no credit)
    T->>T: COMPENSATION_PENDING
    T->>S: release/reverse reserve (idempotent)
    S-->>T: COMPENSATED

    T->>S: capture
    S--xT: timeout after request may have committed
    T->>T: RECONCILIATION_REQUIRED
    T->>R: create exception and stop automatic compensation
    R->>S: query capture status by operation_id
    alt capture absent
        S-->>R: NOT_FOUND / safe to retry
        R->>T: resume capture workflow
    else capture confirmed
        S-->>R: CAPTURED
        R->>T: resume finalization or compensation decision
    end
```

## Terminal rules

- `COMPLETED` requires source capture and destination finalization for the same transfer, amount, currency, and tenant.
- `COMPENSATED` requires every confirmed monetary side effect to have a matching idempotent reversal.
- `FAILED` is allowed only before any confirmed monetary side effect.
- `RECONCILIATION_REQUIRED` is not a failure that can be silently retried. It is a durable operator/work-queue state.
