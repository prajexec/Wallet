#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/digital-wallet-docs.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

if [[ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" && -z "${PUPPETEER_EXECUTABLE_PATH:-}" ]]; then
  export PUPPETEER_EXECUTABLE_PATH="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
fi
if [[ -z "${PUPPETEER_EXECUTABLE_PATH:-}" ]]; then
  for browser in chromium chromium-browser google-chrome; do
    if command -v "$browser" >/dev/null 2>&1; then
      export PUPPETEER_EXECUTABLE_PATH="$(command -v "$browser")"
      break
    fi
  done
fi

DOCS=()
while IFS= read -r -d '' doc; do
  DOCS+=("$doc")
done < <(
  cd "$ROOT_DIR"
  git ls-files -z -- README.md docs
)

rendered=0
PUPPETEER_CONFIG="$WORK_DIR/puppeteer.json"
if [[ "$(id -u)" -eq 0 || "${PUPPETEER_NO_SANDBOX:-}" == "1" || "${CI:-}" == "true" ]]; then
  printf '%s\n' '{"args":["--no-sandbox","--disable-setuid-sandbox"]}' > "$PUPPETEER_CONFIG"
else
  printf '%s\n' '{}' > "$PUPPETEER_CONFIG"
fi
for doc in "${DOCS[@]}"; do
  [[ -n "$doc" ]] || continue
  doc_work_dir="$WORK_DIR/${doc//\//__}"
  mkdir -p "$doc_work_dir"

  awk -v output_dir="$doc_work_dir" '
    /^```mermaid[[:space:]]*$/ {
      block += 1
      output = output_dir "/block-" block ".mmd"
      in_block = 1
      next
    }
    in_block && /^```[[:space:]]*$/ {
      close(output)
      in_block = 0
      next
    }
    in_block { print > output }
    END {
      if (in_block) {
        print FILENAME ": unterminated Mermaid block" > "/dev/stderr"
        exit 1
      }
    }
  ' "$ROOT_DIR/$doc"

  for source in "$doc_work_dir"/*.mmd; do
    [[ -f "$source" ]] || continue
    output="${source%.mmd}.svg"
    npx --yes -p @mermaid-js/mermaid-cli@11.12.0 mmdc \
      --input "$source" \
      --output "$output" \
      --puppeteerConfigFile "$PUPPETEER_CONFIG" \
      --quiet
    rendered=$((rendered + 1))
  done
done

npx --yes @redocly/cli lint "$ROOT_DIR/api/openapi.yaml"
printf 'Rendered %d Mermaid blocks and linted api/openapi.yaml\n' "$rendered"
