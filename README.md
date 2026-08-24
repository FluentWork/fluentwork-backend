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

## CI Goals

- `go fmt`
- `go vet`
- unit tests
- integration tests
- migration checks
- Docker image build

## Upstream Source of Truth

Product rules, service boundaries, and milestone priorities should be aligned with `fluentwork-meta`.
