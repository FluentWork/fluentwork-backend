#!/usr/bin/env bash
# First-wave live smoke: guest -> session -> session.end -> worker -> review ready.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WITH_MYSQL=0
PORT_HINT=""

usage() {
  cat <<'EOF'
Run the first-wave review-ready live smoke.

Usage:
  ./scripts/smoke-review-ready.sh [--mysql]

Default (no Docker):
  Starts an in-process harness that co-locates HTTP handlers and the review
  worker against the memory store, then proves:
    guest auth -> create session -> activate -> session.end -> review ready

With --mysql (requires Docker):
  Starts MySQL via docker compose, applies migrations, sets MYSQL_DSN, then
  runs the same HTTP smoke against a shared MySQL-backed store.

Evidence printed on success includes session_id, review status, generator,
and key review fields. Failure exits non-zero.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mysql)
      WITH_MYSQL=1
      shift
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
  echo "Go is required." >&2
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

# Optional Volcano/Ark credentials for B8 live generator path.
if [[ -f "$ROOT/.env.volc.local" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$ROOT/.env.volc.local"
  set +a
fi
if [[ -z "${ARK_API_KEY:-}" && -n "${ARK_API_KEY_DEV:-}" ]]; then
  export ARK_API_KEY="${ARK_API_KEY_DEV}"
fi

export APP_ENV="${APP_ENV:-development}"
export AUTH_JWT_SECRET="${AUTH_JWT_SECRET:-fluentwork-dev-jwt-secret-change-me!!}"
export INTERNAL_API_TOKEN="${INTERNAL_API_TOKEN:-fluentwork-dev-internal-token-change-me!!}"
export VOICE_GATEWAY_WSS_URL="${VOICE_GATEWAY_WSS_URL:-ws://127.0.0.1:8081/v1/voice}"

COMPOSE_FILE="$ROOT/deploy/docker-compose.yml"

cleanup_mysql=0
cleanup() {
  if [[ "$cleanup_mysql" -eq 1 ]]; then
    docker compose -f "$COMPOSE_FILE" down >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

if [[ "$WITH_MYSQL" -eq 1 ]]; then
  if ! command -v docker >/dev/null 2>&1; then
    echo "Docker is required for --mysql." >&2
    exit 1
  fi
  if ! docker info >/dev/null 2>&1; then
    echo "Docker daemon is not available. Re-run without --mysql, or start Docker." >&2
    exit 1
  fi
  cleanup_mysql=1
  docker compose -f "$COMPOSE_FILE" up -d --wait mysql
  for file in "$ROOT"/migrations/*.sql; do
    echo "Applying $(basename "$file")"
    docker compose -f "$COMPOSE_FILE" exec -T mysql \
      mysql -ufw -pfw fluentwork < "$file"
  done
  export MYSQL_DSN="${MYSQL_DSN:-fw:fw@tcp(127.0.0.1:3306)/fluentwork?parseTime=true&charset=utf8mb4&loc=UTC}"
else
  unset MYSQL_DSN || true
fi

echo "Running wave-1 review-ready smoke..."
echo "  look here on failure: app-server HTTP path / worker ProcessNextJob / store (memory|mysql)"
if [[ -n "${PORT_HINT}" ]]; then
  echo "  port hint: ${PORT_HINT}"
fi

go run ./cmd/smoke-review-ready
