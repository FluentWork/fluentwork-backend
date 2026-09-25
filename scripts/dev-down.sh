#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-8080}"
GATEWAY_PORT="${GATEWAY_PORT:-8081}"

if [[ -f "$ROOT/deploy/docker-compose.yml" ]] && command -v docker >/dev/null 2>&1; then
  docker compose -f "$ROOT/deploy/docker-compose.yml" down
fi

# 回收本仓自己的残留开发服务。
#
# 这一步以前不存在，而它正是「voice-gateway 起不来」的下半段：Ctrl-C 只停掉了
# 当时那个脚本起的东西，而 `go run` 包装进程泄漏出来的服务本体（见
# scripts/lib/dev-service.sh 头注第 1 条）会一直占着 :8081，没人收。下一次
# 启动绑不上端口，报出来却是「voice-gateway exited before becoming healthy」。
#
# 只回收**本仓自己的**进程（判据在 dev_is_our_service）；外来进程只报告不动。
source "$ROOT/scripts/lib/dev-service.sh"
dev_services_init

echo "🔍 回收本仓的开发服务残留..."
reclaimed=0
for port in "$PORT" "$GATEWAY_PORT"; do
  if [[ -n "$(dev_port_holders "$port")" ]]; then
    dev_reclaim_ours "$port" 0 || true
    reclaimed=1
  fi
done
if [[ "$reclaimed" -eq 0 ]]; then
  echo "   （${PORT} 与 ${GATEWAY_PORT} 都没有监听者）"
fi

echo ""
echo "✅ compose 里的 MySQL / Redis 已停止（如果它们由 compose 启动）。"
echo "   本仓的 app-server / voice-gateway 残留已回收。"
echo ""
echo "还没停的："
echo "  - 某个 dev-*.sh 正在前台跑的 app-server / voice-gateway —— 在它那个终端按 Ctrl-C"
echo "  - brew 起的 MySQL / Redis —— ./scripts/local-services-stop.sh"
echo ""
echo "看一眼现状：./scripts/dev-status.sh"
