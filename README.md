# FluentWork Backend

`fluentwork-backend` is the Go service tier for FluentWork — the app-server HTTP API,
the voice-gateway WebSocket transport, and an async worker.

Module: `github.com/FluentWork/fluentwork-backend` (Go 1.26.0).

## Services

| Binary | Command | Default port | Role |
|---|---|---|---|
| `app-server` | `./cmd/app-server` | 8080 | HTTP API — guest auth, sessions, review, corpus, materials, topic cards, session history |
| `voice-gateway` | `./cmd/voice-gateway` | 8081 | WebSocket voice transport at `/v1/voice`; talks to the speech provider and to app-server over internal HTTP |
| `worker` | `./cmd/worker` | — | async review pipeline |

Both HTTP services expose `GET /healthz` (liveness) and `GET /readyz` (readiness) —
see `internal/httpserver/server.go:86-87`.

`cmd/` also holds 17 development and measurement binaries (`smoke-*`, `eval-*`,
`gen-*`, `test-orchestrator`, `poc-injection-window`, `ark-endpoint-probe`, `asr-wer`,
`corpus-seed`, `integration-voice-gateway`). They are tooling, not production services.

## Requirements

- Go 1.26+
- MySQL 8.0+ and Redis 7.0+ — only for the MySQL-backed paths. The default dev entry
  point runs in-memory and needs neither.

## Start

```bash
./scripts/dev-up.sh                 # app-server + voice-gateway, in-memory store
./scripts/dev-status.sh             # what is running, on which port, with which log
```

`dev-up.sh` needs no Docker: it uses the in-memory account/session store, waits for
both services to become healthy, runs a guest-auth smoke, and seeds the badge corpus
so "speak a phrase → get a badge" works without a second command. Each service is
built into `./bin/` and supervised; logs go to `.dev-logs/<name>.log`.

Flags:

| Flag | Effect |
|---|---|
| `--mysql` | start MySQL 8 via Docker Compose and apply migrations |
| `--local-mysql` | use an already-running local MySQL with `fw/fw` credentials |
| `--skip-migrations` | reuse the schema already in MySQL — **required on every run after the first** |
| `--no-gateway` | run app-server only |
| `--no-seed` | skip the badge corpus seed |
| `--no-kill-stale` | refuse to reclaim ports held by our own leftover dev services |
| `--port` / `--gateway-port` | override 8080 / 8081 |
| `--host IP` | set the host in the `wss_url` returned to clients (default `127.0.0.1`) |

`--skip-migrations` is not a convenience. The ALTER migrations (0008 onward) are not
idempotent, so replaying them aborts with `Duplicate column name ...` even when the
schema is already current.

Full Docker Compose stack (MySQL on 3306, Redis on 6379, migrations, both services):

```bash
./scripts/dev-stack.zsh
./scripts/dev-down.sh               # stops the Compose services only
```

Homebrew-based low-level path: `./scripts/local-services-start.sh`,
`./scripts/local-db-init.sh`, `./scripts/dev-local-start.sh`,
`./scripts/local-services-status.sh`, `./scripts/local-services-stop.sh`.

## Configuration

Copy the relevant example and fill it in:

- `configs/app-server.env.example`
- `configs/voice-gateway.env.example`
- `configs/volc.env.example` → repo-root `.env.volc.local` (gitignored)

`.env.volc.local` is the only real environment file in a working tree. `configs/*.env.example`
are the committed templates. Note that `SETUP.md` still refers to a `.env.dev` file that
does not exist and cannot be committed (`.gitignore` matches `.env.*`);
`dev-local-start.sh` carries explicit defaults instead.

dotenv parsing has exactly one implementation point: `scripts/lib/load-env.sh`.
Precedence is **real environment variable > later-loaded file > earlier-loaded file**.
`scripts/check-env-loaders.sh` asserts that semantics and runs inside the gate. Only
bash 3.2 syntax is allowed in these scripts — macOS ships `/bin/bash` 3.2.57 and every
script shebang resolves to it.

## Landing gate

```bash
./scripts/dev-check.sh
```

Eight steps, and **the first failure stops the run**:

1. `gofumpt -l .`
2. `goimports -l .`
3. `golangci-lint run ./...` (config in `.golangci.yml`, includes `depguard` import-boundary rules)
4. `go test ./...`
5. `go build ./...`
6. `scripts/check-env-loaders.sh` — asserts dotenv semantics
7. `scripts/check-dev-service.sh` — asserts the dev-service supervisor (pid/process-group/port safety)
8. `scripts/check-defect-discipline.sh` — asserts that implementation notes changed in the
   working tree carry a 「测试」/「门禁」section

`.githooks/pre-commit` chains `scripts/gstack-review-gate.sh` and then `dev-check.sh`,
with `SKIP_DEV_CHECK=1` as the emergency bypass. **`core.hooksPath` is unset in a fresh
clone**, so nothing runs automatically: run `./scripts/setup-git-hooks.sh` once, and run
`dev-check.sh` yourself before pushing. `golangci-lint` is installed under
`$(go env GOPATH)/bin`; `dev-check.sh` puts that directory on `PATH` itself.

## Layout

| Path | Contents |
|---|---|
| `cmd/` | 20 `main` packages — 3 services + 17 dev/measurement tools |
| `internal/` | 22 packages: `voicegateway` (25 files), `content` (18), `topic` (16), `corpus` (15), `account`, `aicost`, `session`, `drill`, `materials`, `orchestrator`, `review`, `reviewgen`, `voiceduplex`, `voicepoc`, `voiceproto`, `sessionhistory`, `conversation`, `config`, `apierr`, `httpjson`, `httpserver`, `eval` |
| `api/` | `openapi-v1.yaml` — the app-server contract (OpenAPI 3.0.3, 25 paths) |
| `migrations/` | `0001`–`0026` forward SQL + `down/`; embedded via `migrations/embed.go` |
| `schemas/` | mirrors of the infra-owned transport and event schemas |
| `configs/` | `*.env.example` templates |
| `pkg/` | `buildinfo`, `logx` |
| `deploy/` | `docker-compose.yml` |
| `scripts/` | dev entry points, gates, smokes, integration harnesses |
| `eval/`, `test/` | eval datasets and test support |

## Shared schemas

The WSS control-frame and speech-observability schemas are owned by
`fluentwork-infra`:

```bash
./scripts/sync-shared-schemas.sh    # infra/schemas/** -> schemas/{transport,events}/
```

Change the schema in `fluentwork-infra` first, then sync outward.

## CI

- `backend-ci.yml` — `repo-structure-check` (asserts `cmd/app-server`, `cmd/voice-gateway`,
  `cmd/worker`, `internal`, `migrations`, `configs`, `go.mod`, `Dockerfile`, the first
  three migrations and `api/openapi-v1.yaml`) and `go-build-and-test` (gofumpt,
  goimports, golangci-lint, `go test`, `go build`, `docker build`).
- `agent-config-check.yml` — requires `CLAUDE.md` and `AGENTS.md` to exist and to contain
  the string `fluentwork-meta`.

`Dockerfile` builds `cmd/app-server` only and runs it on `distroless/static-debian12:nonroot`.

## Known drift

1. **Build binaries are committed at the repository root.** `app-server` (31 MB),
   `smoke-moat` (31 MB), `worker` (20 MB), `corpus-seed` (8.7 MB) and
   `ark-endpoint-probe` (8.1 MB) are tracked — roughly 99 MB of build output in git.
   `.gitignore` covers `bin/` and `/cmd/*/voice-gateway` but not these paths. They are
   rebuilt into `bin/` by the dev scripts, so the root copies are stale duplicates.
2. **`SETUP.md` documents a `.env.dev` that does not exist.** See Configuration above.
3. **Gate scripts cite deleted documents.** `.golangci.yml` references `docs/99_`,
   `check-env-loaders.sh` references `docs/106_`, `check-dev-service.sh` references
   `docs/109_`, and `check-defect-discipline.sh` references `docs/36`. Those files are
   gone, so the "why" behind each check now lives only in the scripts themselves.

## Related repositories

- `fluentwork-meta` — product, architecture and governance source of truth
- `fluentwork-ios` — SwiftUI client
- `fluentwork-infra` — deployment, environments, shared schemas
