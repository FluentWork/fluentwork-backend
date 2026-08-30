#!/usr/bin/env bash
# Smoke-test Volcano Ark text endpoints (FluentWork fw-* ep list).
# Usage:
#   cp configs/volc.env.example .env.volc.local   # fill ARK_API_KEY(_DEV)
#   set -a && source .env.volc.local && set +a
#   ./scripts/smoke-volc-ark.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Prefer explicit ARK_API_KEY; else Dev key for local smoke.
if [[ -z "${ARK_API_KEY:-}" && -n "${ARK_API_KEY_DEV:-}" ]]; then
  ARK_API_KEY="${ARK_API_KEY_DEV}"
fi

if [[ -z "${ARK_API_KEY:-}" ]]; then
  echo "ARK_API_KEY / ARK_API_KEY_DEV is empty." >&2
  echo "Export it first, or: set -a; source .env.volc.local; set +a" >&2
  exit 1
fi

BASE_URL="${ARK_BASE_URL:-https://ark.cn-beijing.volces.com/api/v3}"
PROJECT_HINT="${ARK_PROJECT:-unknown}"

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
echo "=== Volcano Ark smoke ($BASE_URL) project=${PROJECT_HINT} ==="
echo "key_prefix=$(printf '%s' "$ARK_API_KEY" | cut -c1-12)..."

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
    head -c 500 "$tmp"; echo
    fail=1
  else
    snippet="$(jq -r '.choices[0].message.content // .error.message // "ok"' "$tmp" 2>/dev/null || echo ok)"
    echo "[OK]   $name ($ep) -> ${snippet//$'\n'/ }"
  fi
  rm -f "$tmp"
done

if [[ "$fail" -ne 0 ]]; then
  echo "=== Ark smoke FAILED ===" >&2
  echo "Hints:" >&2
  echo "  - 401: 用了语音 Key，或 Key 不属于该项目" >&2
  echo "  - 404/InvalidEndpoint: Endpoint 与 API Key 不在同一项目（常见：ep 在 default，Key 在 FluentWork-Dev）" >&2
  exit 1
fi

echo "=== Ark smoke PASS (6 endpoints) ==="
