package corpus

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestBatchAcceptIsIdempotentAndSupportsSoftDeleteRecovery(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fixed := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }
	svc.newID = func() string { return "block-1" }

	req := BatchAcceptRequest{
		SourceSessionID: "session-1",
		Blocks: []BatchAcceptBlock{{
			IntentZH:       "说明下一步",
			ExpressionEN:   "I'll follow up tomorrow.",
			AnchorUserSaid: "I will follow up tomorrow.",
			SceneTag:       "standup",
			FunctionTag:    "commit",
		}},
	}

	first, err := svc.BatchAccept(context.Background(), "user-1", req)
	if err != nil {
		t.Fatalf("BatchAccept first: %v", err)
	}
	if first.AcceptedCount != 1 || len(first.Items) != 1 {
		t.Fatalf("unexpected first response: %+v", first)
	}

	second, err := svc.BatchAccept(context.Background(), "user-1", req)
	if err != nil {
		t.Fatalf("BatchAccept second: %v", err)
	}
	if second.AcceptedCount != 1 || second.Items[0].ID != first.Items[0].ID {
		t.Fatalf("expected idempotent result, got %+v", second)
	}

	if err := svc.DeleteBlock(context.Background(), "user-1", first.Items[0].ID); err != nil {
		t.Fatalf("DeleteBlock: %v", err)
	}

	third, err := svc.BatchAccept(context.Background(), "user-1", req)
	if err != nil {
		t.Fatalf("BatchAccept third: %v", err)
	}
	if third.Items[0].ID != first.Items[0].ID {
		t.Fatalf("expected soft-deleted block revival, got %+v", third)
	}
}

func TestListBlocksSupportsFavoriteAndCursor(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fixed := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	calls := 0
	svc.now = func() time.Time {
		defer func() { calls++ }()
		return fixed.Add(time.Duration(calls) * time.Minute)
	}
	id := 0
	svc.newID = func() string {
		id++
		return "block-" + string(rune('0'+id))
	}

	for _, block := range []BatchAcceptBlock{
		{IntentZH: "同步计划", ExpressionEN: "I'll touch base tomorrow.", AnchorUserSaid: "I will sync tomorrow.", SceneTag: "standup", FunctionTag: "commit"},
		{IntentZH: "说明阻塞", ExpressionEN: "I'm blocked on the API review.", AnchorUserSaid: "I am blocked on the API review.", SceneTag: "standup", FunctionTag: "report"},
	} {
		if _, err := svc.BatchAccept(context.Background(), "user-1", BatchAcceptRequest{SourceSessionID: "session-1", Blocks: []BatchAcceptBlock{block}}); err != nil {
			t.Fatalf("BatchAccept: %v", err)
		}
	}

	list, err := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1", Limit: 1})
	if err != nil {
		t.Fatalf("ListBlocks: %v", err)
	}
	if len(list.Items) != 1 || list.NextCursor == "" {
		t.Fatalf("unexpected first page: %+v", list)
	}

	favorited, err := svc.SetFavorite(context.Background(), "user-1", list.Items[0].ID, FavoriteBlockRequest{IsFavorite: true, Pinned: true})
	if err != nil {
		t.Fatalf("SetFavorite: %v", err)
	}
	if !favorited.IsFavorite || favorited.PinnedAt == nil {
		t.Fatalf("unexpected favorite block: %+v", favorited)
	}

	next, err := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1", Cursor: list.NextCursor, Limit: 1})
	if err != nil {
		t.Fatalf("ListBlocks next: %v", err)
	}
	if len(next.Items) != 1 {
		t.Fatalf("unexpected second page: %+v", next)
	}

	favOnly, err := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1", FavoriteOnly: true})
	if err != nil {
		t.Fatalf("ListBlocks favorite only: %v", err)
	}
	if len(favOnly.Items) != 1 || favOnly.Items[0].ID != favorited.ID {
		t.Fatalf("unexpected favorite-only list: %+v", favOnly)
	}
}

func TestReassignerMovesGuestBlocks(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fixed := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }
	svc.newID = func() string { return "block-1" }

	if _, err := svc.BatchAccept(context.Background(), "guest-1", BatchAcceptRequest{
		SourceSessionID: "session-1",
		Blocks: []BatchAcceptBlock{{
			IntentZH:       "说明下一步",
			ExpressionEN:   "I'll follow up tomorrow.",
			AnchorUserSaid: "I will follow up tomorrow.",
			SceneTag:       "standup",
			FunctionTag:    "commit",
		}},
	}); err != nil {
		t.Fatalf("BatchAccept: %v", err)
	}

	if err := (Reassigner{Store: store}).ReassignFromGuest(context.Background(), "guest-1", "user-1"); err != nil {
		t.Fatalf("ReassignFromGuest: %v", err)
	}

	list, err := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1"})
	if err != nil {
		t.Fatalf("ListBlocks after reassign: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected reassigned block, got %+v", list)
	}
}
