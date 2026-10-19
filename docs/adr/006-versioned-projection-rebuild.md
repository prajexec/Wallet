# ADR 006: Versioned projection rebuild and atomic promotion

- **Status:** Accepted
- **Date:** 2026-10-19

## Context

Read models are derived data and will need schema changes, recovery after deletion, and deterministic validation without interrupting reads.

## Decision

Build every rebuild into inactive versioned tables. Replay, catch up to a declared watermark, validate checksums/row counts/invariants, and atomically update `projection_versions`. Retain the prior version for rollback.

## Consequences

- Rebuilds consume temporary storage and need a promotion lock.
- Readers resolve an active pointer and can report consistency watermarks.
- A failed rebuild never corrupts or replaces the active read model.

## Alternatives considered

- **Truncate and rebuild active tables:** rejected because readers would observe partial data.
- **In-place migration:** rejected because it cannot provide a clean rollback boundary.
- **Treat read models as authoritative:** rejected because they are derived and replayable.
