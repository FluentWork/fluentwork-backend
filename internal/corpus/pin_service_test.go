package corpus

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

func newPinService(t *testing.T) (*Service, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fixed := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	n := 0
	svc.now = func() time.Time {
		defer func() { n++ }()
		return fixed.Add(time.Duration(n) * time.Second)
	}
	id := 0
	svc.newID = func() string {
		id++
		return "block-" + string(rune('0'+id))
	}
	return svc, store
}

func acceptN(t *testing.T, svc *Service, n int) []PhraseBlockView {
	t.Helper()
	out := make([]PhraseBlockView, 0, n)
	for i := 0; i < n; i++ {
		resp, err := svc.BatchAccept(context.Background(), "user-1", BatchAcceptRequest{
			SourceSessionID: "session-1",
			Blocks: []BatchAcceptBlock{{
				IntentZH:       "意图" + string(rune('A'+i)),
				ExpressionEN:   "Ship it " + string(rune('A'+i)),
				AnchorUserSaid: "ship it " + string(rune('A'+i)),
				SceneTag:       "review",
				FunctionTag:    "commit",
			}},
		})
		if err != nil {
			t.Fatalf("BatchAccept: %v", err)
		}
		out = append(out, resp.Items[0])
	}
	return out
}

func TestService_UpdatePin_Owner(t *testing.T) {
	svc, _ := newPinService(t)
	blocks := acceptN(t, svc, 1)
	got, err := svc.UpdatePin(context.Background(), "user-1", blocks[0].ID, true)
	if err != nil {
		t.Fatalf("UpdatePin: %v", err)
	}
	if got.PinnedAt == nil {
		t.Fatal("expected pinned_at")
	}
	unpinned, err := svc.UpdatePin(context.Background(), "user-1", blocks[0].ID, false)
	if err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if unpinned.PinnedAt != nil {
		t.Fatalf("expected cleared pin: %+v", unpinned)
	}
}

func TestService_UpdatePin_CrossUser(t *testing.T) {
	svc, _ := newPinService(t)
	blocks := acceptN(t, svc, 1)
	_, err := svc.UpdatePin(context.Background(), "user-2", blocks[0].ID, true)
	if err == nil {
		t.Fatal("expected cross-user error")
	}
	ae, ok := err.(*apierr.Error)
	if !ok || ae.HTTPStatus != 403 {
		t.Fatalf("err = %v", err)
	}
}

func TestService_UpdateFavorite_DoesNotClearPin(t *testing.T) {
	svc, _ := newPinService(t)
	blocks := acceptN(t, svc, 1)
	if _, err := svc.UpdatePin(context.Background(), "user-1", blocks[0].ID, true); err != nil {
		t.Fatalf("UpdatePin: %v", err)
	}
	got, err := svc.UpdateFavorite(context.Background(), "user-1", blocks[0].ID, true)
	if err != nil {
		t.Fatalf("UpdateFavorite: %v", err)
	}
	if !got.IsFavorite || got.PinnedAt == nil {
		t.Fatalf("expected both flags: %+v", got)
	}
	unfav, err := svc.UpdateFavorite(context.Background(), "user-1", blocks[0].ID, false)
	if err != nil {
		t.Fatalf("unfavorite: %v", err)
	}
	if unfav.IsFavorite || unfav.PinnedAt == nil {
		t.Fatalf("unfavorite should keep pin: %+v", unfav)
	}
}

func TestListBlocks_PinnedThenFavoriteThenUpdated(t *testing.T) {
	svc, _ := newPinService(t)
	blocks := acceptN(t, svc, 3)
	if _, err := svc.UpdateFavorite(context.Background(), "user-1", blocks[0].ID, true); err != nil {
		t.Fatalf("favorite: %v", err)
	}
	if _, err := svc.UpdatePin(context.Background(), "user-1", blocks[1].ID, true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	list, err := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1"})
	if err != nil {
		t.Fatalf("ListBlocks: %v", err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("len=%d", len(list.Items))
	}
	if list.Items[0].ID != blocks[1].ID {
		t.Fatalf("pinned should be first, got %+v", list.Items[0])
	}
	if list.Items[1].ID != blocks[0].ID {
		t.Fatalf("favorite should be second, got %+v", list.Items[1])
	}
	pinnedOnly, err := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1", PinnedOnly: true})
	if err != nil {
		t.Fatalf("pinned_only: %v", err)
	}
	if len(pinnedOnly.Items) != 1 || pinnedOnly.Items[0].ID != blocks[1].ID {
		t.Fatalf("pinned_only = %+v", pinnedOnly.Items)
	}
}

func TestListBlocks_CursorKeepsPinnedOnFirstPage(t *testing.T) {
	svc, _ := newPinService(t)
	blocks := acceptN(t, svc, 3)
	page1, err := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1", Limit: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if page1.NextCursor == "" {
		t.Fatal("expected cursor")
	}
	oldest := blocks[0]
	if _, err := svc.UpdatePin(context.Background(), "user-1", oldest.ID, true); err != nil {
		t.Fatalf("pin oldest: %v", err)
	}
	fromStart, err := svc.ListBlocks(context.Background(), ListBlocksRequest{UserID: "user-1", Limit: 2})
	if err != nil {
		t.Fatalf("from start: %v", err)
	}
	if fromStart.Items[0].ID != oldest.ID {
		t.Fatalf("pinned oldest should lead page 1, got %+v", fromStart.Items)
	}
}
