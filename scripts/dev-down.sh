#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -f "$ROOT/deploy/docker-compose.yml" ]] && command -v docker >/dev/null 2>&1; then
  docker compose -f "$ROOT/deploy/docker-compose.yml" down
fi

echo "Local compose services (MySQL / Redis, if started) have been stopped. app-server and voice-gateway are not managed by this script; use Ctrl-C on the active dev script."
