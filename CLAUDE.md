# FluentWork Backend

## Repo Role

`fluentwork-backend` is the Go service repository for FluentWork.

It should implement service boundaries, contracts, and operational rules defined upstream in `fluentwork-meta`.

## Shared Source Of Truth

Shared agent policy is maintained in `FluentWork/fluentwork-meta` under:

- `agents/shared/ai-collaboration.md`
- `agents/shared/git-and-pr-rules.md`
- `agents/shared/review-gate.md`
- `agents/shared/skills-policy.md`
- `agents/shared/matt-pocock-skills.md`

This file only adds backend-specific constraints.

## Repo-Specific Constraints

1. Keep service boundaries explicit.
2. Treat migration, gateway, session state, and auth logic as high-risk.
3. Prefer contract-preserving changes over broad rewrites.
4. Update tests and schemas when interfaces or behavior change.
5. Matt Pocock style skills may assist, but FluentWork backend rules win on conflicts.

## High-Risk Areas

1. voice gateway and session state machine
2. database migrations and deletion paths
3. auth, rate limiting, and idempotency logic
4. deploy and prod-facing configuration

## Expected Workflow

1. Read upstream backend and overall technical docs first.
2. Keep API, worker, and gateway changes scoped.
3. Update tests, migrations, or contracts together when needed.
4. Respect CODEOWNERS and review gates for risky changes.
5. Call out operational or data-risk implications clearly.

## Tooling Integrations

1. `gstack` may be used locally for review and deploy-planning assistance.
2. Matt Pocock style skills may be used as helpers under FluentWork shared policy.
3. OpenCodeReview runs on PRs: any `high` finding blocks merge until fixed; no `high` means merge is allowed (see `fluentwork-meta/agents/shared/review-gate.md`).
