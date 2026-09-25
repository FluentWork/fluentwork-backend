# AGENTS

## Repository

- Name: `fluentwork-backend`
- Role: Go service tier — `app-server` (HTTP API), `voice-gateway` (WebSocket voice
  transport), `worker` (async review pipeline), plus dev and measurement tooling

## Shared Rules

This repository inherits shared agent policy from `fluentwork-meta/agents/shared/`.

Shared topics:

1. AI collaboration and role split
2. Git and PR rules
3. Review gate
4. Defect fix discipline (reproduce → fix → keep the guard → prove the guard bites)
5. Skills policy
6. Matt Pocock skills usage boundary

Read the shared file before inventing a local rule. This file only adds what is
specific to this repository.

## Local Rules

1. **Landing gate.** `./scripts/dev-check.sh` — eight steps, and the first failure stops
   the run: gofumpt → goimports → golangci-lint → `go test` → `go build` →
   `check-env-loaders.sh` → `check-dev-service.sh` → `check-defect-discipline.sh`.
2. **The gate does not run itself.** `.githooks/pre-commit` chains
   `scripts/gstack-review-gate.sh` then `dev-check.sh`, but `core.hooksPath` is unset in
   a fresh clone. Run `./scripts/setup-git-hooks.sh` once, and run `dev-check.sh`
   yourself before pushing. A successful `git commit` is not evidence the gate passed.
3. **Develop on `main`.** Do not open a PR unless explicitly asked. Commit after the gate
   passes; push only when asked. Emergency bypass is `SKIP_DEV_CHECK=1`.
4. **One ticket per commit.** Keep each commit to a single topic.
5. **Implementation notes.** After the gate passes, add `docs/NN_<ticket>_实现说明.md`
   covering the principle, the approach, the root cause of any defect, why the new design
   is right, and the test evidence — and commit it with the code. See the drift note
   below: `docs/` does not currently exist.
6. **Import boundaries are enforced by lint, not by convention.** `.golangci.yml`
   `depguard` rules: `internal/voicegateway/**` may not import `internal/content/tts`,
   `internal/reviewgen`, `internal/eval`, or `internal/account`; nothing under
   `internal/**` may import `internal/voicepoc`. Violating either is a red gate, not a
   style discussion.
7. **dotenv parsing has one implementation point:** `scripts/lib/load-env.sh`. Do not
   write another inline loader in any script. Precedence is real environment variable >
   later-loaded file > earlier-loaded file.
8. **Bash 3.2 only.** macOS ships `/bin/bash` 3.2.57 and every script shebang resolves to
   it — no `declare -A`, no bash 4+ features. In a script that prints Chinese text,
   always close a variable with `${var}`: under `LC_ALL=C`, `$var` followed by a
   non-ASCII byte swallows the byte into the variable name and aborts under `set -u`
   with a garbled error. `check-dev-service.sh` enforces this statically.
9. **Schemas are owned by `fluentwork-infra`.** Never hand-edit `schemas/`; change the
   schema in infra and run `./scripts/sync-shared-schemas.sh`.
10. **No code comments by default.** Do not add header blocks, doc comments, or inline
    rationale to new or changed code unless explicitly asked. Reasoning goes into the
    implementation note. Leave existing comments alone.

## Required Behaviors

1. Read the current code before editing. `go.mod`, `cmd/`, and `internal/` are
   authoritative; do not infer structure from directory names.
2. Keep changes scoped to the active task.
3. Do not bypass review, CI, or owner approval requirements.
4. Do not perform destructive git operations without explicit approval.
5. Surface risks clearly when touching high-risk areas.
6. When you fix a defect, leave a test that failed before the fix — see
   `fluentwork-meta/agents/shared/defect-fix-discipline.md`. Then break the
   implementation and confirm the *expected* test goes red. A test that was already
   green proves nothing.

## High-Risk Paths

1. `internal/voicegateway/` — session/turn orchestration and the speech provider
   boundary. The import boundary above exists because this package once dragged the
   review and eval domains in as transitive dependencies.
2. `internal/session/` — session state machine and the store implementations. Two stores
   (`MemoryStore`, `MySQLStore`) implement the same interface; they are not required to
   be atomic in the same way.
3. `migrations/` — forward-only and **not idempotent from `0008` on**. Replaying them
   aborts with `Duplicate column name`. Use `--skip-migrations` after the first run.
4. `scripts/lib/load-env.sh` and `internal/config/` — environment precedence. A wrong
   precedence here silently changes which credentials and which provider a process uses.
5. Binary WSS frame layout shared with `fluentwork-ios` and the frozen schema in
   `fluentwork-infra`.
6. `internal/voicepoc/` — PoC and measurement code. Production code must not depend on
   it.

## Known Drift

These are measured, not suspected. Fix or work around them deliberately.

1. **Gate step 8 cannot fail right now.** `check-defect-discipline.sh` scans `docs/` for
   changed `*实现说明*.md` files, but this repository has no `docs/` directory. It
   therefore reports "本次没有改动实现说明" and passes unconditionally — a green check
   that is currently incapable of detecting anything. Restore `docs/` (or repoint the
   script) before relying on it.
2. **Build binaries are committed at the repository root.** `app-server`, `smoke-moat`,
   `worker`, `corpus-seed` and `ark-endpoint-probe` are tracked, ~99 MB in total.
   `.gitignore` covers `bin/` but not these root paths.
3. **`SETUP.md` documents a `.env.dev` that does not exist** and cannot be committed
   (`.gitignore` matches `.env.*`). `dev-local-start.sh` carries explicit defaults.
4. **Gate scripts cite deleted documents** — `.golangci.yml` → `docs/99_`,
   `check-env-loaders.sh` → `docs/106_`, `check-dev-service.sh` → `docs/109_`,
   `check-defect-discipline.sh` → `docs/36`. The rationale for each check now lives only
   in the script's own header comment.
5. **Review-gate policy disagrees with itself.** `.cursor/rules/git-workflow.mdc` says
   not to require gstack review attestation; `.githooks/pre-commit` calls
   `scripts/gstack-review-gate.sh`, which blocks without `GSTACK_REVIEWED=1`; and
   `fluentwork-meta/agents/shared/review-gate.md` mandates the attestation for all three
   code repositories. Since the hook path is not installed by default, the disagreement
   is currently inert — resolve it before enabling hooks.

## Local Review Gate

1. If you enable hooks, pre-commit runs `scripts/gstack-review-gate.sh` first and
   requires `GSTACK_REVIEWED=1`; `SKIP_GSTACK_REVIEW=1` is the documented bypass and must
   be justified in the commit or PR body.
2. `scripts/ocr-*.sh` (OpenCodeReview) are optional/manual only and are not part of the
   default gate.
3. See drift note 5 above before treating this as settled.

## CI Boundary

CI runs `repo-structure-check`, then gofumpt, goimports, golangci-lint, `go test`,
`go build` and a `docker build` of the app-server image. It validates that `CLAUDE.md`
and `AGENTS.md` exist and reference `fluentwork-meta`. CI does not run the interactive
gstack review skill and does not load a skills runtime.
