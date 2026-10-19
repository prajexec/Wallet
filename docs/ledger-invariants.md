# Ledger invariants

The journal is append-only and is the only authoritative source of wallet money. Phase 3 has no mutable balance row anywhere. A read model introduced later is derived data and is never authoritative.

## Entry model

Every journal transaction has one or more postings. A posting contains its own `tenant_id`, `currency`, `account_id`, signed side (`DEBIT` or `CREDIT`), entry type, and positive integer `amount_minor`. v1 accepts only `USD`. Wallet event aggregate versions are stored on `events`, not on journal transactions.

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

For an account `a` and currency `c`:

```text
posted_credit(a,c) = Σ amount_minor for CREDIT postings on a and c
posted_debit(a,c)  = Σ amount_minor for DEBIT postings on a and c
posted_balance(a,c) = posted_credit(a,c) - posted_debit(a,c)
```

For a `user_wallet` account, the API's available amount is:

```text
active_holds(a,c) = Σ active hold amounts for a and c
available_balance(a,c) = posted_balance(a,c) - active_holds(a,c)
```

Phase 3 has no holds table and therefore `active_holds` is always zero. Reserve/capture/release will plug into this term in Phase 6. The write path never writes a mutable balance row. The authoritative endpoint computes the formula from the journal.

Only `user_wallet` accounts must have `available_balance >= 0`. `system_clearing` accounts may go negative because they represent the other side of a wallet operation while external settlement is pending. The four per-tenant clearing accounts are `deposit-clearing`, `withdrawal-clearing`, `outbound-clearing`, and `inbound-clearing`.

## Invariant table

| ID | Invariant | Enforcement | Failure handling |
| --- | --- | --- | --- |
| L-001 | Every journal transaction has equal, positive debit and credit totals. | Repository validation plus PostgreSQL constraint/trigger. | Reject transaction; no outbox row is committed. |
| L-002 | A posting has one tenant and currency; v1 currency is USD. | Composite foreign keys and check constraints. | Reject command with a typed validation error. |
| L-003 | Journal transactions and postings are append-only. | PostgreSQL UPDATE/DELETE triggers; application exposes no mutation method. | Database error and alert. |
| L-004 | A tenant cannot read or reference another tenant's rows. | `tenant_id` on every table, composite FKs, tenant-scoped repository methods. | Reject query/command; test cross-tenant access. |
| L-005 | A successful idempotency key maps to one immutable result. | Unique `(tenant_id, key)` and request fingerprint. | Return the original result for an identical request; return 422 for a different fingerprint. |
| L-006 | A user wallet cannot spend more than its available journal-derived amount. | `SELECT ... FOR UPDATE` on the account row, then balance query and formula check in the same transaction. | Return insufficient funds; never create a negative available balance. |
| L-007 | Aggregate versions are monotonic and applied once per consumer. | Version check, inbox unique key, and reorder buffer. | Buffer future version, ignore applied version, alert on persistent gap. |
| L-008 | Outbox event and journal change commit atomically. | Same PostgreSQL transaction. | Relay later; no polling race can lose the event. |
| L-009 | A completed transfer has equal source capture and destination finalization amounts. | Saga terminal check and reconciliation query. | Open an exception; do not auto-adjust either journal. |
| L-010 | Money cannot be created or destroyed silently. | Journal balancing, cross-service completion checks, replay checks. | Block completion or open reconciliation exception. |
| L-011 | A transaction balances independently for every posting currency. | Deferred constraint trigger at commit. | Roll back the transaction. |
| L-012 | Journal transactions, postings, and events are immutable. | UPDATE, DELETE, and TRUNCATE triggers plus INSERT/SELECT-only grants. | Database error and alert. |
| L-013 | A tenant cannot reference another tenant's account or journal row. | Composite tenant foreign keys and tenant-scoped repositories. | Database error or API 404. |
| L-014 | An idempotency key has one request hash and one monetary result. | Unique `(tenant_id, key)` and same-transaction response record. | Replay stored response or return 422 for hash mismatch. |

## Atomic wallet write contract

One wallet write transaction does only the following:

1. resolve the explicit `tenant_id` from `X-Tenant-ID` and the idempotency record;
2. lock the wallet account row with `SELECT ... FOR UPDATE`;
3. validate the journal-derived available amount and command preconditions;
4. append journal transaction and postings;
5. append the domain event;
6. append the outbox row;
7. store the idempotent command result;
8. commit.

It does not update a mutable balance row and does not require NATS to be available. If PostgreSQL commits, the outbox relay can publish the event later. The account lock serializes wallet writes; the unique event aggregate/version constraint is a second safety net that fails loudly if a missed lock would otherwise produce a duplicate version.
