#!/usr/bin/env bash
#
# Seed the dev app-server with a starter set of phrase blocks so that
# B12 hit detection has real anchors to match against when exercising the
# speaking-room → server → badge feedback loop locally.
#
# Default target: http://127.0.0.1:8080 (the port `./scripts/dev-up.sh` uses).
# Override with: ./scripts/corpus-seed.sh http://host:8080
#
# Re-running adds another batch (each run uses a fresh source_session_id);
# the list count grows by N each time.
#
# This is a developer convenience only — never wire this into prod data
# paths.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required. Install Go 1.26+ and retry." >&2
  exit 1
fi

BASE_URL="${1:-${APP_BASE_URL:-http://127.0.0.1:8080}}"

echo "🌱  Seeding dev corpus at ${BASE_URL}"
exec go run ./cmd/corpus-seed -base-url "${BASE_URL}"
