#!/usr/bin/env bash
# B14 integration: server-side ASR fallback drives badge detection.
# Boots an httptest-backed voicegateway.Handler with a stub provider that
# returns ServerASRText and ClientASRTranscription on every control frame.
# The client sends user.speech.end with EMPTY text; the handler must fall
# back to ServerASRText for badge detection and relay the transcript back
# to the client.
#
# Usage:
#   ./scripts/integration-voice-gateway-server-asr.sh
#
# Optional env overrides:
#   PROVIDER_ASR (default: "server-asr-text") — the transcript the provider emits
#   TIMEOUT_MS   (default: 5000)               — overall timeout
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PROVIDER_ASR="${PROVIDER_ASR:-server-asr-text}"
TIMEOUT_MS="${TIMEOUT_MS:-5000}"

echo "=== integration: voice-gateway server-asr ==="
echo "provider_server_asr=$PROVIDER_ASR timeout_ms=$TIMEOUT_MS"

go run ./cmd/integration-voice-gateway \
  --scenario=server-asr \
  --turn-text="" \
  --provider-server-asr-text="$PROVIDER_ASR" \
  --timeout-ms="$TIMEOUT_MS"
