#!/usr/bin/env bash
# Curl-based live smoke for B8 Ark review/refine endpoint.
# Usage:
#   zsh -ic 'proxy_on && ./scripts/smoke-review-ark-curl.sh'
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -f "$ROOT/.env.volc.local" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$ROOT/.env.volc.local"
  set +a
fi

if [[ -z "${ARK_API_KEY:-}" && -n "${ARK_API_KEY_DEV:-}" ]]; then
  export ARK_API_KEY="${ARK_API_KEY_DEV}"
fi

if [[ -z "${APP_ENV:-}" ]]; then
  export APP_ENV=development
fi

if [[ -z "${ARK_API_KEY:-}" || -z "${ARK_EP_REVIEW_REFINE:-}" ]]; then
  echo "ARK_API_KEY/ARK_API_KEY_DEV or ARK_EP_REVIEW_REFINE is empty." >&2
  exit 1
fi

BODY_PATH="$(mktemp)"
RESP_PATH="$(mktemp)"
trap 'rm -f "$BODY_PATH" "$RESP_PATH"' EXIT

cat >"$BODY_PATH" <<EOF
{
  "model": "${ARK_EP_REVIEW_REFINE}",
  "messages": [
    {
      "role": "system",
      "content": "You are FluentWork review-refine generator. Return one JSON object only with exactly two top-level keys: review and refine. review must contain exactly goal_achievement, issues, suggestions, comparisons. refine must contain exactly blocks. review/refine must be objects, never strings. issues[].type MUST be one of: grammar, idiomatic, missing_info (no other types). refine.blocks[].function_tag MUST be one of: object, clarify, report, propose, agree, disagree, ask, summarize, defer, commit. scene_tag must equal standup for this sample. original_quote and anchor_user_said must be exact transcript substrings. comparisons items use only user and better. issues<=5, suggestions<=3, comparisons 3-8. Example: {\"review\":{\"goal_achievement\":{\"met\":true,\"note\":\"Clear blocker and next step.\"},\"issues\":[{\"type\":\"idiomatic\",\"original_quote\":\"sync up with the team\",\"hint\":\"Prefer touch base with the team.\"}],\"suggestions\":[{\"text\":\"Use touch base with the team.\"}],\"comparisons\":[{\"user\":\"sync up with the team\",\"better\":\"touch base with the team\"},{\"user\":\"I am blocked on the API review\",\"better\":\"I'm blocked waiting on the API review\"},{\"user\":\"tomorrow\",\"better\":\"tomorrow morning\"}]},\"refine\":{\"blocks\":[{\"intent_zh\":\"同步进度\",\"expression_en\":\"I'll touch base with the team tomorrow.\",\"anchor_user_said\":\"sync up with the team\",\"scene_tag\":\"standup\",\"function_tag\":\"report\"}]}}"
    },
    {
      "role": "user",
      "content": "scene_type: standup\nsession_id: smoke-b8-review-ark-curl\ntranscript:\nI will sync up with the team tomorrow. I am blocked on the API review."
    }
  ],
  "max_tokens": 800,
  "temperature": 0,
  "response_format": {
    "type": "json_object"
  },
  "thinking": {
    "type": "disabled"
  }
}
EOF

START_TS="$(python3 -c 'import time; print(time.time())')"
CODE="$(
  curl -sS --max-time 60 -o "$RESP_PATH" -w "%{http_code}" \
    "${ARK_BASE_URL:-https://ark.cn-beijing.volces.com/api/v3}/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${ARK_API_KEY}" \
    --data @"$BODY_PATH"
)"
END_TS="$(python3 -c 'import time; print(time.time())')"
WALL="$(python3 -c "print(round($END_TS-$START_TS,2))")"

if [[ "$CODE" != "200" ]]; then
  echo "smoke-review-ark-curl FAILED: HTTP $CODE wall=${WALL}s" >&2
  head -c 2000 "$RESP_PATH" >&2 || true
  exit 1
fi

echo "=== B8 Ark review/refine curl smoke PASS (wall=${WALL}s) ==="
jq '{id, model, usage, content: .choices[0].message.content, reasoning: (.choices[0].message.reasoning_content // null)}' "$RESP_PATH"
