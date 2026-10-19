# Data model

All tables have `tenant_id NOT NULL`. Foreign keys that cross tables are composite `(tenant_id, referenced_id)` keys. The examples below show the contract shape; physical names may be refined during migration implementation without weakening the constraints.

## Wallet database (one database per logical wallet instance)

```mermaid
erDiagram
    tenants ||--o{ wallets : owns
    wallets ||--o{ accounts : contains
    accounts ||--o{ journal_postings : receives
    journal_transactions ||--|{ journal_postings : contains
    wallets ||--o{ wallet_operations : idempotently_handles
    journal_transactions ||--|| domain_events : emits
    domain_events ||--|| outbox : publishes
    wallets ||--o{ reservations : holds

    tenants {
        uuid tenant_id PK
    }
    wallets {
        uuid tenant_id PK,FK
        uuid wallet_id PK
        bigint aggregate_version
        string status
    }
    accounts {
        uuid tenant_id PK,FK
        uuid account_id PK
        uuid wallet_id FK
        string account_type
        string currency
    }
    journal_transactions {
        uuid tenant_id PK,FK
        uuid journal_transaction_id PK
        uuid aggregate_id FK
        bigint aggregate_version
        string entry_type
        timestamp occurred_at
    }
    journal_postings {
        uuid tenant_id PK,FK
        uuid posting_id PK
        uuid journal_transaction_id FK
        uuid account_id FK
        string currency
        string side
        bigint amount_minor
    }
    domain_events {
        uuid tenant_id PK,FK
        uuid event_id PK
        uuid journal_transaction_id FK
        string event_type
        int schema_version
        bigint aggregate_version
        jsonb payload
    }
    outbox {
        uuid tenant_id PK,FK
        uuid event_id PK
        jsonb payload
        string status
        int attempts
        timestamp available_at
    }
    wallet_operations {
        uuid tenant_id PK,FK
        string operation_key PK
        string request_hash
        jsonb result
    }
    reservations {
        uuid tenant_id PK,FK
        uuid reservation_id PK
        uuid wallet_id FK
        bigint amount_minor
        string status
        string operation_key
    }
```

There is no mutable wallet balance table in the authoritative wallet schema. `reservations` is a journal-linked state index, not a balance source; replay can rebuild it and the journal remains the money source.

## Transfer database

```mermaid
erDiagram
    tenants ||--o{ transfers : owns
    transfers ||--o{ transfer_steps : executes
    transfers ||--o{ reconciliation_exceptions : may_create
    transfers ||--o{ transfer_operations : deduplicates
    transfers ||--o{ transfer_events : records
    transfer_events ||--|| outbox : publishes

    tenants {
        uuid tenant_id PK
    }
    transfers {
        uuid tenant_id PK,FK
        uuid transfer_id PK
        uuid source_wallet_id
        uuid destination_wallet_id
        bigint amount_minor
        string currency
        string state
        bigint version
    }
    transfer_steps {
        uuid tenant_id PK,FK
        uuid transfer_id PK,FK
        string step_name PK
        string state
        int attempts
        uuid operation_id
        timestamp next_attempt_at
    }
    transfer_operations {
        uuid tenant_id PK,FK
        string idempotency_key PK
        string request_hash
        uuid transfer_id FK
    }
    transfer_events {
        uuid tenant_id PK,FK
        uuid event_id PK
        string event_type
        int schema_version
        uuid transfer_id FK
        bigint aggregate_version
        jsonb payload
    }
    outbox {
        uuid tenant_id PK,FK
        uuid event_id PK
        jsonb payload
        string status
        int attempts
    }
    reconciliation_exceptions {
        uuid tenant_id PK,FK
        uuid exception_id PK
        uuid transfer_id FK
        string category
        string status
        jsonb evidence
    }
```

The transfer database contains references to wallet IDs and operation IDs, not wallet foreign keys or wallet journal rows.

## Read database

```mermaid
erDiagram
    projection_versions ||--o{ projection_cursors : selects
    projection_versions ||--o{ wallet_balance_view : materializes
    projection_versions ||--o{ wallet_entry_view : materializes
    projection_versions ||--o{ transfer_view : materializes
    inbox_events ||--o{ projection_cursors : advances
    reorder_buffer ||--o{ inbox_events : awaits

    projection_versions {
        uuid tenant_id PK,FK
        string projection_name PK
        int active_version
        int previous_version
        bigint promoted_watermark
    }
    projection_cursors {
        uuid tenant_id PK
        string consumer_name PK
        string projection_name PK
        uuid aggregate_id PK
        bigint current_version
        timestamp updated_at
    }
    inbox_events {
        uuid tenant_id PK
        string consumer_name PK
        uuid event_id PK
        uuid aggregate_id
        bigint aggregate_version
        timestamp received_at
    }
    reorder_buffer {
        uuid tenant_id PK
        string consumer_name PK
        string projection_name PK
        uuid aggregate_id PK
        bigint aggregate_version PK
        jsonb event_payload
        timestamp first_seen_at
    }
    wallet_balance_view {
        int projection_version PK
        uuid tenant_id PK
        uuid wallet_id PK
        bigint current_minor
        bigint held_minor
        bigint available_minor
        bigint source_version
    }
    wallet_entry_view {
        int projection_version PK
        uuid tenant_id PK
        uuid journal_transaction_id PK
        uuid wallet_id
        jsonb postings
    }
    transfer_view {
        int projection_version PK
        uuid tenant_id PK
        uuid transfer_id PK
        string state
        bigint amount_minor
        bigint source_version
    }
```
