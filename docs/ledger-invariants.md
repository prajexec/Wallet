# Ledger invariants

The journal is append-only and is the only authoritative source of wallet money. A balance row, if introduced for a read model, is never authoritative and is never updated by a wallet write transaction.

## Entry model

Every journal transaction has one or more postings. A posting contains its own `tenant_id`, `currency`, `account_id`, signed side (`DEBIT` or `CREDIT`), and positive integer `amount_minor`. v1 accepts only `USD`. A transaction also has an immutable `aggregate_version` and an idempotency key.

The accounting convention is:

```text
transaction_total_debits  = Σ posting.amount_minor where side = DEBIT
transaction_total_credits = Σ posting.amount_minor where side = CREDIT
```

For every committed journal transaction:

```text
transaction_total_debits = transaction_total_credits > 0
```

The database validates this invariant before commit and rejects changes to committed journal rows through triggers.

## Balance formulas

For a wallet account `w` and currency `USD`:

```text
posted_credit(w) = Σ amount_minor for CREDIT postings on w
posted_debit(w)  = Σ amount_minor for DEBIT postings on w
current_balance(w) = posted_credit(w) - posted_debit(w)
```

Reservations are journal-backed. Let `held(w)` be the sum of active, non-captured, non-released reservation amounts reconstructed from reservation journal entries and their terminal events:

```text
available_balance(w) = current_balance(w) - held(w)
held_balance(w)      = held(w)
```

The write path never writes `current_balance` or `available_balance` to a mutable balance row. The authoritative endpoint computes the formula from the journal; the projector may materialize the same formula as a rebuildable view.

## Invariant table

| ID | Invariant | Enforcement | Failure handling |
| --- | --- | --- | --- |
| L-001 | Every journal transaction has equal, positive debit and credit totals. | Repository validation plus PostgreSQL constraint/trigger. | Reject transaction; no outbox row is committed. |
| L-002 | A posting has one tenant and currency; v1 currency is USD. | Composite foreign keys and check constraints. | Reject command with a typed validation error. |
| L-003 | Journal transactions and postings are append-only. | PostgreSQL UPDATE/DELETE triggers; application exposes no mutation method. | Database error and alert. |
| L-004 | A tenant cannot read or reference another tenant's rows. | `tenant_id` on every table, composite FKs, tenant-scoped repository methods. | Reject query/command; test cross-tenant access. |
| L-005 | A successful idempotency key maps to one immutable result. | Unique `(tenant_id, scope, idempotency_key)` and request fingerprint. | Return the original result for an identical request; conflict for a different fingerprint. |
| L-006 | A wallet cannot spend more than its available journal-derived amount. | Transaction-level lock on the wallet aggregate plus formula check before reserve/withdraw. | Return insufficient funds; never create a negative available balance. |
| L-007 | Aggregate versions are monotonic and applied once per consumer. | Version check, inbox unique key, and reorder buffer. | Buffer future version, ignore applied version, alert on persistent gap. |
| L-008 | Outbox event and journal change commit atomically. | Same PostgreSQL transaction. | Relay later; no polling race can lose the event. |
| L-009 | A completed transfer has equal source capture and destination finalization amounts. | Saga terminal check and reconciliation query. | Open an exception; do not auto-adjust either journal. |
| L-010 | Money cannot be created or destroyed silently. | Journal balancing, cross-service completion checks, replay checks. | Block completion or open reconciliation exception. |

## Atomic wallet write contract

One wallet write transaction does only the following:

1. resolve the tenant and idempotency record;
2. lock the wallet aggregate in a deterministic order;
3. validate the journal-derived available amount and command preconditions;
4. append journal transaction and postings;
5. append the domain event;
6. append the outbox row;
7. store the idempotent command result;
8. commit.

It does not update a mutable balance row and does not require NATS to be available. If PostgreSQL commits, the outbox relay can publish the event later.
