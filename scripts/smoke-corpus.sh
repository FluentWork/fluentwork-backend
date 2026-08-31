#!/usr/bin/env bash
# B10 live smoke: guest -> batch-accept -> list -> keyword -> favorite -> delete.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export APP_ENV="${APP_ENV:-development}"
export AUTH_JWT_SECRET="${AUTH_JWT_SECRET:-fluentwork-dev-jwt-secret-change-me!!}"
export INTERNAL_API_TOKEN="${INTERNAL_API_TOKEN:-fluentwork-dev-internal-token-change-me!!}"
export VOICE_GATEWAY_WSS_URL="${VOICE_GATEWAY_WSS_URL:-ws://127.0.0.1:8081/v1/voice}"

echo "Running B10 corpus smoke..."
go run ./cmd/smoke-corpus
