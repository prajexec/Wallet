# Repository instructions

## Commands and checks

- This is a four-module Go workspace (`shared`, `services/wallet`, `services/transfer`, `services/projector`), with no root `go.mod`. Use the root Make targets; root `go test ./...` is not a workspace-wide test command.
- Fast checks: `make lint && make test`. For Go changes also run `make build`, `make vet`, and `make test-race`; these iterate over all four modules.
- Focused example, with `services/projector` as the working directory: `go test -race -count=1 ./internal/projection -run '^TestApplyWalletLifecycle$'`.
- CI runs `make ci`: documentation rendering/OpenAPI lint, Go checks, sqlc freshness, import boundaries, database-role isolation, and real integration tests. It requires Docker Compose v2 and Node.js/npm, not just Go.
- `make lint` checks gofmt on **tracked** Go files and runs `bash -n scripts/*.sh`; format new untracked Go files explicitly. Boundary and Mermaid discovery also use Git-tracked files.

## Database and integration workflow

- Compose commands need `-f deploy/docker-compose.yml` (or the Make wrappers). `make up` starts the stack; `make migrate` applies demo migrations. Services do not run migrations at startup; `make migrate-test` migrates the separate test databases.
- Migrations use `*_MIGRATOR_DATABASE_URL` as the owner role; services and integration assertions use restricted runtime roles. Both wallet instances share `migrations/wallet`; the read database uses `migrations/projector`.
- `make test-integration` starts PostgreSQL/NATS, migrates `wallet_source_test`, `wallet_destination_test`, and `read_test`, then runs wallet/projector integration packages with `-race -count=1`.
- For manual integration runs, supply the runtime and cleanup/migrator test DSNs plus `NATS_TEST_STREAM=WALLET_EVENTS_TEST` and `NATS_TEST_SUBJECT_PREFIX=events-test`. Without the NATS overrides, helpers default to the demo stream/subjects. Never point tests at demo databases.
- An `ok` from `make test` or `make test-race` is **not integration evidence**: wallet tests exit successfully without `WALLET_TEST_DATABASE_URL`; projector tests skip on absent/unreachable `READ_TEST_DATABASE_URL`, and NATS helpers can skip on connection failure. Check verbose output for actual execution.
- `deploy/postgres/init/001-roles-and-databases.sql` runs only on a fresh PostgreSQL volume. Restarting an existing volume does not add newly introduced roles/databases; do not delete its data as an implicit setup fix.

## SQL generation and documentation

- Edit migrations and `services/{wallet,projector}/internal/storage/queries/*.sql`, then run `make sqlc` (pinned in `Makefile`). Commit the generated packages; do not hand-edit them. Wallet generation uses `pgx/v5`, projector generation uses `database/sql`.
- `make sqlc-check` regenerates, then compares SQL inputs **and** generated output against Git HEAD. Intentional uncommitted SQL/config changes can make it fail even when generation is fresh; inspect the diff rather than discarding it.
- `make docs-check` renders Mermaid from tracked `README.md`/`docs` files and lints `api/openapi.yaml`. It uses npm and Chromium; set `PUPPETEER_EXECUTABLE_PATH` to a native browser if the bundled executable has the wrong architecture.
- Contract details live in `api/openapi.yaml`, `docs/ledger-invariants.md`, `docs/event-model.md`, `docs/cqrs-projections.md`, and `docs/adr/`; operational commands live in `docs/operations.md`. Some prose is ahead of implementation: verify routes and wiring before claiming a capability exists.

## Boundaries and correctness traps

- Each service is wired in `services/<name>/main.go`. The two wallet instances are the **same binary**, with different database URLs/roles and instance names. The transfer server currently exposes health/readiness only; the documented saga is not implemented yet.
- Keep cross-service contracts in `shared`; do not import another service's internals or query its database. `make check-boundaries` enforces imports; `make role-isolation` checks real database permissions.
- Wallet balances come from the immutable double-entry journal, not a mutable balance row. Money is USD integer minor units; preserve tenant-scoped queries and composite keys. Projector folds use event payloads, not wallet database reads; authoritative verification goes through the token-protected wallet endpoint.
- Preserve the consumer aggregate advisory lock: a cursor `FOR UPDATE` alone cannot serialize the first event when no cursor row exists. Future versions must persist in `buffered_events` before ACK; never skip/fabricate versions, and ACK only after the database commit.
- Projection slots are fixed `a`/`b`, with `projection_versions.projection_name = 'wallet'`. `wallet_entry_view.entry_version` is a journal-entry ordinal, **not** the aggregate-event watermark. Unknown event types advance the cursor without a balance mutation; unsupported schema versions must not be interpreted.
- Wallet/projector `/internal/*` routes belong only on `INTERNAL_HTTP_ADDR`, require `X-Internal-Token`, and must remain 404 on public listeners. Compose publishes internal ports on loopback (`8181`, `8182`, `8184`). Public tenant-scoped wallet/read APIs require `X-Tenant-ID`.
- `make projectorctl` builds ignored `bin/projectorctl`; its database setting is `DATABASE_URL` (fallback `READ_DATABASE_URL`). `make dev-wipe-active-slot` deletes active read-model rows: disposable development only, never acceptance evidence or mismatch repair.
- Existing phase tags are immutable: never move, delete, or recreate them. Follow the repository's Conventional Commit history when a commit is requested.
