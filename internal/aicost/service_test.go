package aicost

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRecordWritesLedgerRow(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return time.Date(2026, 8, 30, 15, 4, 0, 0, time.UTC) }
	svc.newID = func() string { return "log-1" }

	log, err := svc.Record(context.Background(), RecordRequest{
		UserID:    "user-1",
		TaskType:  "voice.asr",
		Model:     "doubao-asr-v1",
		TokensIn:  0,
		TokensOut: 0,
		AudioSec:  12,
		CostFen:   8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if log.ID != "log-1" || log.UserID == nil || *log.UserID != "user-1" {
		t.Fatalf("unexpected log: %+v", log)
	}

	logs, err := svc.ListRecent(context.Background(), "user-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("logs len = %d", len(logs))
	}
	if logs[0].TaskType != "voice.asr" || logs[0].CostFen != 8 {
		t.Fatalf("unexpected stored log: %+v", logs[0])
	}
}

func TestRecordRejectsInvalidInput(t *testing.T) {
	svc := NewService(NewMemoryStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	if _, err := svc.Record(context.Background(), RecordRequest{
		TaskType: "",
		Model:    "x",
	}); err == nil {
		t.Fatal("expected missing task_type error")
	}
	if _, err := svc.Record(context.Background(), RecordRequest{
		TaskType: "review.eval",
		Model:    "",
	}); err == nil {
		t.Fatal("expected missing model error")
	}
	if _, err := svc.Record(context.Background(), RecordRequest{
		TaskType: "review.eval",
		Model:    "ark",
		CostFen:  -1,
	}); err == nil {
		t.Fatal("expected negative cost error")
	}
}

func TestListRecentReturnsOldestToNewestWithinLimit(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 8, 30, 15, 10, 0, 0, time.UTC)
	svc.now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	id := 0
	svc.newID = func() string {
		id++
		return fmt.Sprintf("log-%d", id)
	}

	for _, userID := range []string{"user-1", "user-1", "user-2"} {
		if _, err := svc.Record(context.Background(), RecordRequest{
			UserID:   userID,
			TaskType: "review.eval",
			Model:    "ark-1",
		}); err != nil {
			t.Fatal(err)
		}
	}

	logs, err := svc.ListRecent(context.Background(), "user-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].UserID == nil || *logs[0].UserID != "user-1" {
		t.Fatalf("unexpected logs: %+v", logs)
	}
}
