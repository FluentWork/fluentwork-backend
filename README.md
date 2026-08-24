# FluentWork Backend

`fluentwork-backend` is the Go service repository for FluentWork.

## Scope

This repository will contain:

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
test/
.github/
```

## Engineering Baseline

- Go as the primary backend language
- clear separation between entrypoints and domain modules
- health, readiness, logging, and config from day one
- contract-first collaboration with iOS
- shared agent policy comes from `fluentwork-meta`
- external helpers such as gstack and Matt Pocock style skills are allowed, but repo rules win on conflicts

## CI Goals

- `go fmt`
- `go vet`
- unit tests
- integration tests
- migration checks
- Docker image build
- agent entry file validation
- report-only AI review integration as a secondary review layer

## Upstream Source of Truth

Product rules, service boundaries, and milestone priorities should be aligned with `fluentwork-meta`.

## Current Initialization Status

This repository currently includes:

- `CLAUDE.md`
- `AGENTS.md`
- `CODEOWNERS`
- `go.mod`
- `.github/workflows/agent-config-check.yml`
- `.github/workflows/backend-ci.yml`
- `.github/workflows/opencode-review.yml`
- executable Go module baseline
- initial service directory skeleton

## Agent Tooling

- `gstack` can be used locally for `/review` and later `/setup-deploy` or `/ship`
- Matt Pocock style skills may be used as helpers under FluentWork shared governance
- OpenCodeReview is initialized as a GitHub review workflow skeleton and should start in report-only mode
