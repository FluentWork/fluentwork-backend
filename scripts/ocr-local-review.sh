#!/usr/bin/env bash
# Local OpenCodeReview gate (pre-commit / manual).
# Policy: fluentwork-meta/agents/shared/review-gate.md
#
# Runs `ocr review --format json` against the current workspace diff, then
# fails closed on any critical/high finding via ocr-fail-on-high.sh.
#
# Bypass (emergency only; record reason in the commit/PR body):
#   SKIP_OCR=1 git commit ...
#
# Usage:
#   ./scripts/ocr-local-review.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATE="${SCRIPT_DIR}/ocr-fail-on-high.sh"

if [[ "${SKIP_OCR:-}" == "1" ]]; then
  echo "OCR local review skipped (SKIP_OCR=1)."
  exit 0
fi

if ! command -v ocr >/dev/null 2>&1; then
  echo "error: ocr CLI not found." >&2
  echo "Install OpenCodeReview CLI, configure LLM via \`ocr config\`, then retry." >&2
  echo "Emergency bypass: SKIP_OCR=1 (must justify in commit/PR body)." >&2
  exit 1
fi

if [[ ! -x "${GATE}" ]]; then
  echo "error: gate script missing or not executable: ${GATE}" >&2
  exit 1
fi

# No staged changes => nothing to gate for a normal commit hook.
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
# Prefer agent audience so progress noise stays off stdout when format=json.
if ! ocr review --format json --audience agent >"${RESULT_PATH}"; then
  echo "error: ocr review failed (LLM/config/network). Fix setup or use SKIP_OCR=1 with justification." >&2
  exit 1
fi

bash "${GATE}" "${RESULT_PATH}"
