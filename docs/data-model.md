# Data model

All tables have `tenant_id NOT NULL`. Foreign keys that cross tables are composite `(tenant_id, referenced_id)` keys. The wallet schema below is implemented in Phase 3; transfer and read schemas remain the Phase 1/2 contracts.

## Wallet database (one database per logical wallet instance)

```mermaid
erDiagram
    tenants ||--o{ wallets : owns
    wallets ||--|| accounts : uses
    accounts ||--o{ journal_postings : receives
    journal_transactions ||--|{ journal_postings : contains
    wallets ||--o{ events : aggregates
    events ||--|| outbox_messages : publishes
    wallets ||--o{ idempotency_records : commands

    tenants {
        uuid tenant_id PK
    }
    wallets {
        uuid tenant_id PK,FK
        uuid id PK
        uuid account_id FK
        string status
        timestamp created_at
    }
    accounts {
        uuid tenant_id PK,FK
        uuid id PK
        string kind
        string name
        string currency
        timestamp created_at
    }
    journal_transactions {
        uuid tenant_id PK,FK
        uuid id PK
        string operation_type
        string external_reference
        timestamp created_at
    }
    journal_postings {
        uuid tenant_id PK,FK
        uuid id PK
        uuid transaction_id FK
        uuid account_id FK
        string side
        bigint amount_minor
        string currency
        string entry_type
        uuid transfer_id
        timestamp created_at
    }
    events {
        uuid tenant_id PK,FK
        uuid event_id PK
        string aggregate_type
        uuid aggregate_id
        bigint aggregate_version
        string event_type
        int schema_version
        uuid correlation_id
        uuid causation_id
        timestamp occurred_at
        jsonb payload
    }
    outbox_messages {
        uuid tenant_id PK,FK
        uuid id PK
        uuid event_id FK
        timestamp created_at
        timestamp published_at
        int attempts
        timestamp next_attempt_at
    }
    idempotency_records {
        uuid tenant_id PK,FK
        string key PK
        string request_hash
        string status
        int response_status
        jsonb response_body
        timestamp created_at
    }
```

`accounts.kind` is either `user_wallet` or `system_clearing`. Each tenant has the four named clearing accounts (`deposit-clearing`, `withdrawal-clearing`, `outbound-clearing`, and `inbound-clearing`) created idempotently when first needed or during wallet creation. Only the user-wallet account is subject to the non-negative available-balance invariant.

`events` and `outbox_messages` store the event envelope and are inserted in the same transaction as the wallet change. Phase 3 does not publish or update outbox rows; the relay begins in Phase 4.

```mermaid
flowchart TD
    Begin[Begin wallet transaction] --> Lock[SELECT wallet/account FOR UPDATE]
    Lock --> Idempotency{Existing tenant + key?}
    Idempotency -->|same request hash| Replay[Return stored response]
    Idempotency -->|different hash| Conflict[Return 422 idempotency conflict]
    Idempotency -->|new key| Balance[SUM postings for account and currency]
    Balance --> Holds[Subtract active holds; Phase 3 holds = 0]
    Holds --> Sufficient{Available balance sufficient?}
    Sufficient -->|no withdrawal funds| Reject[Rollback with insufficient funds]
    Sufficient -->|yes / deposit| Journal[Insert journal transaction and postings]
    Journal --> Event[Insert one event with next aggregate version]
    Event --> Outbox[Insert one outbox_messages row]
    Outbox --> Record[Insert idempotency response]
    Record --> Commit[Commit; return response]
```

The database uses composite tenant foreign keys for mutable account references. References from `journal_postings` to immutable `journal_transactions`, and from `outbox_messages` to immutable `events`, use equivalent tenant-scoped insert validation triggers because PostgreSQL foreign-key key-share checks require UPDATE privilege on the referenced table. Wallet roles retain only INSERT and SELECT on immutable tables. Additional triggers reject UPDATE, DELETE, and TRUNCATE on `journal_transactions`, `journal_postings`, and `events`. A deferred constraint trigger checks debit/credit equality for each transaction and posting currency at commit.

There is no mutable wallet balance table anywhere in the wallet database. The authoritative balance is always recomputed from `journal_postings`; holds are a future extension point and are currently zero.

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
