#!/usr/bin/env bash
# Fail CI when OpenCodeReview reports any critical/high finding.
# Policy: fluentwork-meta/agents/shared/review-gate.md
#
# Usage:
#   ./scripts/ocr-fail-on-high.sh [/tmp/ocr-result.json]
set -euo pipefail

RESULT_PATH="${1:-/tmp/ocr-result.json}"

if [[ ! -f "${RESULT_PATH}" ]]; then
  echo "error: OCR result not found at ${RESULT_PATH}"
  echo "OpenCodeReview ran but produced no result file; failing closed."
  exit 1
fi

python3 - "${RESULT_PATH}" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
raw = json.loads(path.read_text(encoding="utf-8"))

if isinstance(raw, list):
    comments = raw
elif isinstance(raw, dict):
    comments = (
        raw.get("comments")
        or raw.get("findings")
        or raw.get("results")
        or raw.get("items")
        or []
    )
    if not isinstance(comments, list):
        print(f"error: unexpected OCR result shape in {path}", file=sys.stderr)
        sys.exit(1)
else:
    print(f"error: unexpected OCR result type in {path}: {type(raw)!r}", file=sys.stderr)
    sys.exit(1)

blocking = []
for item in comments:
    if not isinstance(item, dict):
        continue
    severity = str(item.get("severity") or "").strip().lower()
    if severity in {"critical", "high"}:
        blocking.append(item)

if not blocking:
    print(f"OCR gate passed: no critical/high findings in {path} ({len(comments)} total).")
    sys.exit(0)

print(f"OCR gate failed: {len(blocking)} critical/high finding(s). Merge is blocked.")
print("Fix these before merge (medium/low may remain as follow-ups):")
for item in blocking:
    severity = item.get("severity", "?")
    category = item.get("category", "?")
    file_path = item.get("path", "?")
    start = item.get("start_line", "?")
    content = str(item.get("content") or "").strip().splitlines()
    summary = content[0] if content else "(no content)"
    print(f"- [{severity}/{category}] {file_path}:{start} — {summary}")

sys.exit(1)
PY
