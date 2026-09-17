// Package reviewgen builds review/refine artifacts for a finished session.
//
// It speaks to a model through orchestrator.Client (the LLM seam), so nothing in
// this package knows which vendor answers: prompts, validation and failure
// classification are provider-neutral.
package reviewgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/eval"
)

// Generator builds review/refine artifacts from a finished transcript.
type Generator interface {
	Generate(ctx context.Context, req Request) (Result, error)
}

// Request is the normalized input sent to one review/refine generation call.
type Request struct {
	SessionID  string
	UserID     string
	SceneType  string
	Transcript string
	// StuckEvents is the session's B8 rescue ladders (PRD §5.4.4). Refine's
	// input is not the transcript alone: without these the silent path's stuck
	// points — a user who said nothing until the ladder arrived — leave no
	// trace, because the transcript has no user text for them to anchor to.
	StuckEvents []StuckEvent
}

// StuckEvent is one rescue ladder as refine sees it. The anchor is already
// resolved by the gateway per PRD §5.2.2: the half-sentence for the incomplete
// path, the first sentence after the ladder for the silent path, and empty when
// the user never spoke — which means the event produces no phrase block.
type StuckEvent struct {
	Seq        int    `json:"seq"`
	TurnID     string `json:"turn_id,omitempty"`
	Level      int    `json:"level"`
	Path       string `json:"path,omitempty"`
	Ladder     string `json:"ladder,omitempty"`
	UserOpened bool   `json:"user_opened"`
	Anchor     string `json:"anchor,omitempty"`
}

// Result is one successful generation output.
type Result struct {
	Review    json.RawMessage
	Refine    json.RawMessage
	Generator string
	Model     string
	TokensIn  int
	TokensOut int
}

// generatedDocument is the two-key document the model must return.
type generatedDocument struct {
	Review json.RawMessage `json:"review"`
	Refine json.RawMessage `json:"refine"`
}

func parseGeneratedDocument(raw, finishReason string) (generatedDocument, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	var doc generatedDocument
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return generatedDocument{}, &GenerateError{
			Kind:         classifyJSONError(finishReason, err),
			Err:          fmt.Errorf("decode generated json: %w", err),
			RawContent:   raw,
			FinishReason: finishReason,
		}
	}
	if len(doc.Review) == 0 || len(doc.Refine) == 0 {
		return generatedDocument{}, &GenerateError{
			Kind:         FailureMissingFields,
			Err:          fmt.Errorf("generated json must include review and refine"),
			RawContent:   raw,
			FinishReason: finishReason,
		}
	}
	return doc, nil
}

// classifyJSONError separates "the model was cut off" from "the model returned
// something that is not our JSON". finish_reason=length is decisive; without it
// the truncation signature is encoding/json's "unexpected end of JSON input",
// which fires only when the payload stops mid-token.
func classifyJSONError(finishReason string, err error) FailureKind {
	if strings.EqualFold(strings.TrimSpace(finishReason), "length") {
		return FailureTruncatedJSON
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) && strings.Contains(syntaxErr.Error(), "unexpected end of JSON input") {
		return FailureTruncatedJSON
	}
	return FailureInvalidJSON
}

func withSession(err error, sessionID string) error {
	var genErr *GenerateError
	if errors.As(err, &genErr) && genErr != nil {
		genErr.SessionID = sessionID
		return genErr
	}
	return err
}

// schemaViolation reports every failed B15 rule plus the raw document, so a
// validator failure can be reproduced from the log alone (P0-2).
func schemaViolation(sessionID, content, finishReason string, findings []eval.Finding) *GenerateError {
	rules := make([]string, 0, len(findings))
	for _, finding := range findings {
		rules = append(rules, finding.Rule)
	}
	return &GenerateError{
		Kind:            FailureSchemaViolation,
		Err:             fmt.Errorf("generated document failed B15 validation: %s", strings.Join(rules, ",")),
		SessionID:       sessionID,
		FinishReason:    finishReason,
		RawContent:      content,
		ValidationRules: rules,
	}
}

func systemPrompt() string {
	return strings.TrimSpace(`
You are FluentWork review-refine generator.
Return one JSON object only with exactly two top-level keys: review and refine.
Do not add any other keys.

Required output shape:
{
  "review": {
    "goal_achievement": {"met": boolean, "note": string},
    "issues": [{"type":"grammar|idiomatic|missing_info","original_quote": string, "hint": string}],
    "suggestions": [{"text": string}],
    "comparisons": [object, object, object]
  },
  "refine": {
    "blocks": [{
      "intent_zh": string,
      "expression_en": string,
      "anchor_user_said": string,
      "scene_tag": string,
      "function_tag": string
    }]
  }
}
Rules:
- output valid JSON only
- review must be an object, never a string
- refine must be an object, never a string
- review must contain exactly: goal_achievement, issues, suggestions, comparisons
- refine must contain exactly: blocks
- issues <= 5
- suggestions <= 3
- comparisons length 3-8
- every original_quote and anchor_user_said must be exact substrings from the transcript
- rescue_events lists the B8 ladders that fired, in order; they are the stuck points worth refining
- path=incomplete: anchor_user_said is the user's half-sentence, and expression_en is the complete expression the ladder gave (or a better one)
- path=silent with an anchor: anchor_user_said is that anchor (the user's first sentence after the ladder), and expression_en is the level-3 complete expression
- path=silent with no anchor (the user never spoke): emit no block for it, and never invent an anchor
- do not emit a block whose anchor is not in the transcript
- scene_tag must be one of: standup, review, 1on1, interview, casual; prefer the provided scene_type when it is one of these
- function_tag must be one of: object, clarify, report, propose, agree, disagree, ask, summarize, defer, commit
- keep every string concise
- when there is no issue, use [] rather than prose
- comparisons items must use keys user and better only

Example:
{
  "review": {
    "goal_achievement": {"met": true, "note": "Clear blocker and next step."},
    "issues": [{"type": "idiomatic", "original_quote": "sync up with the team", "hint": "Prefer touch base with the team."}],
    "suggestions": [{"text": "Use touch base with the team."}],
    "comparisons": [
      {"user": "sync up with the team", "better": "touch base with the team"},
      {"user": "I am blocked on the API review", "better": "I'm blocked waiting on the API review"},
      {"user": "tomorrow", "better": "tomorrow morning"}
    ]
  },
  "refine": {
    "blocks": [
      {"intent_zh": "同步进度", "expression_en": "I'll touch base with the team tomorrow.", "anchor_user_said": "sync up with the team", "scene_tag": "standup", "function_tag": "report"}
    ]
  }
}
`)
}

func userPrompt(req Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "scene_type: %s\nsession_id: %s\n", req.SceneType, req.SessionID)
	if len(req.StuckEvents) > 0 {
		b.WriteString("rescue_events:\n")
		for _, event := range req.StuckEvents {
			path := strings.TrimSpace(event.Path)
			if path == "" {
				path = "silent"
			}
			fmt.Fprintf(&b, "- seq=%d level=%d path=%s user_opened=%t\n",
				event.Seq, event.Level, path, event.UserOpened)
			if anchor := strings.TrimSpace(event.Anchor); anchor != "" {
				fmt.Fprintf(&b, "  anchor: %s\n", anchor)
			}
			if ladder := strings.TrimSpace(event.Ladder); ladder != "" {
				fmt.Fprintf(&b, "  ladder: %s\n", ladder)
			}
		}
	}
	fmt.Fprintf(&b, "transcript:\n%s", req.Transcript)
	return b.String()
}
