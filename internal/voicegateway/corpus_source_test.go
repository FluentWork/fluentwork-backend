package voicegateway_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
)

// This test drives app-server's real corpus route rather than a hand-built body.
//
// It used to build the response itself, with the same field names the client
// expects — which is the shape of the B8 ladder bug: when the producer and the
// fixture are written from the same reading, a rename on the server side cannot
// fail this test, it can only make the real response decode to zero candidates.
// That failure is invisible: the detector finds nothing and reports a miss.
func TestHTTPCorpusSourceFetchesCandidatesForUser(t *testing.T) {
	store := corpus.NewMemoryStore()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if _, err := store.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID:             "block-1",
		UserID:         "user-1",
		IntentZH:       "推动上线",
		ExpressionEN:   "Let's ship it.",
		AnchorUserSaid: "let's ship it",
		SceneTag:       "review",
		FunctionTag:    "commit",
		State:          corpus.StateNew,
		NextDueAt:      now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, {
		ID:           "block-other",
		UserID:       "user-2", // another learner's block must not be a candidate
		ExpressionEN: "Not yours.",
		State:        corpus.StateNew,
		NextDueAt:    now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	corpus.RegisterInternalRoutes(
		engine.Group("/internal/v1"),
		corpus.NewHandler(corpus.NewService(store, nil), nil),
		"internal-token",
	)
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	src := voicegateway.NewHTTPCorpusSource(srv.URL, "internal-token", nil)
	candidates, err := src.CandidatesForUser(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("CandidatesForUser: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %+v, want exactly the caller's one block", candidates)
	}
	got := candidates[0]
	if got.ID != "block-1" || got.ExpressionEN != "Let's ship it." ||
		got.IntentZH != "推动上线" || got.AnchorUserSaid != "let's ship it" ||
		got.SceneTag != "review" || got.FunctionTag != "commit" {
		t.Fatalf("candidate lost fields on the wire: %+v", got)
	}
}

// A wrong token is refused by the real middleware, not by a fixture that
// happens to return 401.
func TestHTTPCorpusSource_RejectsBadToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	corpus.RegisterInternalRoutes(
		engine.Group("/internal/v1"),
		corpus.NewHandler(corpus.NewService(corpus.NewMemoryStore(), nil), nil),
		"internal-token",
	)
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	src := voicegateway.NewHTTPCorpusSource(srv.URL, "wrong-token", nil)
	if _, err := src.CandidatesForUser(context.Background(), "user-1"); err == nil {
		t.Fatal("expected an error for a bad internal token")
	}
}

func TestHTTPCorpusSource_UnconfiguredBaseURLFailsFast(t *testing.T) {
	src := voicegateway.NewHTTPCorpusSource("", "tok", nil)
	if _, err := src.CandidatesForUser(context.Background(), "user-1"); err == nil {
		t.Fatal("an unconfigured source must error rather than call anything")
	}
}
