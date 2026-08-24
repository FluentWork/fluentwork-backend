# OpenCodeReview local review archives

OCR CLI sessions normally live under `~/.opencodereview/sessions/` (outside the repo), which makes findings hard to share or re-open later.

This folder stores **exported** review artifacts for FluentWork backend.

## Files

| File | Purpose |
|---|---|
| `latest.md` | Newest human-readable comment dump |
| `latest.meta.txt` | Newest session metadata |
| `latest.json` | Newest machine-readable comments |
| `<stamp>-<label>-<session>.md` | Historical exports |

Raw session JSONL remains in `~/.opencodereview/sessions/` and is not copied here (it can be large and may contain prompt/tool payloads).

## Export after a review

```bash
ocr review
./scripts/ocr-export-review.sh

# or pin a session / label
./scripts/ocr-export-review.sh <session-id> b1-guest-auth
```

List sessions:

```bash
ocr session list
ocr session show <session-id>
ocr session comments <session-id>
```

## Policy

1. Keep exports readable; do not commit secrets or `.env` contents.
2. Prefer summarizing disposition (fixed / deferred) in the PR body when findings drove code changes.
3. GitHub PR OpenCodeReview remains the secondary report layer; local exports are for author visibility.
