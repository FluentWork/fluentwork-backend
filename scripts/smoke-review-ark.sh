#!/usr/bin/env bash
# Live smoke for B8 Ark review/refine generation.
# Usage:
#   zsh -ic 'proxy_on && ./scripts/smoke-review-ark.sh'
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -f "$ROOT/.env.volc.local" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$ROOT/.env.volc.local"
  set +a
fi

if [[ -z "${APP_ENV:-}" ]]; then
  export APP_ENV=development
fi

if [[ -z "${ARK_API_KEY:-}" && -n "${ARK_API_KEY_DEV:-}" ]]; then
  export ARK_API_KEY="${ARK_API_KEY_DEV}"
fi

if [[ -z "${ARK_API_KEY:-}" || -z "${ARK_EP_REVIEW_REFINE:-}" ]]; then
  echo "ARK_API_KEY/ARK_API_KEY_DEV or ARK_EP_REVIEW_REFINE is empty." >&2
  echo "Create $ROOT/.env.volc.local from configs/volc.env.example and fill the Ark review endpoint." >&2
  exit 1
fi

go run ./cmd/smoke-review-ark "$@"
