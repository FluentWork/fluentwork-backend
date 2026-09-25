#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WITH_MYSQL=0
LOCAL_MYSQL=0
SKIP_MIGRATIONS=0
WITH_GATEWAY=1
WITH_SEED="${AUTO_CORPUS_SEED:-1}"
KILL_STALE=1
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
  ./scripts/dev-up.sh [--mysql] [--local-mysql] [--skip-migrations]
                      [--no-gateway] [--no-seed] [--no-kill-stale]
                      [--port 8080] [--gateway-port 8081] [--host IP]

Default mode uses the in-memory account/session store (no Docker required).
Pass --mysql to start MySQL 8 via Docker Compose and apply migrations.
Pass --local-mysql to use an already-running local MySQL (brew/etc.) with
  fw/fw credentials; migrations and corpus seed run automatically.
Pass --skip-migrations to reuse the schema already in MySQL. Needed on any
  run after the first: the ALTER migrations (0008 onward) are not idempotent,
  so replaying them aborts with "Duplicate column name ..." even though the
  schema is current. Only meaningful together with --mysql / --local-mysql.
Pass --no-gateway to run only app-server.
Pass --no-seed to skip seeding the badge corpus (see below).
Pass --no-kill-stale to refuse to reclaim ports held by our own leftover
  dev services instead of reclaiming them.
Pass --host IP to set the WSS URL host (default: 127.0.0.1 for simulator;
  use LAN IP like 192.168.1.100 for physical device testing).

Badge corpus: the gateway only emits feedback.badge for phrases it can find
in the corpus, and the corpus is served by app-server. This script seeds it
by default (device_id=corpus-seed-dev-device, override with SEED_DEVICE_ID),
so "speak a phrase -> get a badge" works without a second command.

Services are built to ./bin/ and run as supervised child processes; each one
logs to .dev-logs/<name>.log. ./scripts/dev-status.sh shows the whole picture.
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
    --skip-migrations)
      SKIP_MIGRATIONS=1
      shift
      ;;
    --no-gateway)
      WITH_GATEWAY=0
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

# Say so rather than let it look like it did something: the in-memory mode has
# no schema to migrate, so the flag is a no-op there.
if [[ "$SKIP_MIGRATIONS" -eq 1 && "$WITH_MYSQL" -eq 0 ]]; then
  echo "note: --skip-migrations has no effect without --mysql / --local-mysql;" >&2
  echo "      the default in-memory mode runs no migrations." >&2
fi

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required. Install Go 1.26+ and retry." >&2
  exit 1
fi

# dotenv semantics live in scripts/lib/load-env.sh; rationale in docs/106_.
source "$ROOT/scripts/lib/load-env.sh"
load_env_snapshot

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

# Migrations are apply-once: 0008 onward ALTER existing tables, so replaying
# them against a current schema aborts on "Duplicate column name ..." and takes
# the whole bring-up with it. --skip-migrations is how a restart reuses the
# schema it already has.
run_migrations() {
  if [[ "$SKIP_MIGRATIONS" -eq 1 ]]; then
    echo "Skipping migrations (--skip-migrations); using the schema already in MySQL."
    return 0
  fi
  "$@"
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
    run_migrations apply_migrations_local
    export MYSQL_DSN="${MYSQL_DSN:-fw:fw@tcp(127.0.0.1:3306)/fluentwork?parseTime=true&charset=utf8mb4&loc=UTC}"
  else
    if ! command -v docker >/dev/null 2>&1; then
      echo "Docker is required for --mysql." >&2
      exit 1
    fi
    docker compose -f "$COMPOSE_FILE" up -d --wait mysql
    run_migrations apply_migrations
    export MYSQL_DSN="${MYSQL_DSN:-fw:fw@tcp(127.0.0.1:3306)/fluentwork?parseTime=true&charset=utf8mb4&loc=UTC}"
  fi
else
  unset MYSQL_DSN || true
fi

# Every dev-up mode should produce reviews. cmd/app-server/main.go:274-280 only
# defaults the in-process review worker ON when MYSQL_DSN is empty (in-memory
# stores cannot be shared with a standalone cmd/worker). So with a shared MySQL
# store it must be asked for explicitly, or reviews silently never complete.
if [[ -n "${MYSQL_DSN:-}" ]]; then
  export APP_RUN_REVIEW_WORKER="${APP_RUN_REVIEW_WORKER:-1}"
fi

# ---------------------------------------------------------------------------
# 服务的构建、启动、监管：唯一实现在 scripts/lib/dev-service.sh
# ---------------------------------------------------------------------------
source "$ROOT/scripts/lib/dev-service.sh"
dev_services_init

trap dev_cleanup EXIT INT TERM

echo "🚀 FluentWork 本地启动（app-server :${PORT}，voice-gateway :${GATEWAY_PORT}，host ${HOST}）"
echo ""

# 1. 端口预检 —— 放在构建之前：端口冲突是最常见的「起不来」，
#    而它和「编译不过」是两回事，报错必须分开。
echo "① 端口预检"
dev_preflight_port app-server "$PORT" "$KILL_STALE" || exit 1
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  dev_preflight_port voice-gateway "$GATEWAY_PORT" "$KILL_STALE" || exit 1
fi
echo ""

# 2. 构建 —— 全部构建完再启动。构建与就绪分开计时，
#    这样「还在编译」永远不会被报成「服务起不来」。
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

# 3. 启动
echo "③ 启动"
dev_start_service app-server "$PORT" "$APP_BIN"
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  dev_start_service voice-gateway "$GATEWAY_PORT" "$GATEWAY_BIN"
fi
echo ""

# 4. 等就绪
echo "④ 等就绪"
dev_wait_healthy app-server "$PORT" "http://127.0.0.1:${PORT}/healthz" 90 || exit 1
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  dev_wait_healthy voice-gateway "$GATEWAY_PORT" "http://127.0.0.1:${GATEWAY_PORT}/healthz" 90 || exit 1
fi
echo ""

# 5. 冒烟：游客鉴权
echo "⑤ 冒烟测试（游客鉴权）"
curl -sS -H 'Content-Type: application/json' \
  -d '{"device_id":"local-dev-device"}' \
  "http://127.0.0.1:${PORT}/api/v1/auth/guest"
echo ""
echo ""

# 6. 徽章语料 —— 网关只为「能在语料里找到的短语」发 feedback.badge，
#    而语料由 app-server 提供。以前这一步只在 dev-up.sh 里有，于是走
#    dev-stack.zsh / dev-local-start.sh 的人永远打不出徽章，得自己再跑一遍
#    corpus-seed。现在两条路都做，用 --no-seed 关闭。
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

# 7. 汇总
echo "⑦ 就绪"
dev_print_summary
echo ""
echo "   接口：GET  http://127.0.0.1:${PORT}/healthz"
echo "         GET  http://127.0.0.1:${PORT}/readyz"
echo "         POST http://127.0.0.1:${PORT}/api/v1/auth/guest"
if [[ "$WITH_GATEWAY" -eq 1 ]]; then
  echo "   iOS：WSS 地址 ws://${HOST}:${GATEWAY_PORT}/v1/voice（由 POST /api/v1/sessions 返回）"
  echo "        Xcode scheme 里设 LOCAL_HOST=${HOST} 只影响 HTTP，不影响 WSS。"
fi
if [[ -n "${MYSQL_DSN:-}" ]]; then
  echo "   存储：MySQL（进程内 review worker 已开启：APP_RUN_REVIEW_WORKER=${APP_RUN_REVIEW_WORKER}）"
else
  echo "   存储：内存（进程内 review worker 默认开启）"
fi
echo ""
echo "   Ctrl-C 停止。"

dev_supervise
