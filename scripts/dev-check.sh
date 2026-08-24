#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export PATH="$(go env GOPATH)/bin:$PATH"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing $1; install with: go install $2" >&2
    exit 1
  fi
}

need gofumpt mvdan.cc/gofumpt@latest
need goimports golang.org/x/tools/cmd/goimports@latest
need golangci-lint github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

echo "== gofumpt"
bad="$(gofumpt -l . || true)"
if [[ -n "$bad" ]]; then
  echo "$bad"
  exit 1
fi

echo "== goimports"
bad="$(goimports -l . || true)"
if [[ -n "$bad" ]]; then
  echo "$bad"
  exit 1
fi

echo "== golangci-lint"
golangci-lint run ./...

echo "== go test"
go test ./...

echo "== go build"
go build ./...

echo "All checks passed."
