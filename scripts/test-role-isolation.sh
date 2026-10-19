#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE=(docker compose -f "$ROOT_DIR/deploy/docker-compose.yml")

password="wallet_source_dev_password"
if "${COMPOSE[@]}" exec -T postgres env PGPASSWORD="$password" \
  psql -h 127.0.0.1 -U transfer_role -d wallet_source -v ON_ERROR_STOP=1 -c 'SELECT 1' \
  >/dev/null 2>&1; then
  printf 'transfer_role unexpectedly connected to wallet_source\n' >&2
  exit 1
fi

printf 'transfer_role cannot connect to wallet_source.\n'
