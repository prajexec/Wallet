# Architecture

## System context

The wallet backend supports a caller that creates a wallet-to-wallet transfer. A transfer service owns the workflow state and calls two isolated instances of the same wallet service. Each wallet service owns its accounts and journal in a separate database. Events leave each service through a transactional outbox and are delivered through NATS JetStream to the projector and other consumers.

```mermaid
C4Context
    title Digital Wallet system context
    Person(client, "Client / caller", "Creates transfers and reads wallet or transfer state")
    System(transfer, "Transfer service", "Orchestrates the transfer saga and owns transfer state")
    System_Ext(source, "Wallet source instance", "Owns source wallet accounts and journal")
    System_Ext(destination, "Wallet destination instance", "Owns destination wallet accounts and journal")
    System_Ext(nats, "NATS JetStream", "Durable event transport")
    System_Ext(projector, "Projector service", "Builds versioned CQRS read models")
    System_Ext(read_db, "Read database", "Balance, history, and transfer views")

    Rel(client, transfer, "Creates and inspects transfers", "HTTPS")
    Rel(transfer, source, "Reserve, capture, and compensate", "Internal HTTPS")
    Rel(transfer, destination, "Pending credit, finalize, and compensate", "Internal HTTPS")
    Rel(source, nats, "Publishes journal events", "Outbox relay")
    Rel(destination, nats, "Publishes journal events", "Outbox relay")
    Rel(transfer, nats, "Publishes saga events", "Outbox relay")
    Rel(nats, projector, "Delivers ordered, deduplicated events", "JetStream")
    Rel(projector, read_db, "Writes read models", "PostgreSQL")
    Rel(client, source, "Authoritative source-wallet reads", "HTTPS")
    Rel(client, read_db, "Eventually consistent queries", "Through API")
```

## Containers and component boundaries

```mermaid
flowchart TB
    Caller[Client]
    subgraph TransferService[transfer service module]
        TransferAPI[HTTP API]
        Saga[Saga orchestrator]
        TransferRepo[Transfer repository]
        TransferOutbox[Transfer outbox relay]
        TransferDB[(transfer database)]
    end
    subgraph WalletSource[wallet service instance: source]
        SourceAPI[HTTP API]
        SourceCommand[Command handler]
        SourceJournal[Journal repository]
        SourceOutbox[Outbox relay]
        SourceDB[(wallet_source database)]
    end
    subgraph WalletDestination[wallet service instance: destination]
        DestinationAPI[HTTP API]
        DestinationCommand[Command handler]
        DestinationJournal[Journal repository]
        DestinationOutbox[Outbox relay]
        DestinationDB[(wallet_destination database)]
    end
    NATS[(NATS JetStream)]
    Postgres[(PostgreSQL container)]
    subgraph ProjectorService[projector service module]
        Consumer[Consumer + inbox]
        Ordering[Aggregate ordering + gap detector]
        Projector[Projection workers]
        Rebuilder[Rebuild / promotion CLI]
        ReadDB[(read database)]
    end

    Caller --> TransferAPI
    TransferAPI --> Saga
    Saga --> TransferRepo --> TransferDB
    Saga --> SourceAPI
    Saga --> DestinationAPI
    Postgres --> SourceDB
    Postgres --> DestinationDB
    Postgres --> TransferDB
    Postgres --> ReadDB
    TransferDB --> TransferOutbox --> NATS
    SourceAPI --> SourceCommand --> SourceJournal --> SourceDB
    SourceDB --> SourceOutbox --> NATS
    DestinationAPI --> DestinationCommand --> DestinationJournal --> DestinationDB
    DestinationDB --> DestinationOutbox --> NATS
    NATS --> Consumer --> Ordering --> Projector --> ReadDB
    Rebuilder --> ReadDB

    TransferService -. no wallet schema access .-> SourceDB
    TransferService -. no wallet schema access .-> DestinationDB
```

The dashed relationship is a boundary rule, not a network connection. The transfer module may call wallet HTTP/internal APIs and consume contract events, but it must not import wallet storage packages or query wallet tables.

The Phase 2 Compose deployment uses one PostgreSQL container with four databases (`wallet_source`, `wallet_destination`, `transfer`, and `read`) and four database roles. This is a local-development topology only; service role grants remain database-scoped and the transfer role has no `CONNECT` privilege on either wallet database.

## Responsibilities and ownership

| Component | Owns | Does not own |
| --- | --- | --- |
| Wallet service instance | Wallet identity, accounts, immutable journal, idempotency records, outbox, local reservation state | Transfer saga state, read-model tables |
| Transfer service | Transfer aggregate, saga state, step attempts, idempotency, reconciliation hand-off, outbox | Wallet journal or wallet schema |
| Projector | Inbox, aggregate cursors/buffers, versioned balance/history/transfer views, projection versions | Any authoritative money state |
| NATS JetStream | Durable delivery and replay window | Business truth or deduplication correctness |
| PostgreSQL | Durable source of truth inside each service boundary | Cross-service atomicity |

## Service isolation

The Go workspace contains `services/wallet`, `services/transfer`, `services/projector`, and a narrow `shared` module. Wallet source and destination are the same wallet binary with different configuration, database URLs, tenant scope, and logical instance identity. Separate PostgreSQL roles enforce the database boundary. A CI check rejects transfer imports of wallet storage packages.

## Consistency vocabulary

- **Authoritative:** a journal-derived read from the owning wallet database. It can be recomputed at any time.
- **Read-model consistent through:** the highest event sequence or per-aggregate version known to have been applied to the selected projection version.
- **Eventually consistent:** a projector read that may lag the journal but reports its watermark.
- **Ambiguous:** a command's side effect cannot be classified as absent or confirmed; it must enter reconciliation rather than being retried blindly or compensated automatically.
