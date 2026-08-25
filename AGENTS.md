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
4. Before each commit, local OpenCodeReview must pass: fix any `high` / `critical` findings (see `scripts/ocr-local-review.sh`); `medium` / `low` may remain as follow-ups.
5. Do not perform destructive git operations without explicit approval.
6. Surface data safety, migration, and deploy implications clearly.

## High-Risk Paths

1. Gateway and session state machine logic
2. Database migrations and delete flows
3. Auth, idempotency, and concurrency-sensitive code
4. Production deploy and environment configuration

## Local Review Gate

1. One-time per clone: `./scripts/setup-git-hooks.sh` (sets `core.hooksPath=.githooks`).
2. Pre-commit runs `scripts/ocr-local-review.sh` (OCR CLI + `ocr-fail-on-high.sh`).
3. Emergency bypass only: `SKIP_OCR=1`, and justify in the commit/PR body.
4. Optional archive: `./scripts/ocr-export-review.sh` after a review.

## CI Boundary

CI validates fmt, vet, tests, builds, and configuration. CI does not run OpenCodeReview or a full interactive skills runtime.
