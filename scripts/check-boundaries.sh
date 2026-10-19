#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

violations=0

report_matches() {
  local label="$1"
  local pattern="$2"
  shift 2
  local matches
  matches="$(git grep -nE "$pattern" -- "$@" || true)"
  if [[ -n "$matches" ]]; then
    printf 'Boundary violation (%s):\n%s\n' "$label" "$matches" >&2
    violations=1
  fi
}

for service in wallet transfer projector; do
  for other in wallet transfer projector; do
    [[ "$service" == "$other" ]] && continue
    report_matches \
      "$service imports $other/internal" \
      "digitalwallet/services/${other}/internal/" \
      "services/${service}"
  done
done

report_matches \
  "transfer imports wallet storage or projector internals" \
  'digitalwallet/services/(wallet|projector)/(internal|storage|repository|repositories)/' \
  'services/transfer'

if [[ "$violations" -ne 0 ]]; then
  exit 1
fi

printf 'Import boundaries passed.\n'
