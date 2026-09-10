package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

const goodJSON = `{"score":0.8,"dims":{"grammar":0.7,"fluency":0.9,"vocabulary":0.6},"suggestions":["Use touch base"]}`

func TestEvaluator_ScoresDimensions(t *testing.T) {
	e := &Evaluator{LLM: StaticCompleter{Body: goodJSON}}
	got := e.Evaluate(context.Background(), session.Utterance{Text: "I will sync up", Speaker: session.SpeakerUser}, nil)
	if got.Score != 0.8 || got.Dims.Grammar != 0.7 || got.Dims.Fluency != 0.9 || got.Dims.Vocabulary != 0.6 {
		t.Fatalf("got %+v", got)
	}
}

func TestEvaluator_TruncatesSuggestions(t *testing.T) {
	e := &Evaluator{LLM: StaticCompleter{Body: `{"score":0.8,"dims":{"grammar":0.7,"fluency":0.9,"vocabulary":0.6},"suggestions":["Use touch base with them tomorrow morning extra"]}`}}
	got := e.Evaluate(context.Background(), session.Utterance{Text: "I will sync up", Speaker: session.SpeakerUser}, nil)
	if len(got.Suggestions) != 1 || utf8.RuneCountInString(got.Suggestions[0]) > MaxSuggestionRunes {
		t.Fatalf("suggestions = %#v", got.Suggestions)
	}
}

func TestEvaluator_AbnormalASR(t *testing.T) {
	e := &Evaluator{LLM: StaticCompleter{Body: goodJSON}}
	low := 0.1
	zero := e.Evaluate(context.Background(), session.Utterance{Text: "???", Speaker: session.SpeakerUser, ASRConfidence: &low}, nil)
	if zero.Score != 0 || zero.Reason != "abnormal_asr" {
		t.Fatalf("abnormal = %+v", zero)
	}
	empty := e.Evaluate(context.Background(), session.Utterance{Text: "  ", Speaker: session.SpeakerUser}, nil)
	if empty.Score != 0 || empty.Reason != "abnormal_asr" {
		t.Fatalf("empty = %+v", empty)
	}
}

func TestEvaluator_TimeoutFallback(t *testing.T) {
	e := &Evaluator{LLM: StaticCompleter{Err: context.DeadlineExceeded}}
	to := e.Evaluate(context.Background(), session.Utterance{Text: "hello", Speaker: session.SpeakerUser}, nil)
	if to.Score != 0.5 || to.Reason != "timeout" || to.Suggestions[0] != "系统繁忙，请稍后重试" {
		t.Fatalf("timeout = %+v", to)
	}
	if !strings.Contains(PrometheusMetrics(), "review_eval_timeout_total") {
		t.Fatal("missing timeout metric")
	}
}

func TestEvaluator_NonJSONFallback(t *testing.T) {
	e := &Evaluator{LLM: StaticCompleter{Body: "not json"}}
	fb := e.Evaluate(context.Background(), session.Utterance{Text: "hello", Speaker: session.SpeakerUser}, nil)
	if fb.Score != 0.5 || fb.Reason != "parse_error" || fb.Suggestions[0] != "系统繁忙，请稍后重试" {
		t.Fatalf("parse fallback = %+v", fb)
	}
	if !strings.Contains(PrometheusMetrics(), "review_eval_parse_error_total") {
		t.Fatal("missing parse metric")
	}
}

func TestEvalPrompt_IncludesHits(t *testing.T) {
	prompt := EvalPrompt(session.Utterance{Text: "ship it"}, []corpus.RecentHit{{IntentZH: "推动上线", ChunkEN: "Let's ship it"}})
	if !strings.Contains(prompt, "Let's ship it") || !strings.Contains(prompt, "推动上线") {
		t.Fatalf("prompt missing hits: %s", prompt)
	}
}

func TestService_RunEvalJobAndSummary(t *testing.T) {
	store, now := endedSession(t, "sess-1", "user-1",
		session.Utterance{ID: "u1", SessionID: "sess-1", Seq: 1, Speaker: session.SpeakerUser, Text: "I will follow up"},
		session.Utterance{ID: "u2", SessionID: "sess-1", Seq: 2, Speaker: session.SpeakerAI, Text: "Sounds good"},
	)
	svc := NewService(store, nil, StaticCompleter{Body: `{"score":1,"dims":{"grammar":1,"fluency":1,"vocabulary":1},"suggestions":["Nice"]}`}, nil)
	svc.SetInterval(0)
	if err := svc.RunEvalJob(context.Background(), "sess-1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	sum, err := svc.Summary(context.Background(), "sess-1")
	if err != nil || sum == nil || !sum.Complete || sum.Score != 1 || sum.UtteranceN != 1 {
		t.Fatalf("summary = %+v err=%v", sum, err)
	}
	rows, _ := store.ListUtterances(context.Background(), "sess-1")
	if len(rows[0].LLMEvalJSON) == 0 {
		t.Fatal("missing llm_eval_json")
	}
	var saved UtteranceEval
	if err := json.Unmarshal(rows[0].LLMEvalJSON, &saved); err != nil || saved.Score != 1 {
		t.Fatalf("saved = %s", rows[0].LLMEvalJSON)
	}
	_ = now
}

func TestService_EmptySessionZeroScore(t *testing.T) {
	store, _ := endedSession(t, "empty", "user-1")
	svc := NewService(store, nil, StaticCompleter{Body: goodJSON}, nil)
	if err := svc.RunEvalJob(context.Background(), "empty"); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.Summary(context.Background(), "empty")
	if err != nil || sum == nil || !sum.Complete || sum.Score != 0 || sum.UtteranceN != 0 {
		t.Fatalf("summary = %+v err=%v", sum, err)
	}
}

func TestService_IncompleteUntilAllScored(t *testing.T) {
	store, _ := endedSession(t, "partial", "user-1",
		session.Utterance{ID: "a", SessionID: "partial", Seq: 1, Speaker: session.SpeakerUser, Text: "hello"},
		session.Utterance{ID: "b", SessionID: "partial", Seq: 2, Speaker: session.SpeakerUser, Text: "world"},
	)
	svc := NewService(store, nil, StaticCompleter{Body: goodJSON}, nil)
	svc.SetInterval(0)
	raw, _ := json.Marshal(UtteranceEval{Score: 1, Suggestions: []string{"ok"}})
	if err := store.SaveUtteranceEval(context.Background(), "a", raw); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.Summary(context.Background(), "partial")
	if err != nil || sum == nil || sum.Complete {
		t.Fatalf("want incomplete, got %+v err=%v", sum, err)
	}
}

func TestService_RateLimitSleeps(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.CreateSession(ctx, session.Session{ID: "s", UserID: "u", SceneType: "demo", Status: session.StatusCreated, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	utts := make([]session.Utterance, 0, 4)
	for i := 1; i <= 4; i++ {
		utts = append(utts, session.Utterance{ID: string(rune('a' + i)), SessionID: "s", Seq: i, Speaker: session.SpeakerUser, Text: "hello", CreatedAt: now})
	}
	if _, _, _, err := store.EndSession(ctx, "s", 4, utts, now, nil); err != nil {
		t.Fatal(err)
	}
	var sleeps []time.Duration
	svc := NewService(store, nil, StaticCompleter{Body: `{"score":0.5,"dims":{"grammar":0.5,"fluency":0.5,"vocabulary":0.5}}`}, nil)
	svc.SetInterval(time.Second)
	svc.SetSleep(func(d time.Duration) { sleeps = append(sleeps, d) })
	if err := svc.RunEvalJob(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if len(sleeps) != 3 {
		t.Fatalf("sleeps = %d want 3", len(sleeps))
	}
}

func TestService_FiftyUtterancesUnderBudget(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.CreateSession(ctx, session.Session{ID: "big", UserID: "u", SceneType: "demo", Status: session.StatusCreated, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	utts := make([]session.Utterance, 0, 50)
	for i := 1; i <= 50; i++ {
		utts = append(utts, session.Utterance{ID: "id-" + itoa(i), SessionID: "big", Seq: i, Speaker: session.SpeakerUser, Text: "hello team", CreatedAt: now})
	}
	if _, _, _, err := store.EndSession(ctx, "big", 50, utts, now, nil); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil, StaticCompleter{Body: `{"score":0.6,"dims":{"grammar":0.6,"fluency":0.6,"vocabulary":0.6}}`}, nil)
	svc.SetInterval(0)
	start := time.Now()
	if err := svc.RunEvalJob(ctx, "big"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 60*time.Second {
		t.Fatalf("took %s", took)
	}
}

func TestService_EnqueueEvalIdempotent(t *testing.T) {
	store := session.NewMemoryStore()
	svc := NewService(store, nil, StaticCompleter{Body: `{}`}, nil)
	if err := svc.EnqueueEval(context.Background(), "sess-x"); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnqueueEval(context.Background(), "sess-x"); err != nil {
		t.Fatal(err)
	}
	ok, err := store.HasSessionJob(context.Background(), "sess-x", session.JobTypeSessionEval, session.JobStatusPending)
	if err != nil || !ok {
		t.Fatalf("job missing: %v %v", ok, err)
	}
}

func TestService_EvalSurvivesA4Undelete(t *testing.T) {
	store, now := endedSession(t, "sess-a4", "user-1",
		session.Utterance{ID: "u1", SessionID: "sess-a4", Seq: 1, Speaker: session.SpeakerUser, Text: "I will follow up"},
	)
	svc := NewService(store, nil, StaticCompleter{Body: `{"score":0.9,"dims":{"grammar":0.9,"fluency":0.9,"vocabulary":0.9},"suggestions":["Nice"]}`}, nil)
	svc.SetInterval(0)
	if err := svc.RunEvalJob(context.Background(), "sess-a4"); err != nil {
		t.Fatal(err)
	}
	wiper := session.PrivacyWiper{Store: store}
	if n, err := wiper.Wipe(context.Background(), "user-1", now); err != nil || n != 1 {
		t.Fatalf("wipe n=%d err=%v", n, err)
	}
	if n, err := wiper.Restore(context.Background(), "user-1"); err != nil || n != 1 {
		t.Fatalf("restore n=%d err=%v", n, err)
	}
	sum, err := svc.Summary(context.Background(), "sess-a4")
	if err != nil || sum == nil || !sum.Complete || sum.Score != 0.9 {
		t.Fatalf("summary after undelete = %+v err=%v", sum, err)
	}
	rows, err := store.ListUtterances(context.Background(), "sess-a4")
	if err != nil || len(rows) != 1 || len(rows[0].LLMEvalJSON) == 0 {
		t.Fatalf("utterances after undelete = %+v err=%v", rows, err)
	}
}

func TestService_UsesSessionHits(t *testing.T) {
	store, _ := endedSession(t, "sess-h", "user-1",
		session.Utterance{ID: "u1", SessionID: "sess-h", Seq: 1, Speaker: session.SpeakerUser, Text: "ship it"},
	)
	hits := fakeHits{hits: []corpus.RecentHit{{IntentZH: "推动上线", ChunkEN: "Let's ship it"}}}
	spy := &spyCompleter{body: goodJSON}
	svc := NewService(store, hits, spy, nil)
	svc.SetInterval(0)
	if err := svc.RunEvalJob(context.Background(), "sess-h"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spy.prompt, "Let's ship it") {
		t.Fatalf("prompt = %q", spy.prompt)
	}
}

func endedSession(t *testing.T, id, userID string, utts ...session.Utterance) (*session.MemoryStore, time.Time) {
	t.Helper()
	store := session.NewMemoryStore()
	now := time.Now().UTC()
	if err := store.CreateSession(context.Background(), session.Session{
		ID: id, UserID: userID, SceneType: session.DefaultSceneType, Status: session.StatusCreated, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := range utts {
		if utts[i].CreatedAt.IsZero() {
			utts[i].CreatedAt = now
		}
	}
	if _, _, _, err := store.EndSession(context.Background(), id, 10, utts, now, nil); err != nil {
		t.Fatalf("end: %v", err)
	}
	return store, now
}

type fakeHits struct {
	hits []corpus.RecentHit
}

func (f fakeHits) ListSessionHits(context.Context, string) ([]corpus.RecentHit, error) {
	return f.hits, nil
}

type spyCompleter struct {
	body   string
	prompt string
}

func (s *spyCompleter) Complete(_ context.Context, prompt string) (string, error) {
	s.prompt = prompt
	return s.body, nil
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
