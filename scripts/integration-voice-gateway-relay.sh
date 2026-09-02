#!/usr/bin/env bash
# B14 integration: client.asr.transcription relay round-trip.
# Verifies that the gateway emits a client.asr.transcription WSS frame to
# the iOS client so ServerRelayASRTranscriber receives the authoritative
# provider-side transcript. Mirrors the iOS decoding contract in
# fluentwork-ios/.../WSControlFrame.swift (.clientASRTranscription + .text).
#
# Usage:
#   ./scripts/integration-voice-gateway-relay.sh
#
# Optional env overrides:
#   PROVIDER_ASR (default: "server-asr-text") — transcript the provider emits
#   TIMEOUT_MS   (default: 5000)               — overall timeout
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PROVIDER_ASR="${PROVIDER_ASR:-server-asr-text}"
TIMEOUT_MS="${TIMEOUT_MS:-5000}"

echo "=== integration: voice-gateway relay ==="
echo "provider_server_asr=$PROVIDER_ASR timeout_ms=$TIMEOUT_MS"

go run ./cmd/integration-voice-gateway \
  --scenario=relay \
  --turn-text="" \
  --provider-server-asr-text="$PROVIDER_ASR" \
  --timeout-ms="$TIMEOUT_MS"
