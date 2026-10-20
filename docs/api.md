# API contract

The HTTP API is JSON over HTTPS in deployed environments. Local development may use plain HTTP. All IDs are UUIDv7 strings. Monetary amounts are integer minor units; `currency` is required and must be `USD` in v1.

## Common headers and errors

| Header | Required | Meaning |
| --- | --- | --- |
| `X-Tenant-ID` | Yes | Tenant scope for every public and internal request. |
| `Idempotency-Key` | Mutating public requests | Stable caller operation key. Required for wallet money commands and transfer creation. |
| `X-Request-ID` | Recommended | Caller-provided trace ID; server generates one if absent. |
| `X-Correlation-ID` | Internal/recommended | End-to-end transfer correlation. |
| `X-Require-Consistent-Through` | Read-model reads only | Optional JSON watermark requirement. |

Errors use:

```json
{
  "error": {
    "code": "insufficient_funds",
    "message": "wallet does not have enough available USD",
    "request_id": "req_01",
    "retryable": false,
    "details": {}
  }
}
```

`409` is used for invalid state transitions, projection lag that cannot satisfy a requested watermark, and known concurrency conflicts. `422` is used for validation, insufficient funds, and idempotency-key reuse with a different request fingerprint. `503` indicates a dependency or service outage and is retryable only according to the caller's operation semantics.

## Public endpoints

| Method and path | Owner | Purpose | Idempotency |
| --- | --- | --- | --- |
| `POST /v1/wallets` | wallet | Create a wallet and its USD account set. Returns `aggregate_version: 1`. | `Idempotency-Key` required; same request returns original wallet. |
| `GET /v1/wallets/{wallet_id}` | wallet | Read wallet metadata in tenant scope. | N/A. |
| `GET /v1/wallets/{wallet_id}/balance` | wallet | Authoritative journal-derived balance. Returns `as_of_version` in snapshot. | N/A. |
| `GET /v1/wallets/{wallet_id}/entries` | wallet | Authoritative journal entry history. | N/A. |
| `POST /v1/wallets/{wallet_id}/deposit` | wallet | Post an allowed v1 deposit. Returns `aggregate_version`. | Key + request fingerprint; replay returns original entry. |
| `POST /v1/wallets/{wallet_id}/withdraw` | wallet | Post a withdrawal after available check. Returns `aggregate_version`. | Key + request fingerprint; replay returns original entry. |
| `GET /v1/read/wallets/{wallet_id}/balance` | projector | High-throughput read-model balance with read-your-writes support (`min_version`, `wait_ms`). | N/A. |
| `GET /v1/read/wallets/{wallet_id}/entries` | projector | Read-model entry history keyset-paginated by projection-local entry version (`after_version`, `limit`); includes side and aggregate version. | N/A. |
| `POST /v1/transfers` | transfer | Create and start a transfer saga. | Key + request fingerprint; replay returns original transfer. |
| `GET /v1/transfers/{transfer_id}` | transfer | Inspect transfer state and step outcomes. | N/A. |

Commands return the durable accepted result. Transfer creation returns `202` while the saga is running; a repeated request does not start another saga.

## Internal endpoints and listener isolation

All `/internal/*` routes are served exclusively on a dedicated second listener (`INTERNAL_HTTP_ADDR`, default `:8085`) and are never exposed on the public listener (`HTTP_ADDR`, default `:8080`). The public listener returns `404 Not Found` for any `/internal/*` request path.

Every request to the internal listener requires the `X-Internal-Token` HTTP header, matched against the service's configured `INTERNAL_TOKEN` using constant-time comparison. Requests with missing or invalid tokens are rejected immediately with `401 Unauthorized`. In local and production deployments, internal listener ports are never bound to external interfaces (in Docker Compose, they are published strictly to loopback on `127.0.0.1`).

| Method and path | Purpose | Idempotency / auth |
| --- | --- | --- |
| `GET /internal/v1/stats` | Telemetry, outbox backlog, lag, gaps, and dead-letter statistics. | `X-Internal-Token`. |
| `GET /internal/v1/wallets/verify` | Wallet cross-tenant verification scan returning `(wallet_id, currency, posted_balance_minor, as_of_version)` in keyset pages. | `X-Internal-Token`, read-only `REPEATABLE READ` snapshot. |
| `POST /internal/v1/wallets/{wallet_id}/reservations` | Reserve source funds for a transfer. | `X-Internal-Token`, `transfer_id:reserve`; amount and tenant fingerprinted. |
| `POST /internal/v1/wallets/{wallet_id}/pending-credits` | Create destination pending credit. | `X-Internal-Token`, `transfer_id:pending-credit`. |
| `POST /internal/v1/wallets/{wallet_id}/captures` | Capture a confirmed reservation. | `X-Internal-Token`, `transfer_id:capture`. |
| `POST /internal/v1/wallets/{wallet_id}/finalizations` | Make pending credit spendable. | `X-Internal-Token`, `transfer_id:finalize`. |
| `POST /internal/v1/wallets/{wallet_id}/compensations` | Append a reversal for a confirmed side effect. | `X-Internal-Token`, `transfer_id:step:compensate`. |
| `GET /internal/v1/operations/{operation_id}` | Resolve an ambiguous operation by stable UUIDv7 operation ID. | `X-Internal-Token`, read-only lookup. |

An internal command is accepted only when the wallet service's configured instance owns the referenced wallet. The transfer service receives facts and operation IDs, not database access. The `X-Operation-ID` header is a UUIDv7 generated once per saga step and stored in `transfer_steps.operation_id`; the string idempotency key is separately derived as `transfer_id:step`.

## Idempotency semantics

The server hashes the normalized request body, tenant, route, and operation scope. On a duplicate key:

1. same fingerprint and completed result: return the original status/body and set `Idempotency-Replayed: true`;
2. same fingerprint while the first request is in progress: return `409 request_in_progress` or a bounded `202`, as specified by the endpoint;
3. different fingerprint: return `422 idempotency_key_reused`;
4. an ambiguous transport outcome: the caller retries with the same key or queries the operation; it must not mint a new key to guess.

Idempotency records are tenant-scoped and retained at least as long as the business replay window. A database unique constraint is the final guard against concurrent duplicate commands.

## Health and consistency

Every service exposes `/healthz` for process liveness and `/readyz` for required dependencies. Readiness does not imply the projector is caught up. Read-model responses include a watermark, and authoritative wallet reads do not rely on NATS or the projector.

Authoritative wallet balance responses contain `authoritative: true` and do not include `read_model_consistent_through`; that field is reserved for projector-backed query responses. Every wallet command takes `X-Tenant-ID`, and every repository call receives the tenant explicitly rather than deriving it from a wallet ID alone.
