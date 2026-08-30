#!/usr/bin/env bash
# Verify Doubao Speech API-Key auth (new console: X-Api-Key only; no AppID/Token).
# Does a lightweight TTS HTTP probe — not a full realtime WSS session (that remains B14).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -f "$ROOT/.env.volc.local" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$ROOT/.env.volc.local"
  set +a
fi

# Prefer explicit key; else Dev key for local smoke.
if [[ -z "${VOLC_SPEECH_API_KEY:-}" && -n "${VOLC_SPEECH_API_KEY_DEV:-}" ]]; then
  VOLC_SPEECH_API_KEY="${VOLC_SPEECH_API_KEY_DEV}"
fi

if [[ -z "${VOLC_SPEECH_API_KEY:-}" ]]; then
  echo "VOLC_SPEECH_API_KEY / VOLC_SPEECH_API_KEY_DEV is empty." >&2
  echo "Create $ROOT/.env.volc.local from configs/volc.env.example and fill the speech Key." >&2
  exit 1
fi

if [[ -n "${VOLC_SPEECH_APP_ID:-}" || -n "${VOLC_SPEECH_ACCESS_TOKEN:-}" ]]; then
  echo "[WARN] VOLC_SPEECH_APP_ID / ACCESS_TOKEN are set but ignored (project口径: API Key only)."
fi

# TTS 探针仍可能需要产品级 SKU（与控制台「实例名」无关）；可用 env 覆盖。
RESOURCE_TTS="${VOLC_SPEECH_RESOURCE_TTS:-seed-tts-2.0}"
REQ_ID="$(uuidgen | tr '[:upper:]' '[:lower:]')"
URL="${VOLC_SPEECH_TTS_URL:-https://openspeech.bytedance.com/api/v3/tts/unidirectional}"

echo "=== Doubao Speech API-Key smoke ==="
echo "auth=X-Api-Key (new console; no AppID/Token)"
echo "tts_probe_resource=${RESOURCE_TTS} url=${URL}"
echo "key_prefix=$(printf '%s' "$VOLC_SPEECH_API_KEY" | cut -c1-12)..."

body="$(jq -nc '{
  user: {uid: "fw-smoke"},
  req_params: {
    text: "FluentWork speech key smoke.",
    speaker: "zh_female_shuangkuaisisi_moon_bigtts",
    audio_params: {format: "mp3", sample_rate: 24000}
  }
}')"

tmp="$(mktemp)"
code="$(
  curl -sS -o "$tmp" -w "%{http_code}" \
    -X POST "$URL" \
    -H "Content-Type: application/json" \
    -H "X-Api-Key: ${VOLC_SPEECH_API_KEY}" \
    -H "X-Api-Resource-Id: ${RESOURCE_TTS}" \
    -H "X-Api-Request-Id: ${REQ_ID}" \
    -d "$body"
)" || true

# TTS may return 200 with binary/json chunks, or 4xx with JSON error.
# Treat 401/403 as hard fail; other non-2xx print body for diagnosis.
if [[ "$code" == "401" || "$code" == "403" ]]; then
  echo "[FAIL] speech auth HTTP $code"
  head -c 600 "$tmp"; echo
  rm -f "$tmp"
  exit 1
fi

if [[ "$code" != 2* ]]; then
  # Some deployments return application/json error with 200+code field; surface raw.
  echo "[FAIL] speech probe HTTP $code"
  head -c 600 "$tmp"; echo
  rm -f "$tmp"
  exit 1
fi

bytes="$(wc -c < "$tmp" | tr -d ' ')"
echo "[OK]   speech API Key accepted (HTTP $code, body_bytes=${bytes})"
rm -f "$tmp"

echo
echo "Note: proves speech API-Key auth (TTS HTTP probe)."
echo "Realtime end-to-end WSS uses X-Api-Key only (no console instance name required)."
echo "B14 still needs realtime WSS (ASR text + mid-session inject)."
echo "=== Speech API-Key smoke PASS ==="
