#!/usr/bin/env bash
# Print whether Doubao Speech credentials look present; optional lightweight HTTP probe.
# Full end-to-end voice still needs WSS (B14); this only checks credential shape / auth surface.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "=== Doubao Speech credential check ==="

have_classic=0
have_api_key=0
if [[ -n "${VOLC_SPEECH_APP_ID:-}" && -n "${VOLC_SPEECH_ACCESS_TOKEN:-}" ]]; then
  have_classic=1
  echo "[OK] VOLC_SPEECH_APP_ID + VOLC_SPEECH_ACCESS_TOKEN are set"
else
  echo "[MISS] classic AppID/Access Token not both set"
fi

if [[ -n "${VOLC_SPEECH_API_KEY:-}" ]]; then
  have_api_key=1
  echo "[OK] VOLC_SPEECH_API_KEY is set"
else
  echo "[MISS] VOLC_SPEECH_API_KEY not set"
fi

if [[ "$have_classic" -eq 0 && "$have_api_key" -eq 0 ]]; then
  echo "No speech credentials exported. Fill .env.volc.local from configs/volc.env.example" >&2
  exit 1
fi

echo
echo "Next for B14 (cannot be replaced by this script):"
echo "  1. Confirm 豆包语音 → 应用管理 has AppID (even if you also have API Key)"
echo "  2. Open official realtime-voice API doc and run one WSS session"
echo "  3. Verify ASR text callback + mid-session inject (doc 50 V1/V2)"
echo "=== Speech credential check done ==="
