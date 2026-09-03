#!/usr/bin/env bash
# Local dev convenience: MySQL-backed stack with Volc duplex + auto corpus seed
# for the iOS device identity below.
#
# Overrides (all optional):
#   DEVICE_ID / SEED_DEVICE_ID — corpus target device
#   VOICE_GATEWAY_PROVIDER     — e.g. dev-echo / mock / volc-duplex
#   HOST                       — LAN IP advertised to iOS
#   PORT / GATEWAY_PORT
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

export SEED_DEVICE_ID="${SEED_DEVICE_ID:-${DEVICE_ID:-DA87E7D4-1371-4984-AD3F-D6D70B4D17D2}}"
export VOICE_GATEWAY_PROVIDER="${VOICE_GATEWAY_PROVIDER:-volc-duplex}"
export HOST="${HOST:-192.168.2.15}"

exec "$ROOT/scripts/dev-up.sh" --local-mysql "$@"
