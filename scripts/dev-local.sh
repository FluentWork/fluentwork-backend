#!/usr/bin/env bash
# Local dev convenience: MySQL-backed stack with Volc duplex + auto corpus seed
# for the iOS device identity below.
#
# 本脚本是「真机联调」的入口，所以它必须拿到一个 LAN IP。以前这里写死的是
# HOST=192.168.2.104 —— 一个曾经在某台机器上是对的地址。换网络之后它会静默地
# 把一个**错的** WSS 地址发给手机，而症状是「HTTP 正常、WebSocket 毫秒级失败」
# （见 docs/01_本地启动.md 的「关键」一节），离原因很远。
# 现在改成自动探测 + 显式打印，探测不到就报错而不是猜。
#
# Overrides (all optional):
#   DEVICE_ID / SEED_DEVICE_ID — corpus target device
#   VOICE_GATEWAY_PROVIDER     — e.g. dev-echo / mock / volc-duplex
#   HOST                       — LAN IP advertised to iOS
#   PORT / GATEWAY_PORT
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# 探测本机 LAN IP：先问默认路由走哪个接口，再按 en0/en1/en2 兜底。
detect_lan_ip() {
  local iface ip
  iface="$(route -n get default 2>/dev/null | awk '/interface:/{print $2}')"
  if [[ -n "$iface" ]]; then
    ip="$(ipconfig getifaddr "$iface" 2>/dev/null || true)"
    if [[ -n "$ip" ]]; then
      printf '%s\n' "$ip"
      return 0
    fi
  fi
  for iface in en0 en1 en2; do
    ip="$(ipconfig getifaddr "$iface" 2>/dev/null || true)"
    if [[ -n "$ip" ]]; then
      printf '%s\n' "$ip"
      return 0
    fi
  done
  return 1
}

if [[ -z "${HOST:-}" ]]; then
  if ! HOST="$(detect_lan_ip)"; then
    echo "✘ 探测不到本机 LAN IP（默认路由接口与 en0/en1/en2 都没有地址）。" >&2
    echo "  真机联调必须有一个手机能连到的地址，请显式指定：" >&2
    echo "    HOST=\$(ipconfig getifaddr en1) ./scripts/dev-local.sh" >&2
    exit 1
  fi
  echo "📡 自动探测到 LAN IP：${HOST}（用 HOST=... 可覆盖）"
fi
export HOST

export SEED_DEVICE_ID="${SEED_DEVICE_ID:-${DEVICE_ID:-DA87E7D4-1371-4984-AD3F-D6D70B4D17D2}}"
export VOICE_GATEWAY_PROVIDER="${VOICE_GATEWAY_PROVIDER:-volc-duplex}"
# Phase 1 真机联调用 DevEcho，不要走本脚本（见 docs/41）。

exec "$ROOT/scripts/dev-up.sh" --local-mysql "$@"
