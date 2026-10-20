# ADR 022: Dual-Slot Projections Without Runtime DDL

- **Status:** Accepted
- **Date:** 2026-10-20
- **Deciders:** Digital Wallet Core Team
- **Consulted:** Architecture, Database Reliability
- **Informed:** All Engineering

## Context and Problem Statement

When rebuilding or altering projection read models, systems often use dynamic table naming (such as `wallet_balance_view_v17` and `wallet_balance_view_v18`) created and dropped via runtime DDL (`CREATE TABLE`, `DROP TABLE`). In fintech-grade environments, executing runtime DDL introduces substantial risks:
- Requires elevated table-creation privileges for the runtime application role (violating owner separation and least privilege, where only `migrator_role` can execute DDL);
- Interferes with connection pool prepared statements and schema caching;
- Table lock contention during DDL operations can block concurrent read queries.

## Decision Drivers

- Maintain strict owner separation: application roles must have zero DDL privileges (`CREATE`, `DROP`, `ALTER`).
- Allow online background rebuilds without degrading or blocking concurrent reads.
- Provide instant, zero-downtime promotion and instant rollback.
- Avoid dynamic schema generation and keep all migrations managed by Goose under `migrator_role`.

## Decision Outcome

Chosen Solution: **Managed Dual-Slot Architecture (`slot a` and `slot b`)**.

### Specification

1. **Table Schema:**
   All view tables have a `projection_slot` column (`text NOT NULL CHECK (projection_slot IN ('a', 'b'))`) included as the leading element of their primary keys:
   - `wallet_balance_view (projection_slot, tenant_id, wallet_id, currency)`
   - `wallet_entry_view (projection_slot, tenant_id, wallet_id, entry_version)`
2. **Version Pointer Table:**
   A single-row configuration table `projection_versions`:
   - `projection_name text PRIMARY KEY` (e.g. `'wallet_balances'`)
   - `active_slot text NOT NULL CHECK (active_slot IN ('a', 'b'))`
   - `previous_slot text CHECK (previous_slot IN ('a', 'b'))`
   - `promoted_at timestamptz NOT NULL`
3. **Consumer Isolation:**
   Each slot is consumed by a dedicated JetStream durable pull consumer:
   - Slot A consumer name: `projector-balance-a`
   - Slot B consumer name: `projector-balance-b`
   All inbox messages, aggregate cursors, buffered events, and gaps are natively isolated because `consumer_name` is part of their primary keys.
4. **Atomic Promotion and Rollback:**
   - **Promotion:** A single transaction updates `projection_versions.active_slot = candidate_slot, previous_slot = old_active_slot`.
   - **Rollback:** A single transaction flips `active_slot` back to `previous_slot`.
   - Read queries join `projection_versions` in the same statement, ensuring zero cache-drift or stale routing.

## Consequences

### Positive
- Zero runtime DDL: tables and indexes are permanently provisioned by `migrator_role` during standard migrations.
- Runtime `projector_role` requires only `SELECT`, `INSERT`, `UPDATE` grants on view rows.
- Instant atomic promotion and instant rollback.

### Negative
- Requires maintaining two slots' worth of storage capacity in the read database.
