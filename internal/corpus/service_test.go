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
	// Idempotence now comes from the expression, not from the unique key: the
	// same phrase is the same block, and the response says it was merged.
	if second.AcceptedCount != 0 || second.MergedCount != 1 || second.Items[0].ID != first.Items[0].ID {
		t.Fatalf("expected a merged result, got %+v", second)
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

func TestListBlocksKeywordMatchesAnchorUserSaid(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fixed := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }
	id := 0
	svc.newID = func() string {
		id++
		return "block-" + string(rune('0'+id))
	}

	if _, err := svc.BatchAccept(context.Background(), "user-1", BatchAcceptRequest{
		SourceSessionID: "session-1",
		Blocks: []BatchAcceptBlock{{
			IntentZH:       "说明阻塞",
			ExpressionEN:   "I'm blocked on the API review.",
			AnchorUserSaid: "I am blocked on the API review.",
			SceneTag:       "standup",
			FunctionTag:    "report",
		}},
	}); err != nil {
		t.Fatalf("BatchAccept: %v", err)
	}

	matched, err := svc.ListBlocks(context.Background(), ListBlocksRequest{
		UserID:  "user-1",
		Keyword: "blocked on the API review",
	})
	if err != nil {
		t.Fatalf("ListBlocks matched: %v", err)
	}
	if len(matched.Items) != 1 {
		t.Fatalf("expected keyword match on anchor_user_said, got %+v", matched)
	}

	missed, err := svc.ListBlocks(context.Background(), ListBlocksRequest{
		UserID:  "user-1",
		Keyword: "touch base",
	})
	if err != nil {
		t.Fatalf("ListBlocks missed: %v", err)
	}
	if len(missed.Items) != 0 {
		t.Fatalf("expected no match, got %+v", missed)
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

func TestListBlocksIncrementalIncludesDeletionTombstone(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	times := []time.Time{
		time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 31, 9, 1, 0, 0, time.UTC),
		time.Date(2026, 8, 31, 9, 2, 0, 0, time.UTC),
	}
	idx := 0
	svc.now = func() time.Time {
		current := times[idx]
		if idx < len(times)-1 {
			idx++
		}
		return current
	}
	id := 0
	svc.newID = func() string {
		id++
		return "block-" + string(rune('0'+id))
	}

	created, err := svc.BatchAccept(context.Background(), "user-1", BatchAcceptRequest{
		SourceSessionID: "session-1",
		Blocks: []BatchAcceptBlock{{
			IntentZH:       "说明阻塞",
			ExpressionEN:   "I'm blocked on the API review.",
			AnchorUserSaid: "I am blocked on the API review.",
			SceneTag:       "standup",
			FunctionTag:    "report",
		}},
	})
	if err != nil {
		t.Fatalf("BatchAccept: %v", err)
	}
	blockID := created.Items[0].ID

	if err := svc.DeleteBlock(context.Background(), "user-1", blockID); err != nil {
		t.Fatalf("DeleteBlock: %v", err)
	}

	delta, err := svc.ListBlocks(context.Background(), ListBlocksRequest{
		UserID:       "user-1",
		UpdatedAfter: times[0].Format(time.RFC3339Nano),
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("ListBlocks delta: %v", err)
	}
	if len(delta.Items) != 1 {
		t.Fatalf("expected one tombstone row, got %+v", delta)
	}
	if delta.Items[0].ID != blockID || delta.Items[0].DeletedAt == nil {
		t.Fatalf("expected deleted block tombstone, got %+v", delta.Items[0])
	}
	if delta.CursorReset {
		t.Fatalf("expected cursor_reset=false, got %+v", delta)
	}
}

func TestListBlocksIncrementalRejectsBrowseFilters(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := svc.ListBlocks(context.Background(), ListBlocksRequest{
		UserID:       "user-1",
		UpdatedAfter: time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		FavoriteOnly: true,
	})
	if err == nil {
		t.Fatal("expected invalid argument error")
	}
}

func TestListBlocksIncrementalSupportsCursorPagination(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	times := []time.Time{
		time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 31, 9, 1, 0, 0, time.UTC),
	}
	idx := 0
	svc.now = func() time.Time {
		current := times[idx]
		if idx < len(times)-1 {
			idx++
		}
		return current
	}
	id := 0
	svc.newID = func() string {
		id++
		return "block-" + string(rune('0'+id))
	}

	for _, block := range []BatchAcceptBlock{
		{IntentZH: "说明下一步", ExpressionEN: "I'll follow up tomorrow.", AnchorUserSaid: "I will follow up tomorrow.", SceneTag: "standup", FunctionTag: "commit"},
		{IntentZH: "说明阻塞", ExpressionEN: "I'm blocked on the API review.", AnchorUserSaid: "I am blocked on the API review.", SceneTag: "standup", FunctionTag: "report"},
	} {
		if _, err := svc.BatchAccept(context.Background(), "user-1", BatchAcceptRequest{SourceSessionID: "session-1", Blocks: []BatchAcceptBlock{block}}); err != nil {
			t.Fatalf("BatchAccept: %v", err)
		}
	}

	first, err := svc.ListBlocks(context.Background(), ListBlocksRequest{
		UserID:       "user-1",
		UpdatedAfter: time.Date(2026, 8, 31, 8, 59, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Limit:        1,
	})
	if err != nil {
		t.Fatalf("ListBlocks first delta page: %v", err)
	}
	if len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("unexpected first delta page: %+v", first)
	}

	second, err := svc.ListBlocks(context.Background(), ListBlocksRequest{
		UserID:       "user-1",
		UpdatedAfter: time.Date(2026, 8, 31, 8, 59, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Cursor:       first.NextCursor,
		Limit:        1,
	})
	if err != nil {
		t.Fatalf("ListBlocks second delta page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("unexpected second delta page: %+v", second)
	}
}

// 86_ M5: the same phrase refined from a *different* session is the same asset,
// not a near-duplicate. Measured on the flow eval at 15% of refined expressions.
func TestBatchAccept_MergesAcrossSessions(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC) }

	block := BatchAcceptBlock{
		IntentZH:       "说明卡点",
		ExpressionEN:   "The deploy is blocked on the migration.",
		AnchorUserSaid: "the deploy is waiting",
		SceneTag:       "standup",
		FunctionTag:    "report",
	}
	first, err := svc.BatchAccept(context.Background(), "user-1", BatchAcceptRequest{
		SourceSessionID: "session-1", Blocks: []BatchAcceptBlock{block},
	})
	if err != nil {
		t.Fatalf("first accept: %v", err)
	}

	// A later session refines the same sentence with different punctuation and
	// casing: same block.
	second, err := svc.BatchAccept(context.Background(), "user-1", BatchAcceptRequest{
		SourceSessionID: "session-2",
		Blocks: []BatchAcceptBlock{{
			IntentZH:       "说明卡点（重述）",
			ExpressionEN:   "the deploy is blocked on the migration",
			AnchorUserSaid: "deploy blocked because migration",
			SceneTag:       "standup",
			FunctionTag:    "report",
		}},
	})
	if err != nil {
		t.Fatalf("second accept: %v", err)
	}
	if second.AcceptedCount != 0 || second.MergedCount != 1 {
		t.Fatalf("expected a merge, got %+v", second)
	}
	if second.Items[0].ID != first.Items[0].ID {
		t.Fatalf("merge must return the existing block: %+v", second.Items[0])
	}

	blocks, err := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1"})
	if err != nil {
		t.Fatalf("ListBlocks: %v", err)
	}
	if len(blocks.Items) != 1 {
		t.Fatalf("corpus = %d blocks, want the phrase stored once", len(blocks.Items))
	}

	// A different phrase is still a different asset.
	third, err := svc.BatchAccept(context.Background(), "user-1", BatchAcceptRequest{
		SourceSessionID: "session-3",
		Blocks: []BatchAcceptBlock{{
			IntentZH: "另一个表达", ExpressionEN: "I'll touch base with the team.",
			AnchorUserSaid: "sync up", SceneTag: "standup", FunctionTag: "commit",
		}},
	})
	if err != nil {
		t.Fatalf("third accept: %v", err)
	}
	if third.AcceptedCount != 1 || third.MergedCount != 0 {
		t.Fatalf("a distinct phrase must be accepted: %+v", third)
	}
}

func TestNormalizeExpression(t *testing.T) {
	same := []string{
		"The deploy is blocked on the migration.",
		"the deploy is blocked on the migration",
		"  The deploy, is blocked on the migration!  ",
	}
	want := NormalizeExpression(same[0])
	for _, variant := range same[1:] {
		if got := NormalizeExpression(variant); got != want {
			t.Errorf("NormalizeExpression(%q) = %q, want %q", variant, got, want)
		}
	}
	if NormalizeExpression("The deploy is blocked") == want {
		t.Error("different sentences must not normalise together")
	}
}
