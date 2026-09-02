# FluentWork Backend

`fluentwork-backend` is the Go service repository for FluentWork.

## Quick start

Requires Go 1.26+. No Docker is required for the default in-memory store.

```bash
./scripts/dev-up.sh
```

This starts `app-server` on `http://127.0.0.1:8080` and `voice-gateway` on `ws://127.0.0.1:8081/v1/voice`, waits for `/healthz`, and smoke-tests `POST /api/v1/auth/guest`. Press Ctrl-C to stop. Use `--no-gateway` to run app-server only.

Full local stack with Docker Compose:

```bash
./scripts/dev-stack.zsh
```

This starts MySQL + Redis via `deploy/docker-compose.yml`, applies migrations, then launches `app-server` + `voice-gateway` through the existing local start flow. Use `./scripts/dev-down.sh` to stop compose services afterwards.

First-wave release evidence (session.end → worker → review ready):

```bash
./scripts/smoke-review-ready.sh
```

See `docs/03_第一波_review_ready_Smoke_Runbook.md`.

Optional MySQL 8 (Docker daemon required):

```bash
./scripts/dev-up.sh --mysql
```

Recommended entrypoint matrix:

- `./scripts/dev-up.sh`: lightweight in-memory bring-up
- `./scripts/dev-stack.zsh`: full local stack with Compose
- `./scripts/dev-check.sh`: fmt/lint/test/build gate
- `./scripts/smoke-review-ready.sh`: review-ready live smoke

Local checks:

```bash
./scripts/dev-check.sh
```

Full local run details: `docs/01_本地启动.md`  
First-wave scope: `docs/00_开发入口与第一波范围.md`
Shared schema mirrors: `./scripts/sync-shared-schemas.sh`

## Current surface

- `GET /healthz`, `GET /readyz`
- `POST /api/v1/auth/guest`
- `POST /api/v1/account/merge`
- `POST /api/v1/sessions`
- `GET /api/v1/sessions/:id/review` (pending | ready | failed)
- `POST /api/v1/sessions/:id/messages` (text degrade stub; `channel: text`)
- `POST /internal/v1/tickets/consume` (voice-gateway only; `X-Internal-Token`)
- `POST /internal/v1/sessions/activate` / `POST /internal/v1/sessions/end` (voice-gateway session lifecycle)
- async `session.finished` outbox + `cmd/worker` stub review pipeline (B5)
- voice-gateway WSS `GET /v1/voice` (JSON control frames; mirror schema in `schemas/transport/wss-control-frames-v1.json`, canonical source in `fluentwork-infra`)
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
schemas/
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
- pre-commit gstack review attestation (`GSTACK_REVIEWED=1`); CI does not run code review

## Upstream Source of Truth

Product rules, service boundaries, and milestone priorities should be aligned with `fluentwork-meta`.

## Agent Tooling

- before commit, run the interactive gstack review skill in your AI session: usually **`/review`**, or **`/gstack-review`** if skill prefixes are enabled; then `GSTACK_REVIEWED=1 git commit ...`
- emergency bypass: `SKIP_GSTACK_REVIEW=1` (justify in commit/PR)
- `gstack` can be used locally for deeper review and later `/setup-deploy` or `/ship`
- OCR scripts optional/manual only; not part of default pre-commit
- Matt Pocock style skills may be used as helpers under FluentWork shared governance
- GitHub CI does not run code review
