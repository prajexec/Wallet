# ADR 024: Cross-Tenant Internal Verification Endpoint

Status: accepted
- **Date:** 2026-10-20
- **Deciders:** Digital Wallet Core Team
- **Consulted:** Architecture, Security, Compliance
- **Informed:** All Engineering

## Context and Problem Statement

To gate CQRS projection promotion, the candidate read model must be proven identical to the write-side journal truth for every wallet. Sampling or partial verification is unacceptable in financial ledgers. However, standard public APIs are strictly tenant-scoped (`X-Tenant-ID` required), making a full system-wide verification scan impractical without iterating through every known tenant. Furthermore, querying the database during live transactions risks dirty reads and inconsistent point-in-time comparisons.

## Decision Drivers

- Full exhaustive verification across all tenants without statistical sampling.
- Compare projected data against authoritative journal data at equal versions.
- Strict isolation: cross-tenant access must be physically impossible from the public API listener.
- Point-in-time consistency per page: prevent tearing between balance calculation and version query.

## Decision Outcome

Chosen Solution: **Cross-Tenant Keyset-Paginated Verification Endpoint on Internal Listener**.

### Specification

1. **Listener and Authentication Isolation:**
   - Exposed exclusively on the secondary internal HTTP listener (`INTERNAL_HTTP_ADDR`, default `:8085`).
   - The public traffic listener (`HTTP_ADDR`, default `:8080`) returns HTTP 404 for this route.
   - Requires the `X-Internal-Token` header, validated via constant-time comparison against `INTERNAL_TOKEN`.
2. **Endpoint Route and Keyset Pagination:**
   - `GET /internal/v1/wallets/verify?after_tenant_id=&after_wallet_id=&limit=`
   - Reads ordered by `(tenant_id, wallet_id)` with a maximum page size of 500 rows.
3. **Transaction Snapshot Consistency:**
   - Each page is evaluated inside a single `REPEATABLE READ` transaction snapshot.
   - Returns:
     - `tenant_id`: UUIDv7
     - `wallet_id`: UUIDv7
     - `currency`: ISO currency code (`USD`)
     - `posted_balance_minor`: `SUM(postings)`
     - `as_of_version`: `MAX(events.aggregate_version)`
4. **Verifier Comparison Logic:**
   - The projector queries both wallet instances (`WALLET_VERIFY_URLS`).
   - For every wallet, the verifier matches `as_of_version` with the candidate slot's `last_applied_version`:
     - Equal versions **must** have identical posted balances;
     - If candidate projection is behind, the verifier retries until caught up or timeout;
     - Verifies `wallet_balance_view.posted_balance_minor == SUM(wallet_entry_view.amount_minor)`;
    - Verifies `wallet_entry_view.entry_version` runs contiguously from 1 to the number of journal-entry events applied for the wallet; `last_applied_version` is the aggregate-event watermark and can include `wallet.created` or counted no-op events;
     - Verifies candidate slot matches active slot where versions match;
     - Verifies no authoritative wallet is missing from the projection.

## Consequences

### Positive
- Guarantees 100% mathematical accuracy before promotion without sampling.
- Explicit exception to tenant scoping is tightly restricted to the local internal listener and authenticated by shared secret token.
- `REPEATABLE READ` snapshot guarantees consistent `(posted_balance_minor, as_of_version)` pairs.

### Negative
- Running large full-scan verifications places read query load on the wallet databases.
