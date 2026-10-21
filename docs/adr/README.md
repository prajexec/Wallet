# Architecture decision records

These ADRs record decisions that shape the wallet's correctness, ownership, and
recovery behavior. The implementation and executable checks remain authoritative;
an ADR explains why the boundary or invariant exists.

## Decision index

| ADR | Decision |
| --- | --- |
| [001](001-no-central-ledger.md) | No central ledger |
| [002](002-journal-is-source-of-truth.md) | The journal is the source of truth |
| [003](003-posting-currency.md) | Posting currency policy |
| [004](004-outbox-inbox-ordering.md) | Outbox/inbox ordering |
| [005](005-saga-failure-model.md) | Saga failure model |
| [006](006-versioned-projection-rebuild.md) | Versioned projection rebuild |
| [007](007-module-and-database-boundaries.md) | Module and database boundaries |
| [008](008-integer-money-and-immutable-journal.md) | Integer money and immutable journal |
| [009](009-goose-migrations.md) | Goose migrations |
| [010](010-standard-library-http.md) | Standard-library HTTP |
| [011](011-compose-postgres-topology.md) | Compose PostgreSQL topology |
| [012](012-duplicated-http-middleware.md) | Duplicated HTTP middleware |
| [013](013-account-lock-concurrency.md) | Account-lock concurrency |
| [014](014-sqlc-generated-wallet-storage.md) | SQLC-generated wallet storage |
| [015](015-owner-separated-migrations-and-runtime-grants.md) | Owner-separated migrations and runtime grants |
| [016](016-outbox-relay-lease-and-jetstream.md) | Outbox relay leases and JetStream |
| [017](017-shared-ordering-decisions.md) | Shared ordering decisions |
| [018](018-advisory-lock-runtime-isolation.md) | Advisory-lock runtime isolation |
| [019](019-consumer-aggregate-advisory-locking.md) | Consumer aggregate advisory locking |
| [020](020-per-wallet-consistency-cursor.md) | Per-wallet consistency cursor |
| [021](021-payload-only-cqrs-projections.md) | Payload-only CQRS projections |
| [022](022-dual-slot-projections-without-runtime-ddl.md) | Dual-slot projections without runtime DDL |
| [023](023-shadow-rebuild-and-promotion-gates.md) | Shadow rebuild and promotion gates |
| [024](024-cross-tenant-internal-verification.md) | Cross-tenant internal verification |

## How to use ADRs

- Read the relevant ADR before changing a cross-service boundary or financial
  invariant.
- Update an existing ADR only when the decision itself changes; use a new ADR
  for a materially different decision.
- Link implementation changes to the relevant ADR in the commit or pull request.
- Do not treat an ADR as permission to claim a route or behavior that the code
  and tests do not implement.
