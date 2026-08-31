package content

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func TestGenerateDailyReadUsesCorpusBlocks(t *testing.T) {
	generated := GenerateDailyRead([]SourceBlock{{
		ID:           "block-1",
		IntentZH:     "说明阻塞",
		ExpressionEN: "I'm blocked on the API review.",
	}})
	if generated.Generator != GeneratorCorpusStub {
		t.Fatalf("generator = %q", generated.Generator)
	}
	if generated.Title == "" || generated.Body == "" {
		t.Fatalf("unexpected generated payload: %+v", generated)
	}
}

func TestGetTodayUsesPresetWhenCorpusEmpty(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fixed := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }
	svc.newID = func() string { return "read-1" }

	first, err := svc.GetToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetToday first: %v", err)
	}
	if first.Status != StatusReady || first.DailyRead == nil || first.DailyRead.Generator != GeneratorPreset {
		t.Fatalf("unexpected first response: %+v", first)
	}

	second, err := svc.GetToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetToday second: %v", err)
	}
	if second.DailyRead == nil || second.DailyRead.ID != first.DailyRead.ID {
		t.Fatalf("expected idempotent today read, got %+v", second)
	}
}

func TestGetTodayUsesCorpusBlocksWhenAvailable(t *testing.T) {
	corpusStore := corpus.NewMemoryStore()
	blockSource := CorpusBlockSource{Store: corpusStore}
	contentStore := NewMemoryStore()
	svc := NewService(contentStore, blockSource, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fixed := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }
	svc.newID = func() string { return "read-2" }

	_, err := corpusSvcBatchAccept(corpusStore, fixed, "user-1")
	if err != nil {
		t.Fatalf("batch accept: %v", err)
	}

	resp, err := svc.GetToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetToday: %v", err)
	}
	if resp.DailyRead == nil || resp.DailyRead.Generator != GeneratorCorpusStub {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestFollowReadDoesNotScore(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fixed := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }
	svc.newID = func() string { return "read-3" }

	today, err := svc.GetToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetToday: %v", err)
	}

	resp, err := svc.FollowRead(context.Background(), "user-1", today.DailyRead.ID, FollowReadRequest{})
	if err != nil {
		t.Fatalf("FollowRead: %v", err)
	}
	if !resp.Recorded || resp.Score != nil || resp.Generator != GeneratorFollowRead {
		t.Fatalf("unexpected follow read response: %+v", resp)
	}
}

func TestReassignerMovesGuestDailyReads(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fixed := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }
	svc.newID = func() string { return "read-4" }

	if _, err := svc.GetToday(context.Background(), "guest-1"); err != nil {
		t.Fatalf("GetToday guest: %v", err)
	}
	if err := (Reassigner{Store: store}).ReassignFromGuest(context.Background(), "guest-1", "user-1"); err != nil {
		t.Fatalf("ReassignFromGuest: %v", err)
	}
	got, err := svc.GetToday(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetToday user: %v", err)
	}
	if got.DailyRead == nil {
		t.Fatalf("expected reassigned daily read, got %+v", got)
	}
}

func corpusSvcBatchAccept(store corpus.Store, now time.Time, userID string) ([]corpus.PhraseBlock, error) {
	return store.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID:             "block-1",
		UserID:         userID,
		IntentZH:       "说明阻塞",
		ExpressionEN:   "I'm blocked on the API review.",
		AnchorUserSaid: "I am blocked on the API review.",
		SceneTag:       "standup",
		FunctionTag:    "report",
		State:          corpus.StateNew,
		NextDueAt:      now,
		EaseFactor:     2.5,
		CreatedAt:      now,
		UpdatedAt:      now,
	}})
}
