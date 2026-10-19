#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/digital-wallet-boundaries.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

mkdir -p "$WORK_DIR/scripts"
cp "$ROOT_DIR/scripts/check-boundaries.sh" "$WORK_DIR/scripts/check-boundaries.sh"
mkdir -p "$WORK_DIR/services/transfer"
cat > "$WORK_DIR/services/transfer/violation.go" <<'EOF'
package transfer

import _ "digitalwallet/services/wallet/internal/storage"

EOF

(
  cd "$WORK_DIR"
  git init -q
  git add scripts/check-boundaries.sh services/transfer/violation.go
  if ./scripts/check-boundaries.sh >/dev/null 2>&1; then
    printf 'boundary checker did not reject the deliberate violation\n' >&2
    exit 1
  fi
)

printf 'Deliberate boundary violation was rejected.\n'
