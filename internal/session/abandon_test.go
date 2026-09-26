package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

func abandonRequest(sessionID string) EndRequest {
	return EndRequest{
		SessionID:   sessionID,
		DurationSec: 12,
		Reason:      ReasonAbandoned,
		Utterances: []EndUtteranceItem{
			{Seq: 1, Speaker: SpeakerAI, Text: "How is the release looking?"},
			{Seq: 2, Speaker: SpeakerUser, Text: "The deploy is blocked on the migration."},
		},
	}
}

func TestEnd_AbandonedSessionIsNotReviewable(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := newTestService(t, store)
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.End(context.Background(), abandonRequest(created.SessionID)); err != nil {
		t.Fatalf("End: %v", err)
	}

	_, err = svc.GetReview(context.Background(), "user-1", created.SessionID)
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 404 {
		t.Fatalf("GetReview err = %v, want 404", err)
	}
}

func TestEnd_AbandonDoesNotEnqueueAReviewJob(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := newTestService(t, store)
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ended, err := svc.End(context.Background(), abandonRequest(created.SessionID))
	if err != nil {
		t.Fatalf("End: %v", err)
	}

	exists, err := store.HasSessionJob(context.Background(), created.SessionID, JobTypeSessionFinished,
		JobStatusPending, JobStatusProcessing, JobStatusDone)
	if err != nil {
		t.Fatalf("HasSessionJob: %v", err)
	}
	if exists {
		t.Fatal("an abandoned session must not enqueue a review job")
	}
	if !ended.ReviewSkipped {
		t.Fatalf("an abandoned session must not promise a review: %+v", ended)
	}
}

func TestEnd_AbandonKeepsTheTranscriptTheDurationAndTheCostRow(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := newTestService(t, store)
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := abandonRequest(created.SessionID)
	req.VoiceUsage = &VoiceUsageItem{UplinkMS: 4_000, DownlinkMS: 6_000, Model: "1.2.6.1"}
	ended, err := svc.End(context.Background(), req)
	if err != nil {
		t.Fatalf("End: %v", err)
	}

	saved, err := store.ListUtterances(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("ListUtterances: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("utterance count = %d, want 2", len(saved))
	}
	if ended.DurationSec != 12 {
		t.Fatalf("duration_sec = %d, want 12", ended.DurationSec)
	}
	if len(store.costLogs) != 1 {
		t.Fatalf("cost rows = %d, want 1: an abandon spends money like any other session", len(store.costLogs))
	}
	for _, row := range store.costLogs {
		if row.AudioSec != 10 {
			t.Fatalf("audio_sec = %d, want 10", row.AudioSec)
		}
	}
}

func TestEnd_AbandonIsIdempotent(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := newTestService(t, store)
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.End(context.Background(), abandonRequest(created.SessionID)); err != nil {
		t.Fatalf("End: %v", err)
	}
	replay, err := svc.End(context.Background(), abandonRequest(created.SessionID))
	if err != nil {
		t.Fatalf("End replay: %v", err)
	}
	if replay.Status != StatusAbandoned || !replay.AlreadyEnded || !replay.ReviewSkipped {
		t.Fatalf("replay = %+v", replay)
	}

	later, err := svc.End(context.Background(), EndRequest{
		SessionID: created.SessionID,
		Reason:    "client_closed",
		Utterances: []EndUtteranceItem{
			{Seq: 2, Speaker: SpeakerUser, Text: "The deploy is blocked on the migration."},
		},
	})
	if err != nil {
		t.Fatalf("End after abandon: %v", err)
	}
	if later.Status != StatusAbandoned {
		t.Fatalf("status = %q, want %q: a later end cannot un-abandon a session", later.Status, StatusAbandoned)
	}
	if !later.ReviewSkipped {
		t.Fatalf("a later end must not resurrect the pipeline: %+v", later)
	}

	if exists, _ := store.HasSessionJob(context.Background(), created.SessionID, JobTypeSessionFinished,
		JobStatusPending, JobStatusProcessing, JobStatusDone); exists {
		t.Fatal("nothing after an abandon may enqueue a review job")
	}
}

func TestEnd_AbandonWinsOverTheEmptySessionShortcut(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := newTestService(t, store)
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ended, err := svc.End(context.Background(), EndRequest{
		SessionID:   created.SessionID,
		DurationSec: 2,
		Reason:      ReasonAbandoned,
		Utterances:  []EndUtteranceItem{{Seq: 1, Speaker: SpeakerAI, Text: "How is the release looking?"}},
	})
	if err != nil {
		t.Fatalf("End: %v", err)
	}
	if ended.Status != StatusAbandoned {
		t.Fatalf("status = %q, want %q", ended.Status, StatusAbandoned)
	}
	if _, err := svc.GetReview(context.Background(), "user-1", created.SessionID); err == nil {
		t.Fatal("GetReview must not answer for an abandoned session")
	}
}

func TestEnd_AnUnrelatedReasonStillEndsNormally(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	svc := newTestService(t, store)
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ended, err := svc.End(context.Background(), EndRequest{
		SessionID: created.SessionID,
		Reason:    "client_closed",
		Utterances: []EndUtteranceItem{
			{Seq: 1, Speaker: SpeakerUser, Text: "The deploy is blocked on the migration."},
		},
	})
	if err != nil {
		t.Fatalf("End: %v", err)
	}
	if ended.Status != StatusEnded {
		t.Fatalf("status = %q, want %q", ended.Status, StatusEnded)
	}
	if ended.ReviewSkipped {
		t.Fatalf("only an abandon skips the pipeline: %+v", ended)
	}
	if exists, _ := store.HasSessionJob(context.Background(), created.SessionID, JobTypeSessionFinished,
		JobStatusPending, JobStatusProcessing, JobStatusDone); !exists {
		t.Fatal("an unrelated reason must still enqueue a review job")
	}
}

func TestEndSession_AbandonIsTerminalInTheStore(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	if err := store.CreateSession(context.Background(), Session{
		ID:        "s-abandon",
		UserID:    "u-1",
		Status:    StatusActive,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	costLog := buildVoiceCostLog(
		Session{ID: "s-abandon", UserID: "u-1"},
		&VoiceUsageItem{UplinkMS: 4_000, DownlinkMS: 6_000},
		func() string { return "cost-abandon" },
		now,
	)
	if costLog == nil {
		t.Fatal("precondition: usage was reported, so a row must be built")
	}
	if _, _, alreadyEnded, err := store.EndSession(context.Background(), "s-abandon", StatusAbandoned,
		12, nil, nil, now, costLog); err != nil {
		t.Fatalf("EndSession: %v", err)
	} else if alreadyEnded {
		t.Fatal("the first end is not a replay")
	}

	session, _, alreadyEnded, err := store.EndSession(context.Background(), "s-abandon", StatusEnded,
		99, nil, nil, now.Add(time.Minute), nil)
	if err != nil {
		t.Fatalf("EndSession replay: %v", err)
	}
	if !alreadyEnded {
		t.Fatal("an abandoned session is terminal, so a second end must be a replay")
	}
	if session.Status != StatusAbandoned {
		t.Fatalf("status = %q, want %q", session.Status, StatusAbandoned)
	}
	if session.DurationSec != 12 {
		t.Fatalf("duration_sec = %d, want 12", session.DurationSec)
	}
	if len(store.costLogs) != 1 {
		t.Fatalf("cost rows = %d, want 1", len(store.costLogs))
	}
}
