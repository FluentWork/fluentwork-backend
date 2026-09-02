#!/usr/bin/env bash
# B14 integration: client-side ASR drives badge detection.
# Boots an httptest-backed voicegateway.Handler + a real WebSocket client,
# drives user.speech.end with non-empty client text, and verifies that the
# gateway emits a feedback.badge frame on a phrase-block match.
#
# Usage:
#   ./scripts/integration-voice-gateway-client-asr.sh
#
# Optional env overrides:
#   TURN_TEXT  (default: "client-asr-text")  — the text iOS sends on user.speech.end
#   PHRASE     (default: $TURN_TEXT)          — phrase block expression to seed
#   TIMEOUT_MS (default: 5000)                — overall timeout per scenario
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TURN_TEXT="${TURN_TEXT:-client-asr-text}"
PHRASE="${PHRASE:-$TURN_TEXT}"
TIMEOUT_MS="${TIMEOUT_MS:-5000}"

echo "=== integration: voice-gateway client-asr ==="
echo "turn_text=$TURN_TEXT phrase=$PHRASE timeout_ms=$TIMEOUT_MS"

go run ./cmd/integration-voice-gateway \
  --scenario=client-asr \
  --turn-text="$TURN_TEXT" \
  --phrase="$PHRASE" \
  --timeout-ms="$TIMEOUT_MS"
