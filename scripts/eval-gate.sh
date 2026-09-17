#!/usr/bin/env bash
# Core-chain quality gate: run the flow eval and compare it to the baseline.
#
# Run this before merging any change to a prompt, a model, or a scheduling
# parameter. The point is not the number itself — it is that "this prompt feels
# better" becomes a claim with a before and an after.
#
# Usage:
#   ./scripts/eval-gate.sh                     # full run against eval/moat/baseline.json
#   ./scripts/eval-gate.sh --reuse-run DIR     # re-run only the topic stage over a prior run
#   ./scripts/eval-gate.sh --write-baseline    # record the current state as the new baseline
#                                              # (only after an intentional, explained change)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -f .env.volc.local ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env.volc.local
  set +a
fi

export APP_ENV="${APP_ENV:-development}"
if [[ -z "${ARK_API_KEY:-}${ARK_API_KEY_DEV:-}" ]]; then
  echo "eval-gate: ARK_API_KEY / ARK_API_KEY_DEV missing (source .env.volc.local)" >&2
  exit 2
fi

# Large corpora go to the topic referee in one call; the shipping 30s bound is
# not enough for that prompt.
export ARK_HTTP_TIMEOUT="${ARK_HTTP_TIMEOUT:-120s}"

write_baseline=false
args=()
for arg in "$@"; do
  if [[ "$arg" == "--write-baseline" ]]; then
    write_baseline=true
  else
    args+=("$arg")
  fi
done

baseline_flag=(--baseline eval/moat/baseline.json)
if [[ "$write_baseline" == true ]]; then
  baseline_flag=(--write-baseline eval/moat/baseline.json)
  echo "eval-gate: recording a NEW baseline — this discards the previous one's numbers."
fi

go run ./cmd/eval-moat-flow \
  --dataset eval/moat/dataset.json \
  --out eval-out \
  "${baseline_flag[@]}" \
  ${args[@]+"${args[@]}"}

echo
echo "eval-gate: no metric regressed beyond its tolerance."
echo "  baseline: eval/moat/baseline.json (regenerate deliberately, not casually)"
echo "  report:   eval-out/<run>/report.md"
