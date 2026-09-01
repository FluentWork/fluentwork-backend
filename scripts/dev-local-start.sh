#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WITH_GATEWAY=1
PORT="${PORT:-8080}"
GATEWAY_PORT="${GATEWAY_PORT:-8081}"

usage() {
  cat <<'EOF'
Start FluentWork with local MySQL + Redis for development.

Usage:
  ./scripts/dev-local-start.sh [--no-gateway] [--port 8080]

Requires:
  - MySQL running on 127.0.0.1:3306
  - Redis running on 127.0.0.1:6379
  - Run ./scripts/local-services-start.sh first if not already running
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

# Load .env.dev
if [[ -f "$ROOT/.env.dev" ]]; then
  echo "📋 Loading .env.dev configuration..."
  set -a
  while IFS='=' read -r key value; do
    # Skip comments and empty lines
    [[ "$key" =~ ^#.*$ || -z "$key" ]] && continue
    # Remove surrounding quotes if present
    value="${value%\"}"
    value="${value#\"}"
    export "$key=$value"
  done < <(grep -v '^#' "$ROOT/.env.dev" | grep -v '^$')
  set +a
else
  echo "❌ .env.dev not found. Run from project root." >&2
  exit 1
fi

# Override ports
export HTTP_ADDR=":${PORT}"
export VOICE_GATEWAY_HTTP_ADDR=":${GATEWAY_PORT}"
export VOICE_GATEWAY_WSS_URL="ws://127.0.0.1:${GATEWAY_PORT}/v1/voice"
export APP_SERVER_INTERNAL_URL="http://127.0.0.1:${PORT}"

# Check MySQL
echo "🔍 Checking MySQL connection..."
if ! mysqladmin ping -h127.0.0.1 -ufw -pfw --silent 2>/dev/null; then
  echo "❌ MySQL is not accessible. Run ./scripts/local-services-start.sh first." >&2
  exit 1
fi
echo "✅ MySQL is ready"

# Check Redis
echo "🔍 Checking Redis connection..."
if ! redis-cli ping 2>/dev/null | grep -q "PONG"; then
  echo "❌ Redis is not accessible. Run ./scripts/local-services-start.sh first." >&2
  exit 1
fi
echo "✅ Redis is ready"

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

echo ""
echo "🚀 Starting app-server on http://127.0.0.1:${PORT}"
echo "  GET  /healthz"
echo "  GET  /readyz"
echo "  POST /api/v1/auth/guest"
echo "  POST /api/v1/sessions"
echo "  POST /internal/v1/tickets/consume"
echo ""

go run ./cmd/app-server &
SERVER_PID=$!

# Wait for app-server
for i in $(seq 1 60); do
  if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    echo "❌ app-server exited before becoming healthy" >&2
    exit 1
  fi
  if curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
    break
  fi
  if [ $i -eq 60 ]; then
    echo "❌ app-server did not become healthy" >&2
    exit 1
  fi
  sleep 0.5
done

if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  echo "🚀 Starting voice-gateway on ws://127.0.0.1:${GATEWAY_PORT}/v1/voice"
  go run ./cmd/voice-gateway &
  GATEWAY_PID=$!
  
  # Wait for voice-gateway
  for i in $(seq 1 60); do
    if ! kill -0 "$GATEWAY_PID" >/dev/null 2>&1; then
      echo "❌ voice-gateway exited before becoming healthy" >&2
      exit 1
    fi
    if curl -sf "http://127.0.0.1:${GATEWAY_PORT}/healthz" >/dev/null 2>&1; then
      break
    fi
    if [ $i -eq 60 ]; then
      echo "❌ voice-gateway did not become healthy" >&2
      exit 1
    fi
    sleep 0.5
  done
fi

echo ""
echo "✅ Healthy. Smoke-testing guest auth..."
curl -sS -H 'Content-Type: application/json' \
  -d '{"device_id":"local-dev-device"}' \
  "http://127.0.0.1:${PORT}/api/v1/auth/guest"
echo ""
echo ""

if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  echo "✅ app-server (pid ${SERVER_PID}) and voice-gateway (pid ${GATEWAY_PID}) are running."
  echo "   Ctrl-C to stop."
  wait "$SERVER_PID" "$GATEWAY_PID"
else
  echo "✅ app-server is running (pid ${SERVER_PID}). Ctrl-C to stop."
  wait "$SERVER_PID"
fi
