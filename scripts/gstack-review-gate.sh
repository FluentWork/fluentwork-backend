#!/usr/bin/env bash
# Pre-commit attestation gate for gstack /review.
# Policy: fluentwork-meta/agents/shared/review-gate.md
#
# The gstack /review skill is interactive (Cursor/Claude) and cannot be
# invoked from bash. This hook requires an explicit attestation that the
# author (or agent) already ran gstack /review on the staged work.
#
# Usage:
#   GSTACK_REVIEWED=1 git commit ...
# Emergency:
#   SKIP_GSTACK_REVIEW=1 git commit ...   # justify in commit/PR body
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if git diff --cached --quiet; then
  echo "gstack review gate: no staged changes; skipping."
  exit 0
fi

if [[ "${SKIP_GSTACK_REVIEW:-}" == "1" ]]; then
  echo "warning: SKIP_GSTACK_REVIEW=1 — bypassing gstack /review pre-commit gate" >&2
  exit 0
fi

if [[ "${GSTACK_REVIEWED:-}" == "1" ]]; then
  echo "gstack /review gate: GSTACK_REVIEWED=1 acknowledged"
  exit 0
fi

cat <<'EOF' >&2
error: commit blocked by gstack /review pre-commit gate.

Bash cannot run the Cursor skill. Before committing:
  1. Run gstack /review (Cursor skill /review) on this branch/diff
  2. Fix must-fix findings (see fluentwork-meta/agents/shared/review-gate.md)
  3. Retry with attestation:
       GSTACK_REVIEWED=1 git commit ...

Emergency bypass (justify in commit/PR body):
       SKIP_GSTACK_REVIEW=1 git commit ...
EOF
exit 1
