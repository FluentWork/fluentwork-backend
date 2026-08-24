#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

usage() {
  cat <<'EOF'
Export a local OpenCodeReview session into the repo so findings are not a black box.

Usage:
  ./scripts/ocr-export-review.sh [session-id] [label]

Examples:
  ./scripts/ocr-export-review.sh
  ./scripts/ocr-export-review.sh 17393ed0-96b9-4702-8f52-d3cd086805ab b1-guest-auth

Outputs under .opencodereview/reviews/:
  <stamp>-<label>-<session8>.meta.txt   session metadata
  <stamp>-<label>-<session8>.md         human-readable comments
  <stamp>-<label>-<session8>.json       machine-readable comments
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if ! command -v ocr >/dev/null 2>&1; then
  echo "ocr CLI not found. Install OpenCodeReview CLI first." >&2
  exit 1
fi

SESSION_ID="${1:-}"
LABEL="${2:-review}"

if [[ -z "$SESSION_ID" ]]; then
  SESSION_ID="$(ocr session list --color never | awk 'NR==2 { print $1 }')"
fi

if [[ -z "$SESSION_ID" || "$SESSION_ID" == "SESSION" ]]; then
  echo "No OpenCodeReview session found for this repo." >&2
  exit 1
fi

OUT_DIR="$ROOT/.opencodereview/reviews"
mkdir -p "$OUT_DIR"

STAMP="$(date +%Y%m%d-%H%M%S)"
SHORT="${SESSION_ID:0:8}"
BASE="$OUT_DIR/${STAMP}-${LABEL}-${SHORT}"

ocr session show "$SESSION_ID" --color never >"${BASE}.meta.txt"
ocr session comments "$SESSION_ID" --color never >"${BASE}.md"
ocr session comments "$SESSION_ID" --json --color never >"${BASE}.json"

# Stable "latest" pointers for quick open in the IDE.
cp "${BASE}.meta.txt" "$OUT_DIR/latest.meta.txt"
cp "${BASE}.md" "$OUT_DIR/latest.md"
cp "${BASE}.json" "$OUT_DIR/latest.json"

echo "Exported OpenCodeReview session $SESSION_ID"
echo "  ${BASE}.meta.txt"
echo "  ${BASE}.md"
echo "  ${BASE}.json"
echo "  ${OUT_DIR}/latest.md"
