# ADR 008: Integer money and database-enforced journal immutability

Status: accepted
- **Date:** 2026-10-19

## Context

Floating-point arithmetic can produce rounding errors, and application-only append-only rules can be bypassed by an operational SQL session or future code.

## Decision

Represent money as signed 64-bit integer minor units, reject overflow/negative command amounts, and never use floating point. PostgreSQL triggers reject UPDATE and DELETE on journal transactions and postings after insertion. Corrections use new, linked reversal entries.

## Consequences

- Arithmetic is deterministic and serializable.
- Reversal workflows are explicit and increase journal volume.
- Migrations and maintenance tools must respect triggers; evidence cannot be silently edited.

## Alternatives considered

- **Decimal or float in application code:** rejected for portable, unambiguous integer accounting in v1.
- **Application-only immutability:** rejected because direct SQL access could rewrite history.
- **Hard delete for privacy:** rejected for financial evidence; redaction of non-monetary metadata must be designed separately.
