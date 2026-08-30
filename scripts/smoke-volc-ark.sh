#!/usr/bin/env bash
# Smoke-test Volcano Ark text endpoints (FluentWork fw-* ep list).
# Usage:
#   cp configs/volc.env.example .env.volc.local   # fill ARK_API_KEY
#   set -a && source .env.volc.local && set +a
#   ./scripts/smoke-volc-ark.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -z "${ARK_API_KEY:-}" ]]; then
  echo "ARK_API_KEY is empty." >&2
  echo "Export it first, or: set -a; source .env.volc.local; set +a" >&2
  exit 1
fi

BASE_URL="${ARK_BASE_URL:-https://ark.cn-beijing.volces.com/api/v3}"

declare -a NAMES=(
  "fw-review-refine"
  "fw-daily-read"
  "fw-topic-card"
  "fw-hit-match"
  "fw-drill-judge"
  "fw-text-degrade"
)
declare -a EPS=(
  "${ARK_EP_REVIEW_REFINE:-}"
  "${ARK_EP_DAILY_READ:-}"
  "${ARK_EP_TOPIC_CARD:-}"
  "${ARK_EP_HIT_MATCH:-}"
  "${ARK_EP_DRILL_JUDGE:-}"
  "${ARK_EP_TEXT_DEGRADE:-}"
)

fail=0
echo "=== Volcano Ark smoke ($BASE_URL) ==="

for i in "${!NAMES[@]}"; do
  name="${NAMES[$i]}"
  ep="${EPS[$i]}"
  if [[ -z "$ep" ]]; then
    echo "[SKIP] $name (endpoint env empty)"
    continue
  fi

  body="$(jq -nc --arg model "$ep" '{
    model: $model,
    messages: [{role:"user", content:"Reply with exactly: PONG"}],
    max_tokens: 16,
    temperature: 0
  }')"

  tmp="$(mktemp)"
  code="$(
    curl -sS -o "$tmp" -w "%{http_code}" \
      "$BASE_URL/chat/completions" \
      -H "Content-Type: application/json" \
      -H "Authorization: Bearer ${ARK_API_KEY}" \
      -d "$body"
  )" || true

  if [[ "$code" != "200" ]]; then
    echo "[FAIL] $name ($ep) HTTP $code"
    head -c 400 "$tmp"; echo
    fail=1
  else
    snippet="$(jq -r '.choices[0].message.content // .error.message // "ok"' "$tmp" 2>/dev/null || echo ok)"
    echo "[OK]   $name ($ep) -> ${snippet//$'\n'/ }"
  fi
  rm -f "$tmp"
done

if [[ "$fail" -ne 0 ]]; then
  echo "=== Ark smoke FAILED ===" >&2
  echo "Hint: 401/invalid key 通常是「语音 API Key」误用于方舟；请到「火山方舟 → API Key 管理」复制 ARK_API_KEY。" >&2
  exit 1
fi

echo "=== Ark smoke PASS (6 endpoints) ==="
