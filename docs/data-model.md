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
        timestamp locked_until
        string last_error
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
    Begin[Begin wallet transaction] --> Lock[pg_advisory_xact_lock tenant + wallet]
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

The runtime wallet role is not the database or table owner. Accounts and wallets are INSERT/SELECT-only because writes serialize with a transaction-scoped advisory lock rather than `SELECT ... FOR UPDATE`. `idempotency_records` is append-only; same-key commands serialize with an advisory lock and insert the completed response in the command transaction, so the runtime role does not need UPDATE on idempotency records.

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
        boolean is_monetary
        string owner
        string resolution_action
        string resolved_by
        timestamp resolved_at
        jsonb resolution_evidence
        timestamp updated_at
    }
```

The transfer database contains references to wallet IDs and operation IDs, not wallet foreign keys or wallet journal rows.

## Read database

```mermaid
erDiagram
    projection_versions ||--o{ wallet_balance_view : points
    projection_versions ||--o{ wallet_entry_view : points
    projection_rebuilds ||--o{ projection_versions : updates
    inbox_messages ||--o{ aggregate_cursors : advances
    buffered_events ||--o{ inbox_messages : awaits
    ordering_gaps ||--o{ buffered_events : detects
    dead_letter_events ||--o{ inbox_messages : records
    applied_events_log ||--o{ inbox_messages : applies

    projection_versions {
        string projection_name PK
        string active_slot
        string previous_slot
        timestamp promoted_at
    }
    projection_rebuilds {
        uuid rebuild_id PK
        string projection_name
        string target_slot
        string status
        jsonb validation_report
        timestamp started_at
        timestamp completed_at
    }
    inbox_messages {
        uuid tenant_id PK
        string consumer_name PK
        uuid event_id PK
        uuid aggregate_id
        bigint aggregate_version
        timestamp received_at
    }
    aggregate_cursors {
        uuid tenant_id PK
        string consumer_name PK
        uuid aggregate_id PK
        bigint last_version
        timestamp updated_at
    }
    buffered_events {
        uuid tenant_id PK
        string consumer_name PK
        uuid aggregate_id PK
        bigint aggregate_version PK
        uuid event_id
        jsonb payload
        timestamp first_seen_at
    }
    ordering_gaps {
        uuid tenant_id PK
        string consumer_name PK
        uuid aggregate_id PK
        bigint expected_version PK
        string status
        timestamp first_seen_at
    }
    dead_letter_events {
        uuid tenant_id PK
        string consumer_name PK
        uuid event_id PK
        string error
        jsonb payload
        timestamp created_at
    }
    applied_events_log {
        uuid tenant_id PK
        string consumer_name PK
        uuid event_id PK
        uuid aggregate_id
        bigint aggregate_version
        timestamp applied_at
    }
    wallet_balance_view {
        string projection_slot PK
        uuid tenant_id PK
        uuid wallet_id PK
        string currency PK
        bigint posted_balance_minor
        bigint available_balance_minor
        bigint last_applied_version
        timestamp updated_at
    }
    wallet_entry_view {
        string projection_slot PK
        uuid tenant_id PK
        uuid wallet_id PK
        bigint entry_version PK
        bigint aggregate_version
        uuid event_id
        uuid journal_transaction_id
        string entry_type
        string side
        bigint amount_minor
        string currency
        string reference
        timestamp occurred_at
    }
```

The read database is decoupled from wallet write engines and owns projections constructed solely from event payloads. Rather than executing runtime DDL, views use managed dual slots (`slot a` and `slot b`) keyed by `projection_slot`. `projection_versions` holds the globally active slot. `transfer_view` is deferred to Phase 6 when transfer saga events are introduced.
