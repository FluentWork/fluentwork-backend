#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WITH_MYSQL=0
LOCAL_MYSQL=0
WITH_GATEWAY=1
AUTO_CORPUS_SEED="${AUTO_CORPUS_SEED:-1}"
PORT="${PORT:-8080}"
GATEWAY_PORT="${GATEWAY_PORT:-8081}"
# HOST is used for VOICE_GATEWAY_WSS_URL returned to iOS clients.
# For simulator: use 127.0.0.1 (default).
# For physical device: use the host machine's LAN IP (e.g., 192.168.x.x).
HOST="${HOST:-127.0.0.1}"

usage() {
  cat <<'EOF'
Start FluentWork app-server (and voice-gateway by default) for local development.

Usage:
  ./scripts/dev-up.sh [--mysql] [--local-mysql] [--no-gateway] [--port 8080] [--host IP]

Default mode uses the in-memory account/session store (no Docker required).
Pass --mysql to start MySQL 8 via Docker Compose and apply migrations.
Pass --local-mysql to use an already-running local MySQL (brew/etc.) with
  fw/fw credentials; migrations and corpus seed run automatically.
Pass --no-gateway to run only app-server.
Pass --host IP to set the WSS URL host (default: 127.0.0.1 for simulator;
  use LAN IP like 192.168.1.100 for physical device testing).
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mysql)
      WITH_MYSQL=1
      shift
      ;;
    --local-mysql)
      WITH_MYSQL=1
      LOCAL_MYSQL=1
      shift
      ;;
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
    # Strip inline `# comment` tails so values like
    # `ARK_API_KEY=ark-xxx  # Dev/POC` load the real key.
    value="${value%%#*}"
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
if [[ -f "$ROOT/.env.volc.local" ]]; then
  load_env_file "$ROOT/.env.volc.local"
fi

export APP_ENV="${APP_ENV:-development}"
export HTTP_ADDR="0.0.0.0:${PORT}"
export AUTH_JWT_SECRET="${AUTH_JWT_SECRET:-fluentwork-dev-jwt-secret-change-me!!}"
export INTERNAL_API_TOKEN="${INTERNAL_API_TOKEN:-fluentwork-dev-internal-token-change-me!!}"
export VOICE_GATEWAY_WSS_URL="ws://${HOST}:${GATEWAY_PORT}/v1/voice"
export APP_SERVER_INTERNAL_URL="http://127.0.0.1:${PORT}"
export VOICE_GATEWAY_HTTP_ADDR="0.0.0.0:${GATEWAY_PORT}"

COMPOSE_FILE="$ROOT/deploy/docker-compose.yml"

wait_for_url() {
  local url="$1"
  local name="$2"
  local pid="$3"
  local i
  for i in $(seq 1 60); do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      echo "${name} exited before becoming healthy" >&2
      return 1
    fi
    if curl -sf "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  echo "${name} did not become healthy at $url" >&2
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

apply_migrations_local() {
  local file
  for file in "$ROOT"/migrations/*.sql; do
    echo "Applying $(basename "$file") to local MySQL"
    mysql -h127.0.0.1 -ufw -pfw fluentwork < "$file"
  done
}

if [[ "$WITH_MYSQL" -eq 1 ]]; then
  if [[ "$LOCAL_MYSQL" -eq 1 ]]; then
    if ! command -v mysql >/dev/null 2>&1; then
      echo "mysql CLI is required for --local-mysql." >&2
      exit 1
    fi
    if ! mysqladmin ping -h127.0.0.1 --silent >/dev/null 2>&1; then
      echo "Local MySQL is not reachable. Start it first (brew services start mysql)." >&2
      exit 1
    fi
    mysql -uroot -e "CREATE DATABASE IF NOT EXISTS fluentwork CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;"
    mysql -uroot -e "CREATE USER IF NOT EXISTS 'fw'@'localhost' IDENTIFIED BY 'fw'; GRANT ALL PRIVILEGES ON fluentwork.* TO 'fw'@'localhost'; FLUSH PRIVILEGES;"
    mysql -uroot -e "CREATE USER IF NOT EXISTS 'fw'@'127.0.0.1' IDENTIFIED BY 'fw'; GRANT ALL PRIVILEGES ON fluentwork.* TO 'fw'@'127.0.0.1'; FLUSH PRIVILEGES;"
    apply_migrations_local
    export MYSQL_DSN="${MYSQL_DSN:-fw:fw@tcp(127.0.0.1:3306)/fluentwork?parseTime=true&charset=utf8mb4&loc=UTC}"
  else
    if ! command -v docker >/dev/null 2>&1; then
      echo "Docker is required for --mysql." >&2
      exit 1
    fi
    docker compose -f "$COMPOSE_FILE" up -d --wait mysql
    apply_migrations
    export MYSQL_DSN="${MYSQL_DSN:-fw:fw@tcp(127.0.0.1:3306)/fluentwork?parseTime=true&charset=utf8mb4&loc=UTC}"
  fi
else
  unset MYSQL_DSN || true
fi

# Every dev-up mode should produce reviews; with a shared MySQL store the
# app-server in-process worker is otherwise disabled (production uses the
# standalone cmd/worker).
if [[ "$WITH_MYSQL" -eq 1 ]]; then
  export APP_RUN_REVIEW_WORKER=1
fi

SERVER_PID=""
GATEWAY_PID=""

cleanup() {
  if [[ -n "${GATEWAY_PID}" ]] && kill -0 "$GATEWAY_PID" >/dev/null 2>&1; then
    kill "$GATEWAY_PID" >/dev/null 2>&1 || true
    wait "$GATEWAY_PID" >/dev/null 2>&1 || true
  fi
  if [[ -n "${SERVER_PID}" ]] && kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

echo "Starting app-server on http://${HOST}:${PORT}"
echo "  GET  /healthz"
echo "  GET  /readyz"
echo "  POST /api/v1/auth/guest"
echo "  POST /api/v1/sessions"
echo "  POST /internal/v1/tickets/consume"
echo

go run ./cmd/app-server &
SERVER_PID=$!

if ! wait_for_url "http://127.0.0.1:${PORT}/healthz" "app-server" "$SERVER_PID"; then
  exit 1
fi

if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  echo "Starting voice-gateway on ws://${HOST}:${GATEWAY_PORT}/v1/voice"
  go run ./cmd/voice-gateway &
  GATEWAY_PID=$!
  if ! wait_for_url "http://127.0.0.1:${GATEWAY_PORT}/healthz" "voice-gateway" "$GATEWAY_PID"; then
    exit 1
  fi
fi

echo "Healthy. Smoke-testing guest auth..."
curl -sS -H 'Content-Type: application/json' \
  -d '{"device_id":"local-dev-device"}' \
  "http://127.0.0.1:${PORT}/api/v1/auth/guest"
echo
if [[ "$AUTO_CORPUS_SEED" == "1" ]]; then
  echo "Seeding dev corpus (device_id=${SEED_DEVICE_ID:-corpus-seed-dev-device})..."
  go run ./cmd/corpus-seed \
    -base-url "http://127.0.0.1:${PORT}" \
    -device-id "${SEED_DEVICE_ID:-corpus-seed-dev-device}" | tail -2
fi
echo "📡 WSS URL for iOS: ws://${HOST}:${GATEWAY_PORT}/v1/voice"
echo "   Set LOCAL_HOST=${HOST} in Xcode scheme for physical device testing."
echo
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  echo "app-server (pid ${SERVER_PID}) and voice-gateway (pid ${GATEWAY_PID}) are running. Ctrl-C to stop."
  wait "$SERVER_PID" "$GATEWAY_PID"
else
  echo "app-server is running (pid ${SERVER_PID}). Ctrl-C to stop."
  wait "$SERVER_PID"
fi
