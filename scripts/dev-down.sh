#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -f "$ROOT/deploy/docker-compose.yml" ]] && command -v docker >/dev/null 2>&1; then
  docker compose -f "$ROOT/deploy/docker-compose.yml" down
fi

echo "Local MySQL (if started) has been stopped. app-server is not managed by this script; use Ctrl-C on ./scripts/dev-up.sh."
