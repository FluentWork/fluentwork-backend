#!/usr/bin/env bash
# Point this clone at the repo-managed hooks.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ ! -d "$ROOT/.githooks" ]]; then
  echo "error: .githooks/ missing in $ROOT" >&2
  exit 1
fi

git config core.hooksPath .githooks
chmod +x "$ROOT/.githooks/pre-commit" "$ROOT/scripts/dev-check.sh" 2>/dev/null || true
echo "Enabled core.hooksPath=.githooks for $(basename "$ROOT")"
echo "pre-commit runs ./scripts/dev-check.sh (gofumpt, goimports, golangci-lint, test, build)"
echo "Emergency bypass: SKIP_DEV_CHECK=1"
echo "pre-commit also requires gstack /review attestation:"
echo "  GSTACK_REVIEWED=1 git commit ..."
echo "Emergency bypass: SKIP_GSTACK_REVIEW=1 (justify in commit/PR body)"
