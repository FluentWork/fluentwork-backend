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
4. Before opening or merging a PR, run **gstack `/review`** on the branch diff; fix must-fix findings (see `fluentwork-meta/agents/shared/review-gate.md`).
5. Do not perform destructive git operations without explicit approval.
6. Surface data safety, migration, and deploy implications clearly.

## High-Risk Paths

1. Gateway and session state machine logic
2. Database migrations and delete flows
3. Auth, idempotency, and concurrency-sensitive code
4. Production deploy and environment configuration

## Local Review Gate

1. Required before commit: run **gstack `/review`**, then `GSTACK_REVIEWED=1 git commit ...`.
2. pre-commit → `scripts/gstack-review-gate.sh` (attestation; skill cannot run in bash).
3. One-time hooks: `./scripts/setup-git-hooks.sh` (sets `core.hooksPath=.githooks`).
4. Emergency bypass: `SKIP_GSTACK_REVIEW=1` (justify in commit/PR body).
5. OCR scripts are optional/manual only; not part of the default gate.

## CI Boundary

CI validates fmt, vet, tests, builds, and configuration. CI does not run code review or a full interactive skills runtime.
