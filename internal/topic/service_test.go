package topic

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
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

type stubActive struct{ ids []string }

func (s stubActive) ListActiveUserIDs(context.Context, time.Time) ([]string, error) {
	return s.ids, nil
}

func threeCardJSON() string {
	return `{"cards":[
		{"title":"Standup sync","prompt_en":"Share yesterday's progress.","prompt_zh":"同步昨天的进度","card_type":"warmup","seed_tags":["standup","report"]},
		{"title":"Clarify scope","prompt_en":"Ask what is in and out of scope.","prompt_zh":"澄清范围","card_type":"practice","seed_tags":["review","clarify"]},
		{"title":"Propose next step","prompt_en":"Suggest one concrete next action.","prompt_zh":"提出下一步","card_type":"stretch","seed_tags":["1on1","propose"]}
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
	svc, _, _ := testService(t, llm, stubSignals{sig: Signals{Empty: true, Level: "beginner"}})
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
	svc, _, _ := testService(t, llm, nil)
	day := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return day }
	listed, err := svc.ListToday(context.Background(), "u1")
	if err != nil || len(listed.Items) == 0 {
		t.Fatalf("list %v %+v", err, listed)
	}
	cardID := listed.Items[0].ID
	ok, err := svc.Checkin(context.Background(), "u1", cardID, "felt good")
	if err != nil || ok.StreakDays != 1 || ok.CheckinID == "" {
		t.Fatalf("first = %+v err=%v", ok, err)
	}
	_, err = svc.Checkin(context.Background(), "u1", cardID, "")
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 409 {
		t.Fatalf("dup = %v", err)
	}
	_, err = svc.Checkin(context.Background(), "other", cardID, "")
	if !errors.As(err, &ae) || ae.HTTPStatus != 403 {
		t.Fatalf("cross = %v", err)
	}
	_, err = svc.Checkin(context.Background(), "u1", listed.Items[1].ID, strings.Repeat("x", MaxReflectionLen+1))
	if !errors.As(err, &ae) || ae.HTTPStatus != 400 {
		t.Fatalf("long = %v", err)
	}
}

func TestCheckin_SoftDeleteAndRestore(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	svc, _, store := testService(t, llm, nil)
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
	_, err = svc.Checkin(context.Background(), "u1", listed.Items[0].ID, "")
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
	_, gen, store := testService(t, llm, nil)
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
	_, gen, _ := testService(t, llm, nil)
	_ = gen.GenerateForUser(context.Background(), "u1", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	after := PrometheusMetrics()
	if !strings.Contains(after, "topic_card_parse_error_total") {
		t.Fatalf("metrics %s", after)
	}
	if after == before && !strings.Contains(after, "topic_card_parse_error_total") {
		t.Fatal("parse metric missing")
	}
}

func TestGenerate_EmptyTagsStillCards(t *testing.T) {
	llm := &stubLLM{body: threeCardJSON()}
	_, gen, store := testService(t, llm, stubSignals{sig: Signals{Empty: true, SceneCounts: map[string]int{}, FunctionCounts: map[string]int{}}})
	if err := gen.GenerateForUser(context.Background(), "u1", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	got, _ := store.ListTodayCards(context.Background(), "u1", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	if len(got) != 3 {
		t.Fatalf("empty tags n=%d", len(got))
	}
}

func TestScheduler_ActiveAndInactive(t *testing.T) {
	store := NewMemoryStore()
	llm := &stubLLM{body: threeCardJSON()}
	gen := NewGenerator(store, llm, nil)
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
	_, gen, store := testService(t, llm, nil)
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
	gen := NewGenerator(store, llm, nil)
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
