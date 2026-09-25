#!/usr/bin/env bash
#
# 一眼看到本地开发的全貌。
#
# 真源：docs/109_开发启动脚本监管_实现说明.md
#
# 「某个服务没起来」这件事之所以难查，一半原因是没有一个地方能看到全貌：
# 端口上有没有东西、那个东西是不是我们的、日志在哪、review worker 开没开、
# 徽章语料播没播 —— 以前这些信息分散在四个脚本和一次 `ps aux` 里。
#
# 退出码：0 = 期望的服务都在；1 = 有服务没在跑。便于 `dev-status.sh || ...`。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-8080}"
GATEWAY_PORT="${GATEWAY_PORT:-8081}"

source "$ROOT/scripts/lib/dev-service.sh"
dev_services_init

echo "📊 FluentWork 本地开发状态"
echo "=========================="
echo ""

echo "依赖（只有 --local-mysql / --mysql 才需要；默认的内存模式不需要）"

# 判据用**端口有没有人听**，不用 mysqladmin / redis-cli。
# 原因（实测）：本机 PATH 里根本没有 mysqladmin 与 redis-cli，
# 所以旧写法对「MySQL 装没装、跑没跑」永远只能给出 ❌ ——
# 一个从不取真值的判据。lsof 是本文件其它部分本来就依赖的、一定在的东西。
dep_line() {
  local label="$1" formula="$2" port="$3" holder
  holder="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | head -1 || true)"
  if [ -n "$holder" ]; then
    printf '  ✅ %-6s 127.0.0.1:%s  pid %s\n' "$label" "$port" "$holder"
    return 0
  fi
  if ! command -v brew >/dev/null 2>&1; then
    printf '  ⚠️  %-6s 检测不可用：PATH 里没有 brew\n' "$label"
    return 0
  fi
  if brew list --formula 2>/dev/null | grep -qx "$formula"; then
    printf '  ❌ %-6s 已安装但没在跑 —— brew services start %s\n' "$label" "$formula"
  else
    printf '  ➖ %-6s 未安装（要跑 --local-mysql 才需要）\n' "$label"
  fi
}
dep_line MySQL mysql 3306
dep_line Redis redis 6379
echo ""

echo "服务"
down=0
# 顺序固定：app-server 是别的服务的依赖，先看它。
for pair in "app-server:${PORT}" "voice-gateway:${GATEWAY_PORT}"; do
  name="${pair%%:*}"
  port="${pair##*:}"
  if dev_status_line "$name" "$port"; then
    :
  else
    down=$(( down + 1 ))
    log="$DEV_LOG_DIR/${name}.log"
    if [ -f "$log" ]; then
      echo "     日志：${log}（$(wc -l < "$log" | tr -d ' ') 行）"
    else
      echo "     日志：${log} 不存在 —— 说明本轮不是由 dev-*.sh 起的"
    fi
  fi
done
echo ""

echo "日志里的关键行"
showed=0
for name in app-server voice-gateway; do
  log="$DEV_LOG_DIR/${name}.log"
  [ -f "$log" ] || continue
  showed=1
  # 只挑「决定了某个能力开没开」的行，不刷整份日志。
  hit="$(grep -E 'in-process review worker|badge emitter wired|badge emitter disabled|corpus-backed' "$log" 2>/dev/null | tail -3 || true)"
  if [ -n "$hit" ]; then
    while IFS= read -r line; do
      printf '  %-14s %s\n' "${name}:" "$line"
    done <<< "$hit"
  else
    printf '  %-14s （没有关于 worker / badge 的行）\n' "${name}:"
  fi
done
if [ "$showed" -eq 0 ]; then
  echo "  （${DEV_LOG_DIR} 下没有日志 —— 说明本轮不是由 dev-*.sh 起的）"
fi
echo ""

echo "徽章语料"
echo "  网关只为「能在语料里找到的短语」发 feedback.badge。语料由 app-server 提供，"
echo "  由 dev-*.sh 在启动时播种（--no-seed 可跳过）。徽章打不出来时先确认语料："
echo "    ${DEV_BIN_DIR}/corpus-seed -base-url http://127.0.0.1:${PORT} -device-id corpus-seed-dev-device"
echo ""

if [ "$down" -gt 0 ]; then
  echo "✘ 有 ${down} 个服务没有在跑。"
  echo "  重新起：./scripts/dev-up.sh     收尾：./scripts/dev-down.sh"
  exit 1
fi

echo "✅ 期望的服务都在跑。"
