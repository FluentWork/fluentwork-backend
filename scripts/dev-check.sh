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

# -race 不是「更严格的模式」，它是这七步里唯一一件**会一次次去撞**竞态的东西。
# 数据竞争按调度发生、不按命令发生：去掉这一个词，整整一类缺陷对整条门禁完全
# 不可见，而门禁照样打印 All checks passed.（BE-S0-7 潜伏到 2026-09-27 就是这个
# 原因；BE-S0-8 也是只有 -race 才稳定复现。）`scripts/check-gate.sh` 钉着这一行。
echo "== go test -race"
go test -race ./...

echo "== go build"
go build ./...

echo "== 环境加载器"
"$ROOT/scripts/check-env-loaders.sh"

echo "== 开发服务监管"
"$ROOT/scripts/check-dev-service.sh"

echo "== 门禁自身形状"
"$ROOT/scripts/check-gate.sh"

echo "All checks passed."
