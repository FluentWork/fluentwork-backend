package corpus_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func newTestService(t *testing.T) *corpus.Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return corpus.NewService(corpus.NewMemoryStore(), logger)
}

func TestSourceAdapterReturnsCandidatesForUser(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.BatchAccept(ctx, "u1", corpus.BatchAcceptRequest{
		SourceSessionID: "sess-1",
		Blocks: []corpus.BatchAcceptBlock{
			{IntentZH: "推进", ExpressionEN: "ship it", AnchorUserSaid: "we should ship", SceneTag: "standup", FunctionTag: "propose"},
			{IntentZH: "总结", ExpressionEN: "let's wrap up", AnchorUserSaid: "any last thing", SceneTag: "review", FunctionTag: "summarize"},
		},
	})
	if err != nil {
		t.Fatalf("BatchAccept: %v", err)
	}

	adapter := corpus.NewBlockSourceAdapter(svc)
	got, err := adapter.CandidatesForUser(ctx, "u1")
	if err != nil {
		t.Fatalf("CandidatesForUser: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
	}
	if got[0].ExpressionEN != "ship it" {
		t.Fatalf("first candidate: %+v", got[0])
	}
	if got[0].IntentZH != "推进" || got[0].SceneTag != "standup" {
		t.Fatalf("first candidate missing fields: %+v", got[0])
	}
}

func TestSourceAdapterIsolatesUsers(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	mustAccept := func(user string, n int) {
		blocks := make([]corpus.BatchAcceptBlock, 0, n)
		for i := 0; i < n; i++ {
			blocks = append(blocks, corpus.BatchAcceptBlock{
				IntentZH:       "x",
				ExpressionEN:   "expr-" + user + "-" + string(rune('a'+i)),
				AnchorUserSaid: "anchor " + string(rune('a'+i)),
				SceneTag:       "casual",
				FunctionTag:    "object",
			})
		}
		if _, err := svc.BatchAccept(ctx, user, corpus.BatchAcceptRequest{
			SourceSessionID: "sess-" + user,
			Blocks:          blocks,
		}); err != nil {
			t.Fatalf("BatchAccept %s: %v", user, err)
		}
	}
	mustAccept("u1", 3)
	mustAccept("u2", 5)

	adapter := corpus.NewBlockSourceAdapter(svc)
	got, err := adapter.CandidatesForUser(ctx, "u1")
	if err != nil {
		t.Fatalf("CandidatesForUser: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("u1 candidates: got %d want 3", len(got))
	}
	for _, c := range got {
		if c.ID == "" || c.ExpressionEN == "" {
			t.Fatalf("missing required fields: %+v", c)
		}
	}
}

func TestSourceAdapterReturnsEmptyForUnknownUser(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	adapter := corpus.NewBlockSourceAdapter(svc)
	got, err := adapter.CandidatesForUser(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("CandidatesForUser: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates, want 0", len(got))
	}
}

func TestSourceAdapterNilServiceIsRejected(t *testing.T) {
	t.Parallel()
	var adapter *corpus.BlockSourceAdapter
	_, err := adapter.CandidatesForUser(context.Background(), "u1")
	if err == nil {
		t.Fatal("expected error from nil adapter")
	}
}

func TestSourceAdapterPropagatesServiceError(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	adapter := corpus.NewBlockSourceAdapter(svc)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := adapter.CandidatesForUser(ctx, "u1")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled wrapped, got %v", err)
	}
}
