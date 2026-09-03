package voicegateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
)

func TestHTTPCorpusSourceFetchesCandidatesForUser(t *testing.T) {
	var gotToken, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Internal-Token")
		gotUser = r.URL.Query().Get("user_id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id": "block-1", "intent_zh": "推动上线",
					"expression_en": "Let's ship it.", "anchor_user_said": "let's ship it",
					"scene_tag": "review", "function_tag": "commit",
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	src := voicegateway.NewHTTPCorpusSource(srv.URL, "internal-token", nil)
	candidates, err := src.CandidatesForUser(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("CandidatesForUser: %v", err)
	}
	if gotToken != "internal-token" || gotUser != "user-1" {
		t.Fatalf("request headers/query = token %q user %q", gotToken, gotUser)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v", candidates)
	}
	if candidates[0].ID != "block-1" || candidates[0].ExpressionEN != "Let's ship it." {
		t.Fatalf("unexpected candidate: %+v", candidates[0])
	}
}
