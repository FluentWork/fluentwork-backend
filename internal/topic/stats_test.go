package topic

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"

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

// stubLedger records what the checkin asked to credit.
type stubLedger struct {
	calls    []string
	credited int
	sources  map[string]int
	err      error
}

func (s *stubLedger) RecordRealUse(_ context.Context, _ string, blockIDs []string, source, refID string) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.calls = append(s.calls, source+":"+refID)
	s.credited = len(blockIDs)
	return len(blockIDs), nil
}

func (s *stubLedger) CountRealUsesBySource(context.Context, string, time.Time) (map[string]int, error) {
	if s.sources == nil {
		return map[string]int{}, nil
	}
	return s.sources, nil
}

// 86_ M10: the learner ticking "I used these" is the product's only first-hand
// evidence of practice turning into speech.
func TestCheckin_CreditsTheBlocksTheLearnerUsed(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	svc, gen, store := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return day }
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	cards, err := store.ListTodayCards(context.Background(), "u1", day)
	if err != nil || len(cards) == 0 {
		t.Fatalf("cards = %+v err=%v", cards, err)
	}
	card := cards[0]
	if len(card.BlockIDs) < 2 {
		t.Fatalf("fixture card must offer at least two blocks: %+v", card.BlockIDs)
	}
	ledger := &stubLedger{}
	svc.SetRealUseLedger(ledger)

	used := append([]string{}, card.BlockIDs[:2]...)
	used = append(used, "block-from-nowhere")
	result, err := svc.Checkin(context.Background(), "u1", card.ID, CheckinRequest{UsedBlockIDs: used})
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}
	if result.RecordedUse != 2 {
		t.Fatalf("recorded use = %d, want the two the card offered", result.RecordedUse)
	}
	if len(result.IgnoredBlockIDs) != 1 || result.IgnoredBlockIDs[0] != "block-from-nowhere" {
		t.Fatalf("unoffered ids must be reported back: %+v", result.IgnoredBlockIDs)
	}
	if len(ledger.calls) != 1 || ledger.calls[0] != "checkin:"+result.CheckinID {
		t.Fatalf("ledger calls = %+v, want one checkin credit keyed by the checkin", ledger.calls)
	}
	if result.StreakDays != 1 {
		t.Fatalf("streak = %d", result.StreakDays)
	}
}

// A checkin without the ledger, or without a used-block list, still records the
// checkin: the credit is an add-on, never a precondition.
func TestCheckin_WorksWithoutUsedBlocksOrLedger(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	svc, gen, store := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return day }
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	cards, _ := store.ListTodayCards(context.Background(), "u1", day)

	noLedger, err := svc.Checkin(context.Background(), "u1", cards[0].ID, CheckinRequest{})
	if err != nil || noLedger.RecordedUse != 0 || noLedger.StreakDays != 1 {
		t.Fatalf("checkin without ledger = %+v err=%v", noLedger, err)
	}

	svc.SetRealUseLedger(&stubLedger{})
	withBlocks, err := svc.Checkin(context.Background(), "u1", cards[1].ID, CheckinRequest{
		UsedBlockIDs: []string{cards[1].BlockIDs[0]},
	})
	if err != nil || withBlocks.RecordedUse != 1 {
		t.Fatalf("checkin with blocks = %+v err=%v", withBlocks, err)
	}
}

// A credit failure must not lose the checkin: the streak is already recorded.
func TestCheckin_CreditFailureStillSucceeds(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	svc, gen, store := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return day }
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	cards, _ := store.ListTodayCards(context.Background(), "u1", day)
	svc.SetRealUseLedger(&stubLedger{err: errors.New("ledger down")})

	result, err := svc.Checkin(context.Background(), "u1", cards[0].ID, CheckinRequest{
		UsedBlockIDs: []string{cards[0].BlockIDs[0]},
	})
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}
	if result.StreakDays != 1 || result.CheckinID == "" {
		t.Fatalf("checkin must survive a credit failure: %+v", result)
	}
	if result.RecordedUse != 0 {
		t.Fatalf("recorded use = %d, want 0 when the ledger failed", result.RecordedUse)
	}
}

// The stats endpoint reports the two kinds of evidence apart (86_ M9).
func TestPracticeStats_SplitsRealUsesBySource(t *testing.T) {
	svc, store, now := statsFixture(t)
	svc.SetBlockLookup(statsBlocks{})
	svc.SetRealUseLedger(&stubLedger{sources: map[string]int{"hit": 3, "checkin": 2}})
	seedStatsCard(t, store, "c1", "user-1", now.Add(-time.Hour))

	got, err := svc.PracticeStats(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("PracticeStats: %v", err)
	}
	if got.RealUsesHit != 3 || got.RealUsesCheckin != 2 {
		t.Fatalf("split = %d/%d, want 3/2", got.RealUsesHit, got.RealUsesCheckin)
	}
}

// 86_ M11: the only negative signal about the last mile. It must be as easy to
// record as a checkin, and it must not punish anybody.
func TestDismiss_RecordsReasonAndIsConsequenceFree(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	svc, gen, store := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return day }
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	cards, _ := store.ListTodayCards(context.Background(), "u1", day)
	if len(cards) == 0 {
		t.Fatal("no cards")
	}

	result, err := svc.Dismiss(context.Background(), "u1", cards[0].ID, DismissNoPartner)
	if err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if result.AlreadyDismissed || result.Reason != DismissNoPartner {
		t.Fatalf("result = %+v", result)
	}
	stored, err := store.GetCard(context.Background(), cards[0].ID)
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if stored.DismissedAt == nil || stored.DismissReason != DismissNoPartner {
		t.Fatalf("card = %+v", stored)
	}
	// No penalty: the streak table is untouched by a dismissal.
	if _, err := store.GetStreak(context.Background(), "u1"); err != nil {
		t.Fatalf("GetStreak: %v", err)
	}

	// A repeat reports what happened rather than overwriting the first reason.
	again, err := svc.Dismiss(context.Background(), "u1", cards[0].ID, DismissNoTime)
	if err != nil {
		t.Fatalf("second dismiss: %v", err)
	}
	if !again.AlreadyDismissed {
		t.Fatalf("second dismiss = %+v", again)
	}
	stored, _ = store.GetCard(context.Background(), cards[0].ID)
	if stored.DismissReason != DismissNoPartner {
		t.Fatalf("the first reason must win: %q", stored.DismissReason)
	}
}

func TestDismiss_RejectsBadInputAndForeignCards(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	svc, gen, store := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return day }
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	cards, _ := store.ListTodayCards(context.Background(), "u1", day)

	var apiErr *apierr.Error
	cases := []struct {
		name   string
		user   string
		card   string
		reason string
		want   int
	}{
		{"missing user", " ", cards[0].ID, DismissNoPartner, 401},
		{"missing card", "u1", " ", DismissNoPartner, 400},
		{"open reason set", "u1", cards[0].ID, "meh", 400},
		{"unknown card", "u1", "nope", DismissNoPartner, 404},
		{"another learner's card", "u2", cards[0].ID, DismissNoPartner, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Dismiss(context.Background(), tc.user, tc.card, tc.reason)
			if !errors.As(err, &apiErr) || apiErr.HTTPStatus != tc.want {
				t.Fatalf("err = %v, want %d", err, tc.want)
			}
		})
	}
}

// The distribution is the deliverable: it decides where the last mile breaks.
func TestPracticeStats_ReportsDismissReasons(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	svc, gen, store := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return day }
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	cards, _ := store.ListTodayCards(context.Background(), "u1", day)
	if len(cards) < 2 {
		t.Fatalf("need two cards, got %d", len(cards))
	}
	if _, err := svc.Dismiss(context.Background(), "u1", cards[0].ID, DismissNoPartner); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Dismiss(context.Background(), "u1", cards[1].ID, DismissNoPartner); err != nil {
		t.Fatal(err)
	}

	stats, err := svc.PracticeStats(context.Background(), "u1", 30)
	if err != nil {
		t.Fatalf("PracticeStats: %v", err)
	}
	if stats.DismissReasons[DismissNoPartner] != 2 {
		t.Fatalf("dismiss reasons = %+v", stats.DismissReasons)
	}
	if stats.CardsServed != len(cards) || stats.DismissRate != 2.0/float64(len(cards)) {
		t.Fatalf("rate = %v over %d cards", stats.DismissRate, stats.CardsServed)
	}
}
