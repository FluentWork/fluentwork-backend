package drill

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func seedDue(t *testing.T, store *corpus.MemoryStore, userID, id, state string, due time.Time) {
	t.Helper()
	now := due.Add(-time.Hour)
	if _, err := store.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID:             id,
		UserID:         userID,
		IntentZH:       "推动上线",
		ExpressionEN:   "Let's ship it " + id,
		AnchorUserSaid: "ship " + id,
		SceneTag:       "review",
		FunctionTag:    "commit",
		State:          state,
		NextDueAt:      due,
		EaseFactor:     2.5,
		CreatedAt:      now,
		UpdatedAt:      now,
	}}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func TestSelectBlocksForRound_NoDueReturnsEmpty(t *testing.T) {
	store := corpus.NewMemoryStore()
	seedDue(t, store, "user-1", "block-1", corpus.StateNew, time.Now().UTC().Add(24*time.Hour))
	got, err := SelectBlocksForRound(context.Background(), store, "user-1", time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d", len(got))
	}
}

func TestSelectBlocksForRound_CapsAtTenAndFillsAutomated(t *testing.T) {
	store := corpus.NewMemoryStore()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		id := "n-" + string(rune('a'+i))
		seedDue(t, store, "user-1", id, corpus.StateTraining, now.Add(-time.Duration(i)*time.Minute))
	}
	seedDue(t, store, "user-1", "auto-1", corpus.StateAutomated, now.Add(-time.Hour))
	got, err := SelectBlocksForRound(context.Background(), store, "user-1", now, 10)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("len=%d", len(got))
	}
}

func TestApplyJudge_PassFailAutomated(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	block := corpus.PhraseBlock{State: corpus.StateNew, SuccessStreak: 0, NextDueAt: now}
	p1 := ApplyJudge(block, true, now)
	if p1.SuccessStreak != 1 || p1.State != corpus.StateTraining {
		t.Fatalf("pass1 = %+v", p1)
	}
	if !p1.NextDueAt.Equal(now.Add(24 * time.Hour)) {
		t.Fatalf("due1 = %s", p1.NextDueAt)
	}
	p2 := ApplyJudge(p1, true, now)
	p3 := ApplyJudge(p2, true, now)
	if p3.State != corpus.StateAutomated || p3.SuccessStreak != 3 {
		t.Fatalf("pass3 = %+v", p3)
	}
	if !p3.NextDueAt.Equal(now.Add(7 * 24 * time.Hour)) {
		t.Fatalf("due3 = %s", p3.NextDueAt)
	}
	auto := ApplyJudge(p3, true, now)
	if !auto.NextDueAt.Equal(now.Add(30 * 24 * time.Hour)) {
		t.Fatalf("auto due = %s", auto.NextDueAt)
	}
	fail := ApplyJudge(p1, false, now)
	if fail.SuccessStreak != 0 || fail.State != corpus.StateTraining {
		t.Fatalf("fail = %+v", fail)
	}
	if !fail.NextDueAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("fail due = %s", fail.NextDueAt)
	}
}

func TestSelectBlocksForRound_FillsAutomated(t *testing.T) {
	store := corpus.NewMemoryStore()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	seedDue(t, store, "user-1", "train-1", corpus.StateTraining, now.Add(-time.Minute))
	seedDue(t, store, "user-1", "auto-1", corpus.StateAutomated, now.Add(-time.Hour))
	got, err := SelectBlocksForRound(context.Background(), store, "user-1", now, 10)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	ids := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !ids["train-1"] || !ids["auto-1"] {
		t.Fatalf("ids = %v", ids)
	}
}

func TestLLMJudge_TimeoutIsMiss(t *testing.T) {
	j := &LLMJudge{LLM: StaticCompleter{Err: context.DeadlineExceeded}}
	got, err := j.Judge(context.Background(), "target", "said")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got.Pass || got.Reason != "judge_timeout" {
		t.Fatalf("timeout = %+v", got)
	}
	if !strings.Contains(PrometheusMetrics(), "refine_timeout_total") {
		t.Fatal("missing refine_timeout_total")
	}
}

func TestLLMJudge_ParseFailureAndPass(t *testing.T) {
	j := &LLMJudge{LLM: StaticCompleter{Body: "not json"}}
	got, err := j.Judge(context.Background(), "target", "said")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got.Pass || got.Reason != "judge_parse_error" {
		t.Fatalf("parse fail = %+v", got)
	}
	if !strings.Contains(PrometheusMetrics(), "refine_parse_error_total") {
		t.Fatal("missing refine_parse_error_total")
	}
	j.LLM = StaticCompleter{Body: `{"pass": true, "judge_reason": "ok"}`}
	ok, err := j.Judge(context.Background(), "target", "said")
	if err != nil || !ok.Pass {
		t.Fatalf("pass = %+v err=%v", ok, err)
	}
}

func TestService_RoundAndJudge(t *testing.T) {
	store := corpus.NewMemoryStore()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	seedDue(t, store, "user-1", "block-1", corpus.StateNew, now)
	recs := NewMemoryRecordStore()
	svc := NewService(store, recs, &LLMJudge{LLM: StaticCompleter{Body: `{"pass":true}`}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return now }
	round, err := svc.Round(context.Background(), "user-1", 10)
	if err != nil {
		t.Fatalf("Round: %v", err)
	}
	if round.Size != 1 || round.Cards[0].BlockID != "block-1" {
		t.Fatalf("round = %+v", round)
	}
	resp, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "block-1", ASRText: "Let's ship it block-1", ResponseMS: 800})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if !resp.Pass || resp.SuccessStreak != 1 || !resp.Recorded {
		t.Fatalf("resp = %+v", resp)
	}
	if n := len(recs.Records()); n != 1 {
		t.Fatalf("records = %d", n)
	}
	_, err = svc.Judge(context.Background(), "user-2", JudgeRequest{BlockID: "block-1", ASRText: "hi"})
	if err == nil {
		t.Fatal("expected 403")
	}
}
