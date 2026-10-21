# ADR 020: Per-Wallet Consistency Cursor and Read-Your-Writes Consistency

Status: accepted
- **Date:** 2026-10-20
- **Deciders:** Digital Wallet Core Team
- **Consulted:** Architecture, Ledger, Projector Teams
- **Informed:** API Consumers, Integration Engineers

## Context and Problem Statement

In a multi-tenant, partitioned event-driven architecture, events are emitted per aggregate and distributed across topics/subjects without a global FIFO sequencer. When clients execute mutating commands (such as a deposit or withdrawal), immediate subsequent queries to read models may observe stale data due to asynchronous relay and projection propagation delays ("read-your-writes" hazard). We need a precise consistency contract that allows clients to wait for their own mutations to be reflected in read views without introducing global coordination or sacrificing partition tolerance.

## Decision Drivers

- No global monotonically increasing sequencer exists or is desirable across distributed wallet instances.
- Each wallet aggregate maintains a strict monotonic `aggregate_version` starting from version 1 (`wallet.created`).
- Mutating commands must communicate their durable version back to callers.
- Read models must report exactly which aggregate version they reflect, and support bounded waiting.
- Authoritative journal reads must report the exact version at which balance was calculated.

## Considered Options

1. **Wall-clock timestamp watermarks:** Use commit timestamps (`occurred_at`) as the consistency boundary.
2. **Global distributed sequence ID:** Introduce a centralized sequence generator across all wallet shards.
3. **Per-wallet monotonic aggregate version cursor:** Expose `aggregate_version` on write responses and read models, enabling explicit `min_version` queries.

## Decision Outcome

Chosen Option: **Option 3 (Per-wallet monotonic aggregate version cursor)**.

### Specification

1. **Write Responses:**
   - `POST /v1/wallets`: returns `aggregate_version: 1`.
   - `POST /v1/wallets/{wallet_id}/deposit` and `POST /v1/wallets/{wallet_id}/withdraw`: return `aggregate_version: N` of the event emitted in the transaction.
2. **Authoritative Reads:**
   - `GET /v1/wallets/{wallet_id}/balance`: returns `as_of_version: N`, querying `MAX(events.aggregate_version)` in the same transaction snapshot as the balance calculation.
3. **Read-Model Queries:**
   - `GET /v1/read/wallets/{wallet_id}/balance`: accepts optional query parameters `min_version` (int64) and `wait_ms` (integer, max 5,000 ms).
   - The projector queries the active projection slot. If `last_applied_version >= min_version`, it returns immediately with HTTP 200 and `read_model_consistent_through: last_applied_version`.
   - If `last_applied_version < min_version`, the server polls the active slot until the version is reached or `wait_ms` expires.
   - If `wait_ms` expires before the version is reached, the server returns HTTP 409 Conflict with code `read_model_behind` and header `Retry-After: 1`.

## Consequences

### Positive
- Clients achieve deterministic read-your-writes consistency by simply passing `min_version = write_result.aggregate_version`.
- No global locks, centralized sequencers, or distributed coordination protocols are required.
- Clear distinction between eventual read models (`authoritative: false`) and authoritative ledger views (`authoritative: true`).

### Negative
- Read API handlers must manage polling/timeouts for lagging projections when `min_version` is supplied.
