# Offline eval samples (second-wave B15 / GitHub #28)

This directory is the **prompt regression baseline** for session review + refine JSON.
It is not loaded by app-server or the worker.

## Provenance

| Field | Value |
| --- | --- |
| File | `samples/wave2-synth-v1.json` |
| Kind | Synthetic / demo workplace English. **No user transcripts.** |
| Count | 103 (≥ 50 required) |
| Scenes | `standup` `review` `1on1` `interview` `casual` |
| Authoring | 3 hand-written seeds (`syn-standup-01` …) plus `go run ./cmd/gen-eval-samples` (`syn-w2-v1-*`) |

Each sample is a **learner turn that needs polish** (grammar / idiomatic slips, blockers, deferrals) plus a **gold review/refine** that already satisfies `eval.ValidateSample`. “Good / bad” here means bad learner wording vs good refined triples, not a dataset of expected validator failures. Failure rules live in `internal/eval/validate_test.go`.

## Annotation rules (locked to the validator)

1. `issues[].original_quote` is a literal substring of `transcript`; `type` ∈ grammar / idiomatic / missing_info.
2. `refine.blocks[]` has `intent_zh` / `expression_en` / `anchor_user_said`; anchor is in the transcript; `scene_tag` / `function_tag` are closed enums.
3. `review.issues` ≤ 5, `suggestions` ≤ 3, `comparisons` 3–8.

## Run

```bash
./scripts/eval-prompt-regression.sh
```

After changing `internal/reviewgen` prompts, this must stay green.
