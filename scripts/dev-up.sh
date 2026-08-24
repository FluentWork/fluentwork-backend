#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WITH_MYSQL=0
PORT="${PORT:-8080}"

usage() {
  cat <<'EOF'
Start FluentWork app-server for local development.

Usage:
  ./scripts/dev-up.sh [--mysql] [--port 8080]

Default mode uses the in-memory account store (no Docker required).
Pass --mysql to start MySQL 8 via Docker Compose and apply migrations.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mysql)
      WITH_MYSQL=1
      shift
      ;;
    --port)
      PORT="$2"
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

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required. Install Go 1.26+ and retry." >&2
  exit 1
fi

load_env_file() {
  local file="$1"
  local line key value
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in
      ''|\#*) continue ;;
    esac
    key="${line%%=*}"
    value="${line#*=}"
    key="${key%"${key##*[![:space:]]}"}"
    key="${key#"${key%%[![:space:]]*}"}"
    [[ -z "$key" ]] && continue
    export "$key=$value"
  done < "$file"
}

if [[ -f "$ROOT/.env" ]]; then
  load_env_file "$ROOT/.env"
elif [[ -f "$ROOT/configs/app-server.env.example" ]]; then
  load_env_file "$ROOT/configs/app-server.env.example"
fi

export APP_ENV="${APP_ENV:-development}"
export HTTP_ADDR=":${PORT}"
export AUTH_JWT_SECRET="${AUTH_JWT_SECRET:-fluentwork-dev-jwt-secret-change-me!!}"

COMPOSE_FILE="$ROOT/deploy/docker-compose.yml"

wait_for_health() {
  local url="http://127.0.0.1:${PORT}/healthz"
  local i
  for i in $(seq 1 60); do
    if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
      echo "app-server exited before becoming healthy" >&2
      return 1
    fi
    if curl -sf "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  echo "app-server did not become healthy at $url" >&2
  return 1
}

apply_migrations() {
  local file
  for file in "$ROOT"/migrations/*.sql; do
    echo "Applying $(basename "$file")"
    docker compose -f "$COMPOSE_FILE" exec -T mysql \
      mysql -ufw -pfw fluentwork < "$file"
  done
}

if [[ "$WITH_MYSQL" -eq 1 ]]; then
  if ! command -v docker >/dev/null 2>&1; then
    echo "Docker is required for --mysql." >&2
    exit 1
  fi
  docker compose -f "$COMPOSE_FILE" up -d --wait mysql
  apply_migrations
  export MYSQL_DSN="${MYSQL_DSN:-fw:fw@tcp(127.0.0.1:3306)/fluentwork?parseTime=true&charset=utf8mb4&loc=UTC}"
else
  unset MYSQL_DSN || true
fi

echo "Starting app-server on http://127.0.0.1:${PORT}"
echo "  GET  /healthz"
echo "  GET  /readyz"
echo "  POST /api/v1/auth/guest"
echo "  POST /api/v1/account/merge"
echo

go run ./cmd/app-server &
SERVER_PID=$!

cleanup() {
  if kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

if ! wait_for_health; then
  exit 1
fi

echo "Healthy. Smoke-testing guest auth..."
curl -sS -H 'Content-Type: application/json' \
  -d '{"device_id":"local-dev-device"}' \
  "http://127.0.0.1:${PORT}/api/v1/auth/guest"
echo
echo
echo "app-server is running (pid ${SERVER_PID}). Ctrl-C to stop."
wait "$SERVER_PID"
