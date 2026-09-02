# AGENTS

## Repository

- Name: `fluentwork-backend`
- Role: Go services, gateway, workers, migrations, and contracts for FluentWork

## Shared Rules

This repository inherits shared agent policy from `FluentWork/fluentwork-meta/agents/shared/`.

Shared topics:

1. AI collaboration and role split
2. Git and PR rules
3. Review gate
4. Skills policy
5. Matt Pocock skills usage boundary

## Local Rules

1. Protect gateway, migrations, auth, and session state changes.
2. Keep contracts explicit and synchronized with tests.
3. Avoid broad rewrites across unrelated services.
4. Treat operational safety as a first-class requirement.

## Required Behaviors

1. Read current backend and architecture docs before editing.
2. Keep changes scoped to the active service or contract.
3. Do not bypass review, CI, or owner approval requirements.
4. Before opening or merging a PR, run the interactive gstack review skill on the branch diff, normally **`/review`** and **`/gstack-review`** when skill prefixes are enabled; fix must-fix findings (see `fluentwork-meta/agents/shared/review-gate.md`).
5. Do not perform destructive git operations without explicit approval.
6. Surface data safety, migration, and deploy implications clearly.

## High-Risk Paths

1. Gateway and session state machine logic
2. Database migrations and delete flows
3. Auth, idempotency, and concurrency-sensitive code
4. Production deploy and environment configuration

## Script Index

Use these entries in order:

1. `./scripts/dev-up.sh` — default lightweight local start, in-memory store, no Docker
2. `./scripts/dev-stack.zsh` — full local stack, Docker Compose MySQL + Redis + backend processes
3. `./scripts/dev-check.sh` — required local quality gate
4. `./scripts/smoke-review-ready.sh` — first-wave live evidence path

Low-level scripts such as `local-services-start.sh`, `local-db-init.sh`, and `dev-local-start.sh` are troubleshooting/building blocks, not the default entrypoints for new work.

## Local Review Gate

1. Required before commit: run the interactive gstack review skill, normally **`/review`** in Codex/Cursor/Claude. If your gstack config enables skill prefixes, use **`/gstack-review`** instead. Then commit with `GSTACK_REVIEWED=1 git commit ...`.
2. pre-commit → `scripts/gstack-review-gate.sh` (attestation; skill cannot run in bash).
3. One-time hooks: `./scripts/setup-git-hooks.sh` (sets `core.hooksPath=.githooks`).
4. Emergency bypass: `SKIP_GSTACK_REVIEW=1` (justify in commit/PR body).
5. OCR scripts are optional/manual only; not part of the default gate.

## CI Boundary

CI validates fmt, vet, tests, builds, and configuration. CI does not run code review or a full interactive skills runtime.
