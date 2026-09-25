#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WITH_GATEWAY=1
WITH_SERVICES=1
WITH_SEED="${AUTO_CORPUS_SEED:-1}"
KILL_STALE=1
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
  ./scripts/dev-local-start.sh [--no-gateway] [--no-services] [--no-seed]
                               [--no-kill-stale]
                               [--port 8080] [--gateway-port 8081] [--host IP]

Options:
  --no-gateway     Skip starting voice-gateway
  --no-services    Assume MySQL/Redis are already running (skip brew services start)
  --no-seed        Skip seeding the badge corpus
  --no-kill-stale  Refuse to reclaim ports held by our own leftover dev services
  --port 8080      HTTP port for app-server (default: 8080)
  --gateway-port 8081  Port for voice-gateway (default: 8081)
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
    --no-seed)
      WITH_SEED=0
      shift
      ;;
    --no-kill-stale)
      KILL_STALE=0
      shift
      ;;
    --port)
      PORT="$2"
      shift 2
      ;;
    --gateway-port)
      GATEWAY_PORT="$2"
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
# 4. Load environment files
# ---------------------------------------------------------------------------
# dotenv semantics live in scripts/lib/load-env.sh; rationale in docs/106_.
source "$ROOT/scripts/lib/load-env.sh"
load_env_snapshot

# .env.dev is gitignored (.gitignore: `.env.*`), so a fresh clone never has it.
# It used to be a hard requirement here; the DSN default below replaces that.
if [[ -f "$ROOT/.env.dev" ]]; then
  echo "📋 Loading .env.dev configuration..."
  load_env_file "$ROOT/.env.dev"
else
  echo "ℹ️  .env.dev absent (it is gitignored); using built-in local defaults."
fi

if [[ -f "$ROOT/.env.volc.local" ]]; then
  echo "📋 Loading .env.volc.local (overrides .env.dev)..."
  load_env_file "$ROOT/.env.volc.local"
fi

# This script is the MySQL stack (it just started MySQL and Redis), so the DSN
# must resolve even with no .env.dev. Same default as dev-up.sh --local-mysql.
export MYSQL_DSN="${MYSQL_DSN:-fw:fw@tcp(127.0.0.1:3306)/fluentwork?parseTime=true&charset=utf8mb4&loc=UTC}"

# cmd/app-server/main.go:274-280 only defaults the in-process review worker ON
# when MYSQL_DSN is empty. This script is *always* the MySQL stack, so without
# this line the worker is off — and since no dev script starts cmd/worker
# either, reviews silently never complete. dev-up.sh already did this; the
# asymmetry meant the *recommended* entry (dev-stack.zsh -> here) was the one
# that quietly produced no reviews.
export APP_RUN_REVIEW_WORKER="${APP_RUN_REVIEW_WORKER:-1}"

# Override ports and WSS URL.
# Bind to 0.0.0.0 so iOS physical device on LAN can reach the services.
# Use $HOST in VOICE_GATEWAY_WSS_URL so the iOS app connects to the right address.
# APP_ENV must be explicit (config.Load has no implicit default); .env.dev used to
# supply it, and that file is gitignored.
export APP_ENV="${APP_ENV:-development}"
export HTTP_ADDR="0.0.0.0:${PORT}"
export VOICE_GATEWAY_HTTP_ADDR="0.0.0.0:${GATEWAY_PORT}"
export VOICE_GATEWAY_WSS_URL="ws://${HOST}:${GATEWAY_PORT}/v1/voice"
export APP_SERVER_INTERNAL_URL="http://127.0.0.1:${PORT}"

# ---------------------------------------------------------------------------
# 5. 服务的构建、启动、监管：唯一实现在 scripts/lib/dev-service.sh
# ---------------------------------------------------------------------------
source "$ROOT/scripts/lib/dev-service.sh"
dev_services_init

trap dev_cleanup EXIT INT TERM

echo "🚀 FluentWork 本地启动（app-server :${PORT}，voice-gateway :${GATEWAY_PORT}，host ${HOST}）"
echo ""

echo "① 端口预检"
dev_preflight_port app-server "$PORT" "$KILL_STALE" || exit 1
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  dev_preflight_port voice-gateway "$GATEWAY_PORT" "$KILL_STALE" || exit 1
fi
echo ""

echo "② 构建"
APP_BIN="$(dev_build app-server ./cmd/app-server)" || {
  echo "✘ 构建阶段失败，未启动任何服务。" >&2
  exit 1
}
GATEWAY_BIN=""
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  GATEWAY_BIN="$(dev_build voice-gateway ./cmd/voice-gateway)" || {
    echo "✘ 构建阶段失败，未启动任何服务。" >&2
    exit 1
  }
fi
SEED_BIN=""
if [[ "$WITH_SEED" == "1" ]]; then
  SEED_BIN="$(dev_build corpus-seed ./cmd/corpus-seed)" || {
    echo "✘ 构建阶段失败，未启动任何服务。" >&2
    exit 1
  }
fi
echo ""

echo "③ 启动"
dev_start_service app-server "$PORT" "$APP_BIN"
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  dev_start_service voice-gateway "$GATEWAY_PORT" "$GATEWAY_BIN"
fi
echo ""

echo "④ 等就绪"
dev_wait_healthy app-server "$PORT" "http://127.0.0.1:${PORT}/healthz" 90 || exit 1
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  dev_wait_healthy voice-gateway "$GATEWAY_PORT" "http://127.0.0.1:${GATEWAY_PORT}/healthz" 90 || exit 1
fi
echo ""

echo "⑤ 冒烟测试（游客鉴权）"
curl -sS -H 'Content-Type: application/json' \
  -d '{"device_id":"local-dev-device"}' \
  "http://127.0.0.1:${PORT}/api/v1/auth/guest"
echo ""
echo ""

# 徽章语料：网关只为「能在语料里找到的短语」发 feedback.badge，而语料由
# app-server 提供。这一步以前只在 dev-up.sh 里有 —— 于是走本脚本（以及
# dev-stack.zsh，也就是文档推荐的入口）的人永远打不出徽章，必须自己再跑一遍
# corpus-seed。现在两条路都做，用 --no-seed 关闭。
if [[ "$WITH_SEED" == "1" ]]; then
  SEED_ID="${SEED_DEVICE_ID:-corpus-seed-dev-device}"
  echo "⑥ 播种徽章语料（device_id=${SEED_ID}）"
  "$SEED_BIN" -base-url "http://127.0.0.1:${PORT}" -device-id "${SEED_ID}" | tail -2
  echo "   语料属于 device_id=${SEED_ID}。真机不需要播种：开发模式下 app-server"
  echo "   会在游客的第一次会话里给它发同一份起步语料（corpus.StarterProvisioner）。"
  echo "   要把语料钉在某个设备上："
  echo "     ./bin/corpus-seed -device-id <你的 device id>"
  echo ""
fi

echo "⑦ 就绪"
dev_print_summary
echo ""
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  echo "   iOS：WSS 地址 ws://${HOST}:${GATEWAY_PORT}/v1/voice（由 POST /api/v1/sessions 返回）"
  echo "        Xcode scheme 里设 LOCAL_HOST=${HOST} 只影响 HTTP，不影响 WSS。"
fi
echo "   存储：MySQL 127.0.0.1:3306 / Redis 127.0.0.1:6379"
echo "   进程内 review worker 已开启：APP_RUN_REVIEW_WORKER=${APP_RUN_REVIEW_WORKER}"
echo ""
echo "   Ctrl-C 停止。"

dev_supervise
