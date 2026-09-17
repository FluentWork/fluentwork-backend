package aicost

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

func summaryFixture(t *testing.T) (*Service, *MemoryStore, time.Time) {
	t.Helper()
	store := NewMemoryStore()
	svc := NewService(store, nil)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	return svc, store, now
}

func seedCost(t *testing.T, svc *Service, at time.Time, userID, taskType, model string, tokensIn, tokensOut, audioSec, chars int) {
	t.Helper()
	// Record uses the service clock, so write through the store to pin the time.
	log := Log{
		ID: taskType + "-" + at.Format(time.RFC3339Nano) + model, TaskType: taskType, Model: model,
		TokensIn: tokensIn, TokensOut: tokensOut, AudioSec: audioSec, Chars: chars, CreatedAt: at,
	}
	if userID != "" {
		log.UserID = &userID
	}
	if err := svc.store.CreateLog(context.Background(), log); err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
}

// 归因：哪个 task_type / model / 天在花用量。
func TestSummary_GroupsByTaskTypeAndModelAndDay(t *testing.T) {
	svc, _, now := summaryFixture(t)
	seedCost(t, svc, now.Add(-time.Hour), "user-1", TaskTypeVoiceDuplex, "volc-duplex", 0, 0, 42, 0)
	seedCost(t, svc, now.Add(-2*time.Hour), "user-1", TaskTypeVoiceDuplex, "volc-duplex", 0, 0, 18, 0)
	seedCost(t, svc, now.Add(-3*time.Hour), "user-1", "review.eval", "doubao", 100, 50, 0, 0)
	seedCost(t, svc, now.Add(-25*time.Hour), "user-1", TaskTypeVoiceTTS, "voice-a", 0, 0, 0, 120)

	byTask, err := svc.Summary(context.Background(), SummaryFilter{GroupBy: GroupByTaskType})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if len(byTask.Rows) != 3 {
		t.Fatalf("rows = %+v", byTask.Rows)
	}
	// Sorted by key: review.eval, voice.duplex, voice.tts.
	if byTask.Rows[0].Key != "review.eval" || byTask.Rows[1].Key != TaskTypeVoiceDuplex || byTask.Rows[2].Key != TaskTypeVoiceTTS {
		t.Fatalf("keys = %+v", byTask.Rows)
	}
	if byTask.Rows[1].Rows != 2 || byTask.Rows[1].AudioSec != 60 {
		t.Fatalf("duplex bucket = %+v", byTask.Rows[1])
	}
	if byTask.Rows[0].TokensIn != 100 || byTask.Rows[0].TokensOut != 50 {
		t.Fatalf("tokens not summed per unit: %+v", byTask.Rows[0])
	}
	if byTask.Rows[2].Chars != 120 {
		t.Fatalf("chars not summed: %+v", byTask.Rows[2])
	}
	if !strings.Contains(byTask.CostFenIsNotMoney, "not yet money") {
		t.Fatalf("the fen column must say what it is: %q", byTask.CostFenIsNotMoney)
	}

	byDay, err := svc.Summary(context.Background(), SummaryFilter{GroupBy: GroupByDay})
	if err != nil {
		t.Fatalf("Summary by day: %v", err)
	}
	if len(byDay.Rows) != 2 {
		t.Fatalf("day buckets = %+v", byDay.Rows)
	}
	if byDay.Rows[0].Key != "2026-09-17" || byDay.Rows[1].Key != "2026-09-18" {
		t.Fatalf("day keys = %+v", byDay.Rows)
	}

	byModel, err := svc.Summary(context.Background(), SummaryFilter{GroupBy: GroupByModel})
	if err != nil {
		t.Fatalf("Summary by model: %v", err)
	}
	if len(byModel.Rows) != 3 {
		t.Fatalf("model buckets = %+v", byModel.Rows)
	}
}

func TestSummary_WindowIsHalfOpenAndFilterable(t *testing.T) {
	svc, _, now := summaryFixture(t)
	seedCost(t, svc, now.Add(-48*time.Hour), "user-1", TaskTypeVoiceDuplex, "m", 0, 0, 10, 0)
	seedCost(t, svc, now.Add(-12*time.Hour), "user-1", TaskTypeVoiceDuplex, "m", 0, 0, 20, 0)
	seedCost(t, svc, now, "user-1", TaskTypeVoiceDuplex, "m", 0, 0, 30, 0) // == until, excluded
	seedCost(t, svc, now.Add(-time.Hour), "user-2", TaskTypeVoiceDuplex, "m", 0, 0, 99, 0)

	got, err := svc.Summary(context.Background(), SummaryFilter{
		Since:   now.Add(-24 * time.Hour),
		Until:   now,
		GroupBy: GroupByTaskType,
	})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if len(got.Rows) != 1 || got.Rows[0].AudioSec != 119 {
		t.Fatalf("rows = %+v, want the in-window rows from both users", got.Rows)
	}

	scoped, err := svc.Summary(context.Background(), SummaryFilter{
		UserID:  "user-1",
		Since:   now.Add(-24 * time.Hour),
		Until:   now,
		GroupBy: GroupByTaskType,
	})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if len(scoped.Rows) != 1 || scoped.Rows[0].AudioSec != 20 {
		t.Fatalf("user filter not applied: %+v", scoped.Rows)
	}
}

func TestSummary_Validation(t *testing.T) {
	svc, _, now := summaryFixture(t)
	cases := map[string]SummaryFilter{
		"open group_by":     {GroupBy: "planet"},
		"since after until": {Since: now, Until: now.Add(-time.Hour)},
		"too wide":          {Since: now.Add(-365 * 24 * time.Hour), Until: now},
	}
	for name, filter := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Summary(context.Background(), filter)
			var apiErr *apierr.Error
			if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 400 {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// 默认窗口 7 天：不传参数也不能变成"全表"。
func TestSummary_DefaultsToSevenDays(t *testing.T) {
	svc, _, now := summaryFixture(t)
	seedCost(t, svc, now.Add(-3*24*time.Hour), "user-1", TaskTypeVoiceDuplex, "m", 0, 0, 5, 0)
	seedCost(t, svc, now.Add(-30*24*time.Hour), "user-1", TaskTypeVoiceDuplex, "m", 0, 0, 500, 0)

	got, err := svc.Summary(context.Background(), SummaryFilter{})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.GroupBy != GroupByTaskType {
		t.Fatalf("group_by = %q", got.GroupBy)
	}
	if len(got.Rows) != 1 || got.Rows[0].AudioSec != 5 {
		t.Fatalf("default window leaked older rows: %+v", got.Rows)
	}
}

// P1-5 的 TTS 记账适配器：用量是事实，费率待核实。
func TestTTSRecorder_WritesUsageWithZeroFen(t *testing.T) {
	svc, store, _ := summaryFixture(t)
	recorder := TTSRecorder{Svc: svc}
	if err := recorder.RecordTTSUsage(context.Background(), "voice-a", 321); err != nil {
		t.Fatalf("RecordTTSUsage: %v", err)
	}
	logs, err := store.ListRecent(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("logs = %+v", logs)
	}
	log := logs[0]
	if log.TaskType != TaskTypeVoiceTTS || log.Model != "voice-a" || log.Chars != 321 || log.CostFen != 0 {
		t.Fatalf("log = %+v", log)
	}
	// 没有独立 ASR SKU 时不该写 voice.asr：会把 duplex 已经计过的音频再算一遍。
	for _, existing := range logs {
		if existing.TaskType == "voice.asr" {
			t.Fatal("a standalone ASR row must not be written")
		}
	}
}

func TestTTSRecorder_DefaultsAndNilSafety(t *testing.T) {
	svc, store, _ := summaryFixture(t)
	recorder := TTSRecorder{Svc: svc}
	if err := recorder.RecordTTSUsage(context.Background(), "   ", -5); err != nil {
		t.Fatalf("RecordTTSUsage: %v", err)
	}
	logs, _ := store.ListRecent(context.Background(), "", 10)
	if len(logs) != 1 || logs[0].Model != "default" || logs[0].Chars != 0 {
		t.Fatalf("log = %+v", logs)
	}
	if err := (TTSRecorder{}).RecordTTSUsage(context.Background(), "voice-a", 10); err != nil {
		t.Fatalf("nil service must be a no-op: %v", err)
	}
}
