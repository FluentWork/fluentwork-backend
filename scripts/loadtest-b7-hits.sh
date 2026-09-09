#!/usr/bin/env bash
# B19 / T-HIT-4: 100 QPS × 30s against POST /internal/v1/voicegateway/hits.
# Asserts hey/vegeta p99 < 50ms.
#
# Usage:
#   APP_SERVER_URL=http://127.0.0.1:8080 INTERNAL_API_TOKEN=... \
#     ./scripts/loadtest-b7-hits.sh
#
# Optional:
#   USER_ID SESSION_ID TURN_ID BLOCK_ID DURATION_SEC QPS P99_MS
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE_URL="${APP_SERVER_URL:-http://127.0.0.1:8080}"
TOKEN="${INTERNAL_API_TOKEN:-${INTERNAL_TOKEN:-fluentwork-dev-internal-token-change-me!!}}"
USER_ID="${USER_ID:-user-1}"
SESSION_ID="${SESSION_ID:-session-loadtest}"
TURN_ID="${TURN_ID:-turn-loadtest}"
BLOCK_ID="${BLOCK_ID:-block-1}"
DURATION_SEC="${DURATION_SEC:-30}"
QPS="${QPS:-100}"
P99_MS="${P99_MS:-50}"

BODY="$(mktemp)"
trap 'rm -f "$BODY"' EXIT
printf '{"user_id":"%s","session_id":"%s","turn_id":"%s","hits":[{"block_id":"%s","detected_at_ms":1000}]}' \
  "$USER_ID" "$SESSION_ID" "$TURN_ID" "$BLOCK_ID" >"$BODY"

URL="${BASE_URL%/}/internal/v1/voicegateway/hits"

echo "=== B7 hits loadtest ==="
echo "url=${URL} qps=${QPS} duration=${DURATION_SEC}s p99_budget=${P99_MS}ms"

p99_seconds=""

if command -v hey >/dev/null 2>&1; then
  out="$(hey -z "${DURATION_SEC}s" -q "$QPS" -m POST \
    -H "Content-Type: application/json" \
    -H "X-Internal-Token: ${TOKEN}" \
    -D "$BODY" \
    "$URL")"
  echo "$out"
  p99_seconds="$(echo "$out" | awk '/99% in/{print $(NF-1); exit}')"
elif command -v vegeta >/dev/null 2>&1; then
  targets="$(mktemp)"
  trap 'rm -f "$BODY" "$targets"' EXIT
  printf 'POST %s\nContent-Type: application/json\nX-Internal-Token: %s\n@%s\n' \
    "$URL" "$TOKEN" "$BODY" >"$targets"
  out="$(vegeta attack -duration="${DURATION_SEC}s" -rate="${QPS}/s" -targets="$targets" | vegeta report)"
  echo "$out"
  p99_seconds="$(echo "$out" | awk -F'[=, ]+' '/99th/{print $2; exit}')"
else
  echo "install hey (github.com/rakyll/hey) or vegeta to run the 30s loadtest." >&2
  echo "Unit stand-in: go test ./internal/corpus/ -run TestHitsHTTP_ConcurrentPostP99" >&2
  exit 1
fi

if [[ -z "$p99_seconds" ]]; then
  echo "could not parse p99 from loadtest output" >&2
  exit 1
fi

# hey prints seconds (e.g. 0.0045); vegeta may print 4.5ms or 0.0045s.
set +e
p99_ms="$(python3 - "$p99_seconds" "$P99_MS" <<'PY'
import sys
raw, budget = sys.argv[1], float(sys.argv[2])
s = raw.strip().lower()
if s.endswith("ms"):
    ms = float(s[:-2])
elif s.endswith("us"):
    ms = float(s[:-2]) / 1000.0
elif s.endswith("s"):
    ms = float(s[:-1]) * 1000.0
else:
    ms = float(s) * 1000.0
print(f"{ms:.3f}")
if ms > budget:
    sys.exit(1)
PY
)"
status=$?
set -e
echo "p99=${p99_ms}ms budget=${P99_MS}ms"
if [[ "$status" -ne 0 ]]; then
  echo "FAIL: p99 ${p99_ms}ms exceeds ${P99_MS}ms" >&2
  exit 1
fi
echo "PASS"
