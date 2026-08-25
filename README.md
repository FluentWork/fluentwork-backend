# FluentWork Backend

`fluentwork-backend` is the Go service repository for FluentWork.

## Quick start

Requires Go 1.26+. No Docker is required for the default in-memory store.

```bash
./scripts/dev-up.sh
```

This starts `app-server` on `http://127.0.0.1:8080`, waits for `/healthz`, and smoke-tests `POST /api/v1/auth/guest`. Press Ctrl-C to stop.

Optional MySQL 8 (Docker daemon required):

```bash
./scripts/dev-up.sh --mysql
```

Local checks:

```bash
./scripts/dev-check.sh
```

Full local run details: `docs/01_本地启动.md`  
First-wave scope: `docs/00_开发入口与第一波范围.md`

## Current surface

- `GET /healthz`, `GET /readyz`
- `POST /api/v1/auth/guest`
- `POST /api/v1/account/merge`
- error envelope `{code, message, request_id}`
- OpenAPI: `api/openapi-v1.yaml`

## Scope

This repository contains:

- application API services
- voice gateway services
- background workers
- database migrations
- shared contracts and schemas
- integration and contract tests

## Planned Structure

```text
cmd/
├── app-server/
├── voice-gateway/
└── worker/
internal/
pkg/
api/
migrations/
deploy/
scripts/
test/
.github/
```

## Engineering Baseline

- Go as the primary backend language
- clear separation between entrypoints and domain modules
- health, readiness, logging, and config from day one
- contract-first collaboration with iOS (`api/openapi-v1.yaml`, also served at `GET /openapi.yaml`)
- shared agent policy comes from `fluentwork-meta`
- external helpers such as gstack and Matt Pocock style skills are allowed, but repo rules win on conflicts

## Recommended Code Quality Stack

- formatting: `gofumpt` + `goimports`
- lint and static checks: `golangci-lint`
- minimum correctness baseline: `go test ./...`
- keep `go vet` enabled as part of the aggregated lint baseline

## CI Goals

- `gofumpt`
- `goimports`
- `golangci-lint`
- unit tests
- integration tests
- migration checks
- Docker image build
- agent entry file validation
- report-only AI review integration as a secondary review layer

## Upstream Source of Truth

Product rules, service boundaries, and milestone priorities should be aligned with `fluentwork-meta`.

## Agent Tooling

- `gstack` can be used locally for `/review` and later `/setup-deploy` or `/ship`
- local OpenCodeReview CLI (`ocr`) can be used for pre-PR review against `.opencodereview/rule.json`
- after `ocr review`, run `./scripts/ocr-export-review.sh` to save findings under `.opencodereview/reviews/` (see `latest.md`)
- Matt Pocock style skills may be used as helpers under FluentWork shared governance
- OpenCodeReview is initialized as a GitHub review workflow and should start in report-only mode
