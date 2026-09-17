package topic

import (
	"context"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func statsFixture(t *testing.T) (*Service, *MemoryStore, time.Time) {
	t.Helper()
	store := NewMemoryStore()
	svc := NewService(store, nil, nil)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	return svc, store, now
}

func seedStatsCard(t *testing.T, store *MemoryStore, id, userID string, createdAt time.Time) {
	t.Helper()
	day := utcDate(createdAt)
	if err := store.InsertCards(context.Background(), []Card{{
		ID: id, UserID: userID, ForDate: day, Title: "t", PromptEN: "en",
		CardType: CardTypePractice, ValidUntil: day.Add(24 * time.Hour),
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}}); err != nil {
		t.Fatalf("InsertCards: %v", err)
	}
}

func seedStatsCheckin(t *testing.T, store *MemoryStore, id, cardID, userID string, createdAt time.Time) {
	t.Helper()
	if err := store.InsertCheckin(context.Background(), Checkin{
		ID: id, CardID: cardID, UserID: userID, CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("InsertCheckin: %v", err)
	}
}

// 实战转化率 = 练到绿灯的表达里有多少真用出去了；打卡率 = 给出的卡有多少被行动。
func TestPracticeStats_CountsAndRates(t *testing.T) {
	svc, store, now := statsFixture(t)
	svc.SetBlockLookup(statsBlocks{
		{ID: "b1", State: corpus.StateAutomated, RealUseCount: 2},
		{ID: "b2", State: corpus.StateAutomated, RealUseCount: 0},
		{ID: "b3", State: corpus.StateTraining, RealUseCount: 1},
		{ID: "b4", State: corpus.StateNew, RealUseCount: 0},
	})
	seedStatsCard(t, store, "c1", "user-1", now.Add(-2*24*time.Hour))
	seedStatsCard(t, store, "c2", "user-1", now.Add(-1*24*time.Hour))
	seedStatsCheckin(t, store, "k1", "c1", "user-1", now.Add(-2*24*time.Hour))

	got, err := svc.PracticeStats(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("PracticeStats: %v", err)
	}
	if got.WindowDays != 30 || got.Checkins != 1 || got.CardsServed != 2 {
		t.Fatalf("counts = %+v", got)
	}
	if got.BlocksTotal != 4 || got.BlocksUsed != 2 {
		t.Fatalf("blocks = %+v", got)
	}
	if got.GreenBlocks != 2 || got.GreenUsed != 1 {
		t.Fatalf("green = %+v", got)
	}
	if got.ConversionRate != 0.5 {
		t.Fatalf("conversion = %v, want 1/2", got.ConversionRate)
	}
	if got.CheckinRate != 0.5 {
		t.Fatalf("checkin rate = %v, want 1/2", got.CheckinRate)
	}
}

// 还没有绿灯块时转化率是 0，而不是 NaN。
func TestPracticeStats_EmptyDenominatorsAreZero(t *testing.T) {
	svc, _, _ := statsFixture(t)
	svc.SetBlockLookup(statsBlocks{{ID: "b1", State: corpus.StateTraining, RealUseCount: 0}})

	got, err := svc.PracticeStats(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("PracticeStats: %v", err)
	}
	if got.ConversionRate != 0 || got.CheckinRate != 0 {
		t.Fatalf("rates = %+v", got)
	}
}

func TestPracticeStats_WindowExcludesOlderActivity(t *testing.T) {
	svc, store, now := statsFixture(t)
	svc.SetBlockLookup(statsBlocks{})
	seedStatsCard(t, store, "old", "user-1", now.Add(-60*24*time.Hour))
	seedStatsCard(t, store, "new", "user-1", now.Add(-1*24*time.Hour))
	seedStatsCheckin(t, store, "k-old", "old", "user-1", now.Add(-60*24*time.Hour))

	got, err := svc.PracticeStats(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("PracticeStats: %v", err)
	}
	if got.CardsServed != 1 || got.Checkins != 0 {
		t.Fatalf("window not applied: %+v", got)
	}

	wide, err := svc.PracticeStats(context.Background(), "user-1", 90)
	if err != nil {
		t.Fatalf("PracticeStats: %v", err)
	}
	if wide.CardsServed != 2 || wide.Checkins != 1 {
		t.Fatalf("wider window = %+v", wide)
	}
}

// 打卡是用户的判断，随账号一起被擦除；擦除后不再计入转化率。
func TestPracticeStats_WipedCheckinsExcluded(t *testing.T) {
	svc, store, now := statsFixture(t)
	svc.SetBlockLookup(statsBlocks{})
	seedStatsCard(t, store, "c1", "user-1", now.Add(-time.Hour))
	seedStatsCheckin(t, store, "k1", "c1", "user-1", now.Add(-time.Hour))

	if _, err := store.SoftDeleteAllForUser(context.Background(), "user-1", now); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	got, err := svc.PracticeStats(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("PracticeStats: %v", err)
	}
	if got.Checkins != 0 || got.CardsServed != 0 {
		t.Fatalf("wiped rows still counted: %+v", got)
	}
}

func TestPracticeStats_RequiresUser(t *testing.T) {
	svc, _, _ := statsFixture(t)
	if _, err := svc.PracticeStats(context.Background(), "  ", 30); err == nil {
		t.Fatal("missing user must be rejected")
	}
}

// statsBlocks is a BlockLookup over a fixed slice.
type statsBlocks []corpus.PhraseBlock

func (s statsBlocks) ListBlocks(context.Context, corpus.ListFilter) ([]corpus.PhraseBlock, error) {
	return []corpus.PhraseBlock(s), nil
}
