#!/usr/bin/env bash
# B14 D2: Volcano realtime duplex WSS smoke (API-Key only).
# Usage:
#   cp configs/volc.env.example .env.volc.local   # fill VOLC_SPEECH_API_KEY
#   ./scripts/smoke-volc-realtime.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -f "$ROOT/.env.volc.local" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$ROOT/.env.volc.local"
  set +a
fi

if [[ -z "${VOLC_SPEECH_API_KEY:-}" && -n "${VOLC_SPEECH_API_KEY_DEV:-}" ]]; then
  VOLC_SPEECH_API_KEY="${VOLC_SPEECH_API_KEY_DEV}"
fi
if [[ -z "${VOLC_POC_API_KEY:-}" && -n "${VOLC_SPEECH_API_KEY:-}" ]]; then
  VOLC_POC_API_KEY="${VOLC_SPEECH_API_KEY}"
fi

if [[ -z "${VOLC_SPEECH_API_KEY:-}${VOLC_POC_API_KEY:-}" ]]; then
  echo "VOLC_SPEECH_API_KEY / VOLC_POC_API_KEY is empty." >&2
  echo "Create $ROOT/.env.volc.local from configs/volc.env.example." >&2
  exit 1
fi

echo "=== Volcano realtime duplex smoke (B14 D2) ==="
echo "key_prefix=$(printf '%s' "${VOLC_POC_API_KEY:-$VOLC_SPEECH_API_KEY}" | cut -c1-12)..."
go run ./cmd/smoke-volc-realtime
