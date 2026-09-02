#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WITH_GATEWAY=1
WITH_SERVICES=1
PORT="${PORT:-8080}"
GATEWAY_PORT="${GATEWAY_PORT:-8081}"
# HOST is used for WSS URL returned to iOS clients.
# For simulator: use 127.0.0.1 (default).
# For physical device: use the host machine's LAN IP (e.g., 192.168.x.x).
HOST="${HOST:-127.0.0.1}"

usage() {
  cat <<'EOF'
Start FluentWork with local MySQL + Redis + backend servers for development.

Usage:
  ./scripts/dev-local-start.sh [--no-gateway] [--no-services] [--port 8080]

Options:
  --no-gateway     Skip starting voice-gateway
  --no-services    Assume MySQL/Redis are already running (skip brew services start)
  --port 8080      HTTP port for app-server (default: 8080)
  --host IP        Host IP for WSS URL (default: 127.0.0.1 for simulator;
                   use LAN IP like 192.168.1.100 for physical device testing)

Examples:
  ./scripts/dev-local-start.sh                        # Simulator: 127.0.0.1
  ./scripts/dev-local-start.sh --host 192.168.1.100   # Physical device on LAN
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-gateway)
      WITH_GATEWAY=0
      shift
      ;;
    --no-services)
      WITH_SERVICES=0
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

# ---------------------------------------------------------------------------
# 1. Start MySQL and Redis via brew services (unless --no-services)
# ---------------------------------------------------------------------------
if [[ "$WITH_SERVICES" -eq 1 ]]; then
  echo "🚀 Starting MySQL and Redis via brew services..."

  # MySQL
  if brew services list 2>/dev/null | grep -q "mysql.*started"; then
    echo "✅ MySQL is already running"
  else
    echo "▶️  Starting MySQL..."
    brew services start mysql 2>/dev/null || echo "⚠️  Could not start MySQL via brew (may already be running)"
    for i in $(seq 1 30); do
      if mysqladmin ping -h127.0.0.1 --silent 2>/dev/null; then
        echo "✅ MySQL is ready"
        break
      fi
      if [ $i -eq 30 ]; then
        echo "❌ MySQL failed to start within 30 seconds" >&2
        exit 1
      fi
      sleep 1
    done
  fi

  # Redis
  if brew services list 2>/dev/null | grep -q "redis.*started"; then
    echo "✅ Redis is already running"
  else
    echo "▶️  Starting Redis..."
    brew services start redis 2>/dev/null || echo "⚠️  Could not start Redis via brew (may already be running)"
    for i in $(seq 1 10); do
      if redis-cli ping 2>/dev/null | grep -q "PONG"; then
        echo "✅ Redis is ready"
        break
      fi
      if [ $i -eq 10 ]; then
        echo "❌ Redis failed to start within 10 seconds" >&2
        exit 1
      fi
      sleep 1
    done
  fi
fi

# ---------------------------------------------------------------------------
# 2. Check MySQL and Redis are reachable
# ---------------------------------------------------------------------------
echo "🔍 Checking MySQL connection..."
if ! mysqladmin ping -h127.0.0.1 -ufw -pfw --silent 2>/dev/null; then
  echo "❌ MySQL is not accessible. Run with --no-services if already running, or start MySQL manually." >&2
  exit 1
fi
echo "✅ MySQL is ready"

echo "🔍 Checking Redis connection..."
if ! redis-cli ping 2>/dev/null | grep -q "PONG"; then
  echo "❌ Redis is not accessible. Run with --no-services if already running, or start Redis manually." >&2
  exit 1
fi
echo "✅ Redis is ready"

# ---------------------------------------------------------------------------
# 3. Configure host and ports
# ---------------------------------------------------------------------------
# HOST defaults to 127.0.0.1 (simulator). Override with --host for physical device.
echo "📡 Using host IP for WSS URL: $HOST"

# ---------------------------------------------------------------------------
# 4. Load .env.dev (sh-compatible: no process substitution)
# ---------------------------------------------------------------------------
if [[ -f "$ROOT/.env.dev" ]]; then
  echo "📋 Loading .env.dev configuration..."
  set -a
  _env_tmp=$(mktemp)
  grep -v '^#' "$ROOT/.env.dev" | grep -v '^$' > "$_env_tmp"
  while IFS='=' read -r key value; do
    value="${value%\"}"
    value="${value#\"}"
    export "$key=$value"
  done < "$_env_tmp"
  rm -f "$_env_tmp"
  set +a
else
  echo "❌ .env.dev not found. Run from project root." >&2
  exit 1
fi

# Overlay .env.volc.local if present (gitignored) — supplies volc-duplex keys.
# Values here take precedence so the gateway can boot with a real provider
# without editing the committed .env.dev.
if [[ -f "$ROOT/.env.volc.local" ]]; then
  echo "📋 Loading .env.volc.local (overrides volc-related vars)..."
  set -a
  _env_tmp=$(mktemp)
  grep -v '^#' "$ROOT/.env.volc.local" | grep -v '^$' > "$_env_tmp"
  while IFS='=' read -r key value; do
    value="${value%\"}"
    value="${value#\"}"
    export "$key=$value"
  done < "$_env_tmp"
  rm -f "$_env_tmp"
  set +a
fi

# Override ports and WSS URL.
# Bind to 0.0.0.0 so iOS physical device on LAN can reach the services.
# Use $HOST in VOICE_GATEWAY_WSS_URL so the iOS app connects to the right address.
export HTTP_ADDR="0.0.0.0:${PORT}"
export VOICE_GATEWAY_HTTP_ADDR="0.0.0.0:${GATEWAY_PORT}"
export VOICE_GATEWAY_WSS_URL="ws://${HOST}:${GATEWAY_PORT}/v1/voice"
export APP_SERVER_INTERNAL_URL="http://127.0.0.1:${PORT}"

# ---------------------------------------------------------------------------
# 4. Start app-server
# ---------------------------------------------------------------------------
SERVER_PID=""
GATEWAY_PID=""

cleanup() {
  echo ""
  echo "🛑 Shutting down..."
  if [[ -n "${GATEWAY_PID}" ]] && kill -0 "$GATEWAY_PID" >/dev/null 2>&1; then
    kill "$GATEWAY_PID" >/dev/null 2>&1 || true
    wait "$GATEWAY_PID" >/dev/null 2>&1 || true
  fi
  if [[ -n "${SERVER_PID}" ]] && kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" >/dev/null 2>&1 || true
  fi
  echo "✅ Stopped."
}
trap cleanup EXIT INT TERM

echo ""
echo "🚀 Starting app-server on http://${HOST}:${PORT}"

go run ./cmd/app-server &
SERVER_PID=$!

for i in $(seq 1 60); do
  if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    echo "❌ app-server exited before becoming healthy" >&2
    exit 1
  fi
  if curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
    echo "✅ app-server is healthy"
    break
  fi
  if [ $i -eq 60 ]; then
    echo "❌ app-server did not become healthy" >&2
    exit 1
  fi
  sleep 0.5
done

# ---------------------------------------------------------------------------
# 5. Start voice-gateway (optional)
# ---------------------------------------------------------------------------
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  echo "🚀 Starting voice-gateway on ws://${HOST}:${GATEWAY_PORT}/v1/voice"
  go run ./cmd/voice-gateway &
  GATEWAY_PID=$!

  for i in $(seq 1 60); do
    if ! kill -0 "$GATEWAY_PID" >/dev/null 2>&1; then
      echo "❌ voice-gateway exited before becoming healthy" >&2
      exit 1
    fi
    if curl -sf "http://127.0.0.1:${GATEWAY_PORT}/healthz" >/dev/null 2>&1; then
      echo "✅ voice-gateway is healthy"
      break
    fi
    if [ $i -eq 60 ]; then
      echo "❌ voice-gateway did not become healthy" >&2
      exit 1
    fi
    sleep 0.5
  done
fi

# ---------------------------------------------------------------------------
# 6. Smoke test
# ---------------------------------------------------------------------------
echo ""
echo "✅ Smoke-testing guest auth..."
curl -sS -H 'Content-Type: application/json' \
  -d '{"device_id":"local-dev-device"}' \
  "http://127.0.0.1:${PORT}/api/v1/auth/guest"
echo ""

if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  echo "✅ All services running."
  echo "   app-server:     http://${HOST}:${PORT}"
  echo "   voice-gateway:   ws://${HOST}:${GATEWAY_PORT}/v1/voice"
  echo "   MySQL:           127.0.0.1:3306"
  echo "   Redis:           127.0.0.1:6379"
  echo ""
  echo "   iOS app:        Set LOCAL_HOST=${HOST} in Xcode scheme"
  echo "   Ctrl-C to stop."
  wait "$SERVER_PID" "$GATEWAY_PID"
else
  echo "✅ app-server running on http://${HOST}:${PORT}. Ctrl-C to stop."
  wait "$SERVER_PID"
fi
