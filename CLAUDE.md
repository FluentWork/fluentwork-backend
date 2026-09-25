# FluentWork Backend

## Repo Role

`fluentwork-backend` is the Go service tier for FluentWork.

It owns three long-running services — `app-server` (HTTP API on 8080), `voice-gateway`
(WebSocket voice transport on 8081) and `worker` (async review pipeline) — plus the
development and measurement binaries under `cmd/`. It owns the API contract
(`api/openapi-v1.yaml`) and the database migrations (`migrations/`), but **not** the
shared transport and event schemas, which belong to `fluentwork-infra`.

## Shared Source Of Truth

Shared agent policy is maintained in `fluentwork-meta` under:

- `agents/shared/ai-collaboration.md`
- `agents/shared/git-and-pr-rules.md`
- `agents/shared/review-gate.md`
- `agents/shared/defect-fix-discipline.md`
- `agents/shared/skills-policy.md`
- `agents/shared/matt-pocock-skills.md`

Treat those files as the governance source. This file only adds backend-specific
constraints. `AGENTS.md` in this repository is the fuller local entry point — read it
before changing code.

## Repo-Specific Constraints

1. **Landing gate:** `./scripts/dev-check.sh`, seven steps, first failure stops the run.
   The pre-commit hook is not installed by default (`core.hooksPath` is unset) — run the
   gate yourself.
2. **Develop on `main`; do not open a PR unless asked.** One ticket per commit.
3. **Migrations are forward-only and not idempotent from `0008` on.** Replaying them
   aborts with `Duplicate column name`; pass `--skip-migrations` on any run after the
   first.
4. **Import boundaries are lint-enforced** via `depguard` in `.golangci.yml`:
   `voicegateway` must not import `content/tts`, `reviewgen`, `eval` or `account`; no
   `internal/**` package may import `voicepoc`.
5. **Environment loading has one implementation point** (`scripts/lib/load-env.sh`) with
   precedence real env var > later file > earlier file. Scripts must stay bash 3.2
   compatible and must close variables as `${var}` before non-ASCII text.
6. **Schemas:** change them in `fluentwork-infra`, then run
   `./scripts/sync-shared-schemas.sh`.
7. **No code comments.** Reasoning goes in the commit body, not next to the code.

## High-Risk Areas

1. `internal/voicegateway/` — session/turn orchestration and the provider boundary.
2. `internal/session/` — state machine plus two store implementations (`MemoryStore`,
   `MySQLStore`) whose atomicity is not guaranteed to match.
3. `migrations/` — forward-only, non-idempotent ALTERs.
4. `scripts/lib/load-env.sh` and `internal/config/` — environment precedence determines
   which provider and which credentials a process picks up.
5. Binary WSS frame layout shared with `fluentwork-ios`.
6. `internal/voicepoc/` — PoC/measurement code that production must not import.

## Expected Workflow

1. Read the code first; `go.mod`, `cmd/` and `internal/` are authoritative.
2. Prefer minimal diffs over broad rewrites.
3. Update tests when behavior changes; a defect fix must leave a test that failed first.
4. Put the evidence in the commit body — which test reproduced the defect, its output
   before the fix, the gate result. Do not create documents.
5. Respect review gates and owner approval for high-risk paths.
6. Push only when asked.

## Tooling Integrations

1. `gstack` review attestation is required by `.githooks/pre-commit` and by
   `fluentwork-meta/agents/shared/review-gate.md`, but `.cursor/rules/git-workflow.mdc`
   says not to require it. The hook path is not installed by default, so this is
   currently inert — see the drift notes in `AGENTS.md`.
2. `scripts/ocr-*.sh` (OpenCodeReview) are optional/manual only.
3. Matt Pocock style skills may be used as helpers, but FluentWork governance wins on
   conflicts.
