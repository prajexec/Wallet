#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE=(docker compose -f "$ROOT_DIR/deploy/docker-compose.yml")

run_psql() {
  local role="$1"
  local password="$2"
  local database="$3"
  "${COMPOSE[@]}" exec -T postgres env PGPASSWORD="$password" \
    psql -h 127.0.0.1 -U "$role" -d "$database" -v ON_ERROR_STOP=1 -c 'SELECT 1' \
    >/dev/null 2>&1
}

run_psql_command() {
  local role="$1"
  local password="$2"
  local database="$3"
  local command="$4"
  "${COMPOSE[@]}" exec -T postgres env PGPASSWORD="$password" \
    psql -h 127.0.0.1 -U "$role" -d "$database" -v ON_ERROR_STOP=1 -c "$command" \
    >/dev/null 2>&1
}

assert_allowed() {
  local role="$1"
  local password="$2"
  local database="$3"
  if ! run_psql "$role" "$password" "$database"; then
    printf 'role matrix setup failed: %s should connect to %s\n' "$role" "$database" >&2
    exit 1
  fi
  printf 'allowed: %s -> %s\n' "$role" "$database"
}

assert_denied() {
  local role="$1"
  local password="$2"
  local database="$3"
  if run_psql "$role" "$password" "$database"; then
    printf 'unexpected access: %s connected to %s\n' "$role" "$database" >&2
    exit 1
  fi
  printf 'denied: %s -> %s\n' "$role" "$database"
}

assert_sql_denied() {
  local role="$1"
  local password="$2"
  local database="$3"
  local command="$4"
  if run_psql_command "$role" "$password" "$database" "$command"; then
    printf 'unexpected SQL access: %s executed on %s: %s\n' "$role" "$database" "$command" >&2
    exit 1
  fi
  printf 'denied SQL: %s on %s: %s\n' "$role" "$database" "$command"
}

# First prove every credential works on its own database. A blanket failure
# would otherwise make a broken password or unavailable server look like a
# successful isolation test.
assert_allowed wallet_source_role wallet_source_dev_password wallet_source
assert_allowed wallet_destination_role wallet_destination_dev_password wallet_destination
assert_allowed transfer_role transfer_dev_password transfer
assert_allowed projector_role projector_dev_password read

databases=(wallet_source wallet_destination transfer read)
role_matrix=(
  'transfer_role|transfer_dev_password|transfer'
  'wallet_source_role|wallet_source_dev_password|wallet_source'
  'wallet_destination_role|wallet_destination_dev_password|wallet_destination'
  'projector_role|projector_dev_password|read'
)

for role_entry in "${role_matrix[@]}"; do
  IFS='|' read -r role password database <<< "$role_entry"
  for denied_database in "${databases[@]}"; do
    [[ "$database" == "$denied_database" ]] && continue
    assert_denied "$role" "$password" "$denied_database"
  done
done

wallet_role_matrix=(
  'wallet_source_role|wallet_source_dev_password|wallet_source'
  'wallet_destination_role|wallet_destination_dev_password|wallet_destination'
)
immutable_denials=(
  'ALTER TABLE journal_postings DISABLE TRIGGER ALL'
  'DROP TRIGGER journal_postings_immutable_row ON journal_postings'
  'DROP TABLE journal_postings'
  'UPDATE journal_postings SET amount_minor = amount_minor WHERE false'
  'UPDATE accounts SET name = name WHERE false'
  'UPDATE wallets SET status = status WHERE false'
  'UPDATE idempotency_records SET response_body = response_body WHERE false'
)

for role_entry in "${wallet_role_matrix[@]}"; do
  IFS='|' read -r role password database <<< "$role_entry"
  for command in "${immutable_denials[@]}"; do
    assert_sql_denied "$role" "$password" "$database" "$command"
  done
done

printf 'Role isolation matrix passed.\n'
