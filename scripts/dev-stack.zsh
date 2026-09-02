#!/usr/bin/env zsh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

COMPOSE_FILE="$ROOT/deploy/docker-compose.yml"
WITH_GATEWAY=1
PORT="${PORT:-8080}"
HOST="${HOST:-127.0.0.1}"

usage() {
  cat <<'EOF'
Start the full local FluentWork backend stack with Docker Compose.

Usage:
  ./scripts/dev-stack.zsh [--no-gateway] [--port 8080] [--host IP]

What it does:
  1. Starts MySQL + Redis via deploy/docker-compose.yml
  2. Applies migrations inside the MySQL container
  3. Starts app-server + voice-gateway via ./scripts/dev-local-start.sh --no-services

Examples:
  ./scripts/dev-stack.zsh
  ./scripts/dev-stack.zsh --host 192.168.1.100
  ./scripts/dev-stack.zsh --no-gateway

Stop compose services later with:
  ./scripts/dev-down.sh
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-gateway)
      WITH_GATEWAY=0
      shift
      ;;
    --port)
      PORT="$2"
      shift 2
      ;;
    --host)
      HOST="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is required for ./scripts/dev-stack.zsh." >&2
  exit 1
fi

if ! docker info >/dev/null 2>&1; then
  echo "Docker daemon is not available. Start Docker Desktop or Colima first." >&2
  exit 1
fi

apply_migrations() {
  local file
  for file in "$ROOT"/migrations/*.sql; do
    echo "Applying $(basename "$file")"
    docker compose -f "$COMPOSE_FILE" exec -T mysql \
      mysql -ufw -pfw fluentwork < "$file"
  done
}

echo "Starting MySQL + Redis via Docker Compose..."
docker compose -f "$COMPOSE_FILE" up -d --wait mysql redis
apply_migrations

echo
echo "Compose services are ready. Handing off to dev-local-start..."

args=(--no-services --port "$PORT" --host "$HOST")
if [[ "$WITH_GATEWAY" -eq 0 ]]; then
  args+=(--no-gateway)
fi

exec "$ROOT/scripts/dev-local-start.sh" "${args[@]}"
