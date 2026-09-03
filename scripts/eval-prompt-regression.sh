#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
go test ./internal/eval/...
# Generator self-check: every synth sample must already satisfy the validator
# before we run the regression over the persisted JSON.
go test ./cmd/gen-eval-samples/...
go run ./cmd/eval-prompt-regression "$@"
