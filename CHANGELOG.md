# Changelog

All notable changes to this project are documented here.

## [Unreleased]

## [Phase 1 — Contracts and documentation] — 2026-10-19

### Added

- Added the architecture, ledger, event, saga, outbox/inbox, projection, reconciliation, data model, API, testing, benchmarking, and operations contracts.
- Added eight ADRs covering the locked architecture decisions.
- Added Mermaid diagrams for all required system flows and data ownership boundaries.
- Added the initial OpenAPI contract for public and internal endpoints.

### Verification

- `git diff --check` passed for every staged documentation unit.
- OpenAPI YAML parsed successfully with Ruby (`13` paths); a structural documentation check confirmed all required files, Mermaid blocks, diagram constructs, and balanced fences.
- A Mermaid CLI renderer was not installed in the environment, so visual rendering was not claimed; no Go checks apply because Phase 1 contains no Go code.
