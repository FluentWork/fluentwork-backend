package reviewgen

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

const truncatedDocument = `{"review":{"goal_achievement":{"met":true,"note":"ok"},"issues":[],"suggestions":[],"comparisons":[{},{},{}]},"refine":{"blocks":[{"intent_zh":"同步进度"`

func TestParseGeneratedDocument_ClassifiesFailures(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		finishReason string
		want         FailureKind
	}{
		{"truncated by max_tokens", truncatedDocument, "length", FailureTruncatedJSON},
		{"truncated mid-token without finish_reason", truncatedDocument, "", FailureTruncatedJSON},
		{"complete but unparseable", `{"review": nope}`, "stop", FailureInvalidJSON},
		{"missing refine key", `{"review":{"goal_achievement":{}}}`, "stop", FailureMissingFields},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGeneratedDocument(tc.raw, tc.finishReason)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := KindOf(err); got != tc.want {
				t.Fatalf("kind = %q, want %q (err=%v)", got, tc.want, err)
			}
			var genErr *GenerateError
			if !errors.As(err, &genErr) {
				t.Fatalf("err is not *GenerateError: %T", err)
			}
			// P0-2: the raw document is kept whole so the log carries what the
			// model actually returned.
			if genErr.RawContent != tc.raw {
				t.Fatalf("raw content = %q, want %q", genErr.RawContent, tc.raw)
			}
			if genErr.FinishReason != tc.finishReason {
				t.Fatalf("finish reason = %q, want %q", genErr.FinishReason, tc.finishReason)
			}
		})
	}
}

func TestGenerateError_Raw(t *testing.T) {
	content := &GenerateError{RawContent: "model said this", RawBody: "body"}
	if content.Raw() != "model said this" {
		t.Fatalf("raw = %q, want content", content.Raw())
	}
	body := &GenerateError{RawBody: "body"}
	if body.Raw() != "body" {
		t.Fatalf("raw = %q, want body fallback", body.Raw())
	}
	var nilErr *GenerateError
	if nilErr.Raw() != "" || nilErr.Error() != "<nil>" || nilErr.Unwrap() != nil {
		t.Fatalf("nil GenerateError must be safe to use")
	}
	if KindOf(errors.New("plain")) != "" {
		t.Fatalf("plain error must not classify")
	}
}

func TestOrchestratorAdapter_ClassifiesFailureKinds(t *testing.T) {
	cases := []struct {
		name       string
		transcript string
		response   orchestrator.CompletionResponse
		clientErr  error
		want       FailureKind
	}{
		{
			name:       "empty session",
			transcript: "",
			want:       FailureEmptySession,
		},
		{
			name:       "transport",
			transcript: "hello",
			clientErr:  errors.New("dial tcp: timeout"),
			want:       FailureTransport,
		},
		{
			name:       "empty content",
			transcript: "hello",
			response:   orchestrator.CompletionResponse{},
			want:       FailureEmptyContent,
		},
		{
			name:       "truncated json",
			transcript: "hello",
			response: orchestrator.CompletionResponse{
				Content:      truncatedDocument,
				FinishReason: "length",
			},
			want: FailureTruncatedJSON,
		},
		{
			name:       "schema violation",
			transcript: "hello",
			response: orchestrator.CompletionResponse{
				Content:      `{"review":{"goal_achievement":{},"issues":[],"suggestions":[],"comparisons":[]},"refine":{"blocks":[]}}`,
				FinishReason: "stop",
			},
			want: FailureSchemaViolation,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &OrchestratorAdapter{
				Client: &orchestrator.MockClient{Response: tc.response, Err: tc.clientErr},
			}
			_, err := adapter.Generate(context.Background(), Request{
				SessionID:  "s1",
				SceneType:  "standup",
				Transcript: tc.transcript,
			})
			if err == nil {
				t.Fatal("expected error")
			}
			var genErr *GenerateError
			if !errors.As(err, &genErr) {
				t.Fatalf("err is not *GenerateError: %T (%v)", err, err)
			}
			if genErr.Kind != tc.want {
				t.Fatalf("kind = %q, want %q (err=%v)", genErr.Kind, tc.want, err)
			}
			if genErr.SessionID != "s1" {
				t.Fatalf("session id = %q, want s1", genErr.SessionID)
			}
			// Everything that failed *after* the model produced text must keep
			// that text; the two kinds below never had any (P0-2).
			hadNoOutput := tc.want == FailureEmptySession || tc.want == FailureTransport ||
				tc.want == FailureEmptyContent
			if !hadNoOutput && genErr.Raw() == "" {
				t.Fatalf("kind %q must carry the raw response", tc.want)
			}
		})
	}
}

// A validator failure must name every rule and keep the document verbatim, so
// the failure can be reproduced offline from the log alone (P0-2 acceptance 3).
func TestOrchestratorAdapter_SchemaViolationKeepsEveryRule(t *testing.T) {
	content := `{"review":{"goal_achievement":{},"issues":[],"suggestions":[],"comparisons":[]},"refine":{"blocks":[]}}`
	adapter := &OrchestratorAdapter{Client: &orchestrator.MockClient{
		Response: orchestrator.CompletionResponse{Content: content, FinishReason: "stop"},
	}}
	_, err := adapter.Generate(context.Background(), Request{
		SessionID:  "s1",
		SceneType:  "standup",
		Transcript: "hello",
	})
	var genErr *GenerateError
	if !errors.As(err, &genErr) {
		t.Fatalf("err is not *GenerateError: %T (%v)", err, err)
	}
	if len(genErr.ValidationRules) < 2 {
		t.Fatalf("validation rules = %v, want every failed rule", genErr.ValidationRules)
	}
	if !strings.Contains(genErr.Err.Error(), genErr.ValidationRules[0]) {
		t.Fatalf("error text %q must name the rules", genErr.Err.Error())
	}
	if genErr.RawContent != content {
		t.Fatalf("raw content lost")
	}
}

func TestUserPrompt_RendersRescueEvents(t *testing.T) {
	prompt := userPrompt(Request{
		SessionID:  "s1",
		SceneType:  "standup",
		Transcript: "user: I was going to say that the deploy is",
		StuckEvents: []StuckEvent{
			{
				Seq: 1, TurnID: "turn-1", Level: 3, Path: "incomplete",
				Ladder: "The deploy is blocked on the migration.", UserOpened: true,
				Anchor: "the deploy is",
			},
			{Seq: 2, Level: 1, Path: "silent"},
		},
	})

	for _, want := range []string{
		"rescue_events:",
		"seq=1 level=3 path=incomplete user_opened=true",
		"anchor: the deploy is",
		"ladder: The deploy is blocked on the migration.",
		"seq=2 level=1 path=silent user_opened=false",
		"transcript:",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// A silent event with no anchor must not grow one: §5.2.2 says such an
	// event produces no block, and an invented anchor would invent a stuck
	// point the transcript cannot corroborate.
	if strings.Count(prompt, "anchor:") != 1 {
		t.Fatalf("silent event must render no anchor:\n%s", prompt)
	}
}

func TestUserPrompt_OmitsRescueSectionWithoutEvents(t *testing.T) {
	prompt := userPrompt(Request{SessionID: "s1", SceneType: "standup", Transcript: "hello"})
	if strings.Contains(prompt, "rescue_events:") {
		t.Fatalf("unexpected rescue section:\n%s", prompt)
	}
}

func TestSystemPrompt_StatesRescueAnchorRules(t *testing.T) {
	prompt := systemPrompt()
	for _, want := range []string{
		"path=incomplete",
		"path=silent with an anchor",
		"never invent an anchor",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q", want)
		}
	}
}
