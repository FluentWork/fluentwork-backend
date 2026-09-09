#!/usr/bin/env bash
# Probe the four D-2 TTS speakers against Volc unidirectional TTS.
# Usage: ./scripts/check-volc-voice-resource-ids.sh
# Requires VOLC_SPEECH_API_KEY (or _DEV) in the environment or .env.volc.local.
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

if [[ -z "${VOLC_SPEECH_API_KEY:-}" ]]; then
  echo "VOLC_SPEECH_API_KEY / VOLC_SPEECH_API_KEY_DEV is empty." >&2
  echo "Fill $ROOT/.env.volc.local from configs/volc.env.example (C-2)." >&2
  exit 1
fi

RESOURCE_TTS="${VOLC_SPEECH_RESOURCE_TTS:-seed-tts-2.0}"
URL="${VOLC_SPEECH_TTS_URL:-https://openspeech.bytedance.com/api/v3/tts/unidirectional}"

voices=(
  "zh_male_tech_01"
  "en_female_professional"
  "en_male_narrator"
  "en_female_clear"
)

fail=0
echo "=== Volc D-2 voice resource probe ==="
echo "resource=${RESOURCE_TTS} url=${URL}"

for speaker in "${voices[@]}"; do
  req_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  body="$(jq -nc --arg speaker "$speaker" '{
    user: {uid: "fw-voice-probe"},
    req_params: {
      text: "FluentWork voice probe.",
      speaker: $speaker,
      audio_params: {format: "ogg_opus", sample_rate: 24000}
    }
  }')"
  tmp="$(mktemp)"
  code="$(
    curl -sS -o "$tmp" -w "%{http_code}" \
      -X POST "$URL" \
      -H "Content-Type: application/json" \
      -H "X-Api-Key: ${VOLC_SPEECH_API_KEY}" \
      -H "X-Api-Resource-Id: ${RESOURCE_TTS}" \
      -H "X-Api-Request-Id: ${req_id}" \
      -d "$body"
  )" || true
  if [[ "$code" == 2* ]]; then
    echo "[PASS] ${speaker} HTTP ${code}"
  else
    echo "[FAIL] ${speaker} HTTP ${code}"
    head -c 400 "$tmp"; echo
    fail=1
  fi
  rm -f "$tmp"
done

if [[ "$fail" -ne 0 ]]; then
  echo "=== voice probe FAIL (update VoiceID after C-2 lookup) ===" >&2
  exit 1
fi
echo "=== voice probe PASS ==="
