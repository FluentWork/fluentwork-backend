package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const testInternalToken = "test-internal-token"

type stubLLM struct {
	body string
	err  error
	// lastPrompt records what the generator asked for.
	lastPrompt string
}

func (s *stubLLM) Complete(_ context.Context, prompt string, _ CompletionOptions) (string, error) {
	s.lastPrompt = prompt
	if s.err != nil {
		return "", s.err
	}
	return s.body, nil
}

func newRescueEngine(t *testing.T, llm LLMClient) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	var gen *RescueGenerator
	if llm != nil {
		gen = NewRescueGenerator(llm)
	}
	RegisterInternalRoutes(engine.Group("/internal/v1"), NewHandler(gen, nil), testInternalToken)
	return engine
}

func postLadder(t *testing.T, engine *gin.Engine, body map[string]any, token string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/rescue/ladder", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestPostLadder_GeneratesAndPassesContext(t *testing.T) {
	llm := &stubLLM{body: "I think the main risk is…"}
	engine := newRescueEngine(t, llm)

	rec := postLadder(t, engine, map[string]any{
		"session_id": "s1", "user_id": "u1", "turn_id": "t1", "level": 1,
		"last_ai_message":  "What is blocking the release?",
		"scenario_context": "standup",
		"user_role":        "Backend Engineer",
		"recent_turns":     []map[string]any{{"speaker": "ai", "content": "hi"}},
	}, testInternalToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp LadderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Text != "I think the main risk is…" || resp.Level != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	// The model sees the conversation; the identifiers stay out of the prompt.
	if !strings.Contains(llm.lastPrompt, "What is blocking the release?") {
		t.Fatalf("prompt missing the AI's line: %s", llm.lastPrompt)
	}
	if strings.Contains(llm.lastPrompt, "u1") || strings.Contains(llm.lastPrompt, "s1") {
		t.Fatalf("identifiers leaked into the prompt: %s", llm.lastPrompt)
	}
}

func TestPostLadder_RejectsBadRequests(t *testing.T) {
	engine := newRescueEngine(t, &stubLLM{body: "x"})

	cases := []struct {
		name  string
		body  map[string]any
		token string
		want  int
	}{
		{"missing token", map[string]any{"session_id": "s1", "level": 1}, "", http.StatusUnauthorized},
		{"wrong token", map[string]any{"session_id": "s1", "level": 1}, "nope", http.StatusUnauthorized},
		{"level out of range", map[string]any{"session_id": "s1", "level": 4}, testInternalToken, http.StatusBadRequest},
		{"level zero", map[string]any{"session_id": "s1", "level": 0}, testInternalToken, http.StatusBadRequest},
		{"missing session", map[string]any{"level": 1}, testInternalToken, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := postLadder(t, engine, tc.body, tc.token); rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// No generator is a configuration state, not a model failure: say so, so the
// gateway's fallback is understood as "not wired" rather than "model down".
func TestPostLadder_UnconfiguredIsUnavailable(t *testing.T) {
	engine := newRescueEngine(t, nil)
	rec := postLadder(t, engine, map[string]any{"session_id": "s1", "level": 1}, testInternalToken)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// A model failure is 503 too — the gateway falls back to the static ladder, and
// a 5xx is what tells it to.
func TestPostLadder_ModelFailureIsUnavailable(t *testing.T) {
	engine := newRescueEngine(t, &stubLLM{err: errors.New("model down")})
	rec := postLadder(t, engine, map[string]any{"session_id": "s1", "level": 2}, testInternalToken)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
