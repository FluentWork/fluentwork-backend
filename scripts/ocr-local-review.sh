#!/usr/bin/env bash
# Local review helper (pre-commit / manual).
# Policy: fluentwork-meta/agents/shared/review-gate.md
#
# OpenCodeReview pre-commit gate is PAUSED. Use gstack /review before
# opening or merging PRs. OCR scripts remain for optional manual use.
#
# Usage:
#   ./scripts/ocr-local-review.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ "${FORCE_OCR:-}" == "1" ]]; then
  echo "FORCE_OCR=1: running paused OpenCodeReview path..."
else
  echo "OpenCodeReview local gate is PAUSED."
  echo "Use gstack /review (Cursor skill) against the PR/base diff before merge."
  echo "Optional manual OCR: FORCE_OCR=1 ./scripts/ocr-local-review.sh"
  exit 0
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATE="${SCRIPT_DIR}/ocr-fail-on-high.sh"

if ! command -v ocr >/dev/null 2>&1; then
  echo "error: ocr CLI not found (FORCE_OCR=1)." >&2
  exit 1
fi

if [[ ! -x "${GATE}" ]]; then
  echo "error: gate script missing or not executable: ${GATE}" >&2
  exit 1
fi

if git diff --cached --quiet; then
  echo "OCR local review: no staged changes; skipping."
  exit 0
fi

RESULT_PATH="${OCR_RESULT_FILE:-}"
CLEANUP_RESULT=0
if [[ -z "${RESULT_PATH}" ]]; then
  RESULT_PATH="$(mktemp "${TMPDIR:-/tmp}/ocr-local-review.XXXXXX.json")"
  CLEANUP_RESULT=1
fi

cleanup() {
  if [[ "${CLEANUP_RESULT}" -eq 1 && -f "${RESULT_PATH}" ]]; then
    rm -f "${RESULT_PATH}"
  fi
}
trap cleanup EXIT

echo "Running local OpenCodeReview (workspace diff; gate=critical/high)..."
if ! ocr review --format json --audience agent >"${RESULT_PATH}"; then
  echo "error: ocr review failed (LLM/config/network)." >&2
  exit 1
fi

bash "${GATE}" "${RESULT_PATH}"
