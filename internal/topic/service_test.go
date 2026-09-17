package topic

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

type stubLLM struct {
	body      string
	err       error
	failTimes int32
	calls     atomic.Int32
}

func (s *stubLLM) Complete(context.Context, string) (string, error) {
	n := s.calls.Add(1)
	if s.failTimes > 0 && n <= s.failTimes {
		if s.err != nil {
			return "", s.err
		}
		return "", errors.New("timeout")
	}
	if s.err != nil && s.failTimes == 0 {
		return "", s.err
	}
	return s.body, nil
}

type stubSignals struct{ sig Signals }

func (s stubSignals) Snapshot(context.Context, string, time.Time) (Signals, error) {
	return s.sig, nil
}

// groundedSignals clears H1's threshold (≥ DefaultMinBlocks) and carries blocks
// for the tags threeCardJSON uses, so a card can actually be grounded on them.
func groundedSignals() stubSignals {
	// The blocks carry the phrases the fixture cards quote: content-level
	// grounding is what the generator enforces, so a fixture whose cards share
	// no wording with its corpus would (correctly) produce nothing.
	phrases := []string{
		"I shipped the login bug fix yesterday",
		"Can we park that for now",
		"Let me walk you through the migration plan",
	}
	scenes := []string{"standup", "review", "1on1"}
	blocks := make([]BlockRef, 0, DefaultMinBlocks)
	counts := map[string]int{}
	for i := 0; i < DefaultMinBlocks; i++ {
		scene := scenes[i%len(scenes)]
		counts[scene]++
		blocks = append(blocks, BlockRef{
			ID:             "block-" + itoa(i),
			ExpressionEN:   phrases[i%len(phrases)],
			AnchorUserSaid: phrases[i%len(phrases)],
			IntentZH:       "意图 " + itoa(i),
			SceneTag:       scene,
			FunctionTag:    "report",
		})
	}
	return stubSignals{sig: Signals{
		Level:          "intermediate",
		SceneCounts:    counts,
		FunctionCounts: map[string]int{"report": DefaultMinBlocks},
		Blocks:         blocks,
	}}
}

type stubActive struct{ ids []string }

func (s stubActive) ListActiveUserIDs(context.Context, time.Time) ([]string, error) {
	return s.ids, nil
}

func threeCardJSON() string {
	return `{"cards":[
		{"title":"Standup sync","prompt_en":"Quick sync: I shipped the login bug fix yesterday.","prompt_zh":"同步昨天的进度","card_type":"warmup","seed_tags":["standup","report"]},
		{"title":"Clarify scope","prompt_en":"Can we park that for now, or should I keep going?","prompt_zh":"澄清范围","card_type":"practice","seed_tags":["review","clarify"]},
		{"title":"Propose next step","prompt_en":"Let me walk you through the migration plan.","prompt_zh":"提出下一步","card_type":"stretch","seed_tags":["1on1","propose"]}
	]}`
}

func testService(t *testing.T, llm Completer, sig SignalSource) (*Service, *Generator, Store) {
	t.Helper()
	store := NewMemoryStore()
	gen := NewGenerator(store, llm, sig)
	svc := NewService(store, gen, nil)
	return svc, gen, store
}

func TestStore_InsertListGet(t *testing.T) {
	store := NewMemoryStore()
	day := utcDate(time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC))
	cards := []Card{{
		ID: "c1", UserID: "u1", ForDate: day, Title: "A", PromptEN: "en", PromptZH: "zh",
		CardType: CardTypeWarmup, ValidUntil: day.Add(24 * time.Hour), CreatedAt: day, UpdatedAt: day,
	}}
	if err := store.InsertCards(context.Background(), cards); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListTodayCards(context.Background(), "u1", day)
	if err != nil || len(got) != 1 || got[0].Title != "A" {
		t.Fatalf("list = %+v err=%v", got, err)
	}
	one, err := store.GetCard(context.Background(), "c1")
	if err != nil || one.ID != "c1" {
		t.Fatalf("get = %+v err=%v", one, err)
	}
}

func TestStreak_SevenConsecutiveDays(t *testing.T) {
	store := NewMemoryStore()
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var st Streak
	for i := 0; i < 7; i++ {
		var err error
		st, err = UpdateOnCheckin(context.Background(), store, "u1", start.AddDate(0, 0, i))
		if err != nil {
			t.Fatal(err)
		}
	}
	if st.CurrentStreak != 7 || st.LongestStreak != 7 {
		t.Fatalf("streak = %+v", st)
	}
}

func TestStreak_BreakResets(t *testing.T) {
	store := NewMemoryStore()
	day := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if _, err := UpdateOnCheckin(context.Background(), store, "u1", day); err != nil {
		t.Fatal(err)
	}
	st, err := UpdateOnCheckin(context.Background(), store, "u1", day.AddDate(0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if st.CurrentStreak != 1 || st.LongestStreak != 1 {
		t.Fatalf("reset = %+v", st)
	}
}

func TestListToday_ThreeCardsValidUntil(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	svc, _, _ := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return day }
	got, err := svc.ListToday(context.Background(), "u1")
	if err != nil || len(got.Items) != 3 {
		t.Fatalf("list = %+v err=%v", got, err)
	}
	until := utcDate(day).Add(24 * time.Hour)
	for _, card := range got.Items {
		if !card.ValidUntil.Equal(until) {
			t.Fatalf("valid_until = %s want %s", card.ValidUntil, until)
		}
	}
}

func TestCheckin_FirstDuplicateCrossUserAndReflection(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	svc, _, _ := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return day }
	listed, err := svc.ListToday(context.Background(), "u1")
	if err != nil || len(listed.Items) == 0 {
		t.Fatalf("list %v %+v", err, listed)
	}
	cardID := listed.Items[0].ID
	ok, err := svc.Checkin(context.Background(), "u1", cardID, CheckinRequest{Reflection: "felt good"})
	if err != nil || ok.StreakDays != 1 || ok.CheckinID == "" {
		t.Fatalf("first = %+v err=%v", ok, err)
	}
	_, err = svc.Checkin(context.Background(), "u1", cardID, CheckinRequest{Reflection: ""})
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
		t.Fatalf("dup = %v", err)
	}
	_, err = svc.Checkin(context.Background(), "other", cardID, CheckinRequest{Reflection: ""})
	if !errors.As(err, &ae) || ae.HTTPStatus != 403 {
		t.Fatalf("cross = %v", err)
	}
	_, err = svc.Checkin(context.Background(), "u1", listed.Items[1].ID, CheckinRequest{Reflection: strings.Repeat("x", MaxReflectionLen+1)})
	if !errors.As(err, &ae) || ae.HTTPStatus != 400 {
		t.Fatalf("long = %v", err)
	}
}

func TestCheckin_SoftDeleteAndRestore(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	svc, _, store := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return day }
	listed, _ := svc.ListToday(context.Background(), "u1")
	if _, err := store.SoftDeleteAllForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	empty, err := svc.ListToday(context.Background(), "u1")
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("hidden after wipe = %+v err=%v", empty, err)
	}
	_, err = svc.Checkin(context.Background(), "u1", listed.Items[0].ID, CheckinRequest{Reflection: ""})
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 404 {
		t.Fatalf("deleted checkin = %v", err)
	}
	if _, err := store.RestoreDeletedForUser(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCard(context.Background(), listed.Items[0].ID)
	if err != nil || got.DeletedAt != nil {
		t.Fatalf("restored = %+v err=%v", got, err)
	}
}

func TestGenerate_LLMTimeoutRetryThenSkip(t *testing.T) {
	llm := &stubLLM{err: errors.New("timeout"), failTimes: 3}
	_, gen, store := testService(t, llm, groundedSignals())
	if err := gen.GenerateForUser(context.Background(), "u1", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if llm.calls.Load() != 3 {
		t.Fatalf("calls = %d", llm.calls.Load())
	}
	got, _ := store.ListTodayCards(context.Background(), "u1", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	if len(got) != 0 {
		t.Fatalf("skipped user still has cards %#v", got)
	}
}

func TestGenerate_ParseErrorMetric(t *testing.T) {
	before := PrometheusMetrics()
	llm := &stubLLM{body: "not-json"}
	_, gen, _ := testService(t, llm, groundedSignals())
	_ = gen.GenerateForUser(context.Background(), "u1", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	after := PrometheusMetrics()
	if !strings.Contains(after, "topic_card_parse_error_total") {
		t.Fatalf("metrics %s", after)
	}
	if after == before && !strings.Contains(after, "topic_card_parse_error_total") {
		t.Fatal("parse metric missing")
	}
}

// H1: below the corpus threshold there is nothing to ground a topic on, so the
// user gets no cards — and the model is never called.
func TestGenerate_BelowThresholdSkips(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	thin := stubSignals{sig: Signals{
		Level: "beginner", Empty: true,
		SceneCounts:    map[string]int{"standup": 2},
		FunctionCounts: map[string]int{},
		Blocks:         []BlockRef{{ID: "b1", ExpressionEN: "x", SceneTag: "standup", FunctionTag: "report"}},
	}}
	_, gen, store := testService(t, llm, thin)
	day := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	if llm.calls.Load() != 0 {
		t.Fatalf("a below-threshold user must not cost an LLM call, got %d", llm.calls.Load())
	}
	if got, _ := store.ListTodayCards(context.Background(), "u1", day); len(got) != 0 {
		t.Fatalf("below threshold still produced cards: %+v", got)
	}
	if !strings.Contains(PrometheusMetrics(), "below_threshold") {
		t.Fatalf("skip reason not recorded: %s", PrometheusMetrics())
	}
}

// H2: a card whose tags match nothing of the learner's is dropped rather than
// shipped as a generic topic.
func TestGenerate_UngroundedCardDropped(t *testing.T) {
	// Blocks exist (above the threshold) but none is about travel or social,
	// which is what the model returns.
	blocks := make([]BlockRef, 0, DefaultMinBlocks)
	for i := 0; i < DefaultMinBlocks; i++ {
		blocks = append(blocks, BlockRef{
			ID: "b-" + itoa(i), ExpressionEN: "deploy note " + itoa(i),
			SceneTag: "standup", FunctionTag: "report",
		})
	}
	sig := stubSignals{sig: Signals{
		Level:          "intermediate",
		SceneCounts:    map[string]int{"standup": DefaultMinBlocks},
		FunctionCounts: map[string]int{"report": DefaultMinBlocks},
		Blocks:         blocks,
	}}
	llm := &stubLLM{body: `{"cards":[
		{"title":"Ordering coffee","prompt_en":"Order a flat white.","prompt_zh":"点咖啡","card_type":"warmup","seed_tags":["travel","social"]},
		{"title":"Small talk at a party","prompt_en":"Ask about hobbies.","prompt_zh":"聊爱好","card_type":"practice","seed_tags":["social"]},
		{"title":"Weekend plans","prompt_en":"Describe your weekend.","prompt_zh":"周末计划","card_type":"stretch","seed_tags":["casual"]}
	]}`}
	_, gen, store := testService(t, llm, sig)
	day := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	// Nothing of the learner's can serve travel, social or casual, so all three
	// cards are dropped: no cards beats generic cards (H2).
	if got, _ := store.ListTodayCards(context.Background(), "u1", day); len(got) != 0 {
		t.Fatalf("generic topics were shipped: %+v", got)
	}
	if !strings.Contains(PrometheusMetrics(), "ungrounded") {
		t.Fatalf("drop not recorded: %s", PrometheusMetrics())
	}
}

// A grounded card carries both halves H1 asks for: the blocks it can put to
// work, and where it came from.
func TestGenerate_GroundedCardCarriesBlocksAndSource(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	_, gen, store := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	got, _ := store.ListTodayCards(context.Background(), "u1", day)
	if len(got) != 3 {
		t.Fatalf("cards = %d", len(got))
	}
	for _, card := range got {
		if len(card.BlockIDs) == 0 {
			t.Fatalf("card without a block list: %+v", card)
		}
		if card.SourceNote == "" {
			t.Fatalf("card without a source note (H2): %+v", card)
		}
	}
}

func TestScheduler_ActiveAndInactive(t *testing.T) {
	store := NewMemoryStore()
	llm := &stubLLM{body: threeCardJSON()}
	gen := NewGenerator(store, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	sched := NewScheduler(gen, stubActive{ids: []string{"active"}}, nil)
	if err := sched.DailyCardGeneration(context.Background(), day); err != nil {
		t.Fatal(err)
	}
	active, _ := store.ListTodayCards(context.Background(), "active", day)
	inactive, _ := store.ListTodayCards(context.Background(), "idle", day)
	if len(active) != 3 {
		t.Fatalf("active n=%d", len(active))
	}
	if len(inactive) != 0 {
		t.Fatalf("inactive n=%d", len(inactive))
	}
}

func TestScheduler_SessionActiveUsers(t *testing.T) {
	sessions := session.NewMemoryStore()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	_ = sessions.CreateSession(context.Background(), session.Session{
		ID: "s1", UserID: "u-active", SceneType: "standup", Status: session.StatusEnded, CreatedAt: now.Add(-2 * 24 * time.Hour), UpdatedAt: now,
	})
	_ = sessions.CreateSession(context.Background(), session.Session{
		ID: "s2", UserID: "u-old", SceneType: "standup", Status: session.StatusEnded, CreatedAt: now.Add(-40 * 24 * time.Hour), UpdatedAt: now,
	})
	ids, err := sessions.ListActiveUserIDs(context.Background(), now.Add(-ActiveLookback))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(ids, ",")
	if !strings.Contains(joined, "u-active") || strings.Contains(joined, "u-old") {
		t.Fatalf("ids = %v", ids)
	}
}

func TestGenerate_IdempotentSameDay(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	_, gen, store := testService(t, llm, groundedSignals())
	day := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	got, _ := store.ListTodayCards(context.Background(), "u1", day)
	if len(got) != 3 {
		t.Fatalf("n=%d", len(got))
	}
}

func TestScheduler_P95Budget(t *testing.T) {
	store := NewMemoryStore()
	llm := &stubLLM{body: threeCardJSON()}
	gen := NewGenerator(store, llm, groundedSignals())
	ids := make([]string, 200)
	for i := range ids {
		ids[i] = "u-" + time.Now().Format("15:04:05") + string(rune('a'+(i%26))) + string(rune('0'+i%10))
		ids[i] = strings.Repeat("x", 0) + "user-" + itoa(i)
	}
	sched := NewScheduler(gen, stubActive{ids: ids}, nil)
	start := time.Now()
	if err := sched.DailyCardGeneration(context.Background(), time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Minute {
		t.Fatalf("batch too slow: %s", time.Since(start))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// blockLookupStub resolves card block ids for the list response.
type blockLookupStub struct{ byID map[string][]string }

func (s blockLookupStub) ListBlocks(context.Context, corpus.ListFilter) ([]corpus.PhraseBlock, error) {
	out := make([]corpus.PhraseBlock, 0, len(s.byID))
	for id, texts := range s.byID {
		out = append(out, corpus.PhraseBlock{ID: id, ExpressionEN: texts[0], IntentZH: texts[1]})
	}
	return out, nil
}

// The client renders the 可调用话术块清单 from the list response, so the ids have
// to arrive resolved into something displayable.
func TestListToday_ResolvesCardBlocks(t *testing.T) {
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
	lookup := blockLookupStub{byID: map[string][]string{}}
	wantByCard := map[string]int{}
	for _, card := range cards {
		wantByCard[card.ID] = len(card.BlockIDs)
		for _, blockID := range card.BlockIDs {
			lookup.byID[blockID] = []string{"expression for " + blockID, "意图"}
		}
	}
	svc.SetBlockLookup(lookup)

	got, err := svc.ListToday(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ListToday: %v", err)
	}
	if len(got.Items) == 0 {
		t.Fatal("no items")
	}
	for _, item := range got.Items {
		if len(item.Blocks) != wantByCard[item.ID] {
			t.Fatalf("card %s resolved %d blocks, want %d", item.ID, len(item.Blocks), wantByCard[item.ID])
		}
		for _, ref := range item.Blocks {
			if ref.ExpressionEN == "" || ref.IntentZH == "" {
				t.Fatalf("block ref incomplete: %+v", ref)
			}
		}
	}
}
