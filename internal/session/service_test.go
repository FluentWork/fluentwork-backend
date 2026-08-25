package session

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

func TestCreateSessionIssuesTicket(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		HTTPAddr:           ":0",
		AppEnv:             "development",
		AuthJWTSecret:      config.DevJWTSecret,
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   60 * time.Second,
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	result, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.SessionID == "" || result.Ticket == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.WSSURL != cfg.VoiceGatewayWSSURL {
		t.Fatalf("wss_url = %q", result.WSSURL)
	}
	if result.TicketExpiresIn != 60 {
		t.Fatalf("ticket_expires_in = %d", result.TicketExpiresIn)
	}
	if result.SceneType != "standup" || result.Status != StatusCreated {
		t.Fatalf("unexpected metadata: %+v", result)
	}

	ticket, err := svc.ConsumeTicket(context.Background(), result.Ticket)
	if err != nil {
		t.Fatalf("ConsumeTicket: %v", err)
	}
	if ticket.SessionID != result.SessionID || ticket.UserID != "user-1" {
		t.Fatalf("unexpected ticket: %+v", ticket)
	}
	if ticket.UsedAt == nil {
		t.Fatal("expected UsedAt to be set after consume")
	}
	if _, err := svc.ConsumeTicket(context.Background(), result.Ticket); err == nil {
		t.Fatal("expected replay to fail")
	}
}

func TestCreateDefaultsSceneTypeAndRejectsInvalidMaterial(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   time.Minute,
		AuthJWTSecret:      config.DevJWTSecret,
		AppEnv:             "development",
		HTTPAddr:           ":0",
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ok, err := svc.Create(context.Background(), "user-1", CreateRequest{})
	if err != nil {
		t.Fatalf("Create empty body: %v", err)
	}
	if ok.SceneType != DefaultSceneType {
		t.Fatalf("scene_type = %q", ok.SceneType)
	}

	bad := "not a valid id!!!"
	if _, err := svc.Create(context.Background(), "user-1", CreateRequest{MaterialID: &bad}); err == nil {
		t.Fatal("expected invalid material_id error")
	}
}

func TestLookupTicketRejectsExpired(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   time.Second,
		AuthJWTSecret:      config.DevJWTSecret,
		AppEnv:             "development",
		HTTPAddr:           ":0",
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fixed := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixed }

	result, err := svc.Create(context.Background(), "user-1", CreateRequest{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	svc.now = func() time.Time { return fixed.Add(2 * time.Second) }
	if _, err := svc.ConsumeTicket(context.Background(), result.Ticket); err == nil {
		t.Fatal("expected expired ticket error")
	}
}

func TestLookupTicketRejectsInvalidAndUsed(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   time.Minute,
		AuthJWTSecret:      config.DevJWTSecret,
		AppEnv:             "development",
		HTTPAddr:           ":0",
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := svc.ConsumeTicket(context.Background(), ""); err == nil {
		t.Fatal("expected empty ticket error")
	}
	if _, err := svc.ConsumeTicket(context.Background(), "not-a-real-ticket"); err == nil {
		t.Fatal("expected invalid ticket error")
	}

	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	usedAt := now
	if err := store.CreateTicket(context.Background(), Ticket{
		ID:        "ticket-1",
		SessionID: "session-1",
		UserID:    "user-1",
		Hash:      hashTicket("used-ticket"),
		ExpiresAt: now.Add(time.Minute),
		UsedAt:    &usedAt,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if _, err := svc.ConsumeTicket(context.Background(), "used-ticket"); err == nil {
		t.Fatal("expected used ticket error")
	}
}

func TestCreateRejectsInvalidSceneType(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   time.Minute,
		AuthJWTSecret:      config.DevJWTSecret,
		AppEnv:             "development",
		HTTPAddr:           ":0",
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "bad scene!"}); err == nil {
		t.Fatal("expected invalid scene_type error")
	}
}

func TestReassignerMovesSessions(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   time.Minute,
		AuthJWTSecret:      config.DevJWTSecret,
		AppEnv:             "development",
		HTTPAddr:           ":0",
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	created, err := svc.Create(context.Background(), "guest-1", CreateRequest{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	reassigner := Reassigner{Store: store}
	if err := reassigner.ReassignFromGuest(context.Background(), "guest-1", "user-2"); err != nil {
		t.Fatalf("ReassignFromGuest: %v", err)
	}
	got, err := store.GetSession(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.UserID != "user-2" {
		t.Fatalf("user_id = %q", got.UserID)
	}
}

func TestActivateAndEndPersistUtterances(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   time.Minute,
		AuthJWTSecret:      config.DevJWTSecret,
		AppEnv:             "development",
		HTTPAddr:           ":0",
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "demo"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	activated, err := svc.Activate(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if activated.Status != StatusActive {
		t.Fatalf("status = %q", activated.Status)
	}

	ended, err := svc.End(context.Background(), EndRequest{
		SessionID:   created.SessionID,
		DurationSec: 12,
		Reason:      "user",
		Utterances: []EndUtteranceItem{
			{Seq: 1, Speaker: SpeakerAI, Text: "ready"},
			{Seq: 2, Speaker: SpeakerUser, Text: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("End: %v", err)
	}
	if ended.Status != StatusEnded || ended.UtteranceCount != 2 || ended.AlreadyEnded {
		t.Fatalf("unexpected end response: %+v", ended)
	}

	replay, err := svc.End(context.Background(), EndRequest{
		SessionID:   created.SessionID,
		DurationSec: 99,
		Utterances:  []EndUtteranceItem{{Seq: 1, Speaker: SpeakerAI, Text: "ignored"}},
	})
	if err != nil {
		t.Fatalf("End replay: %v", err)
	}
	if !replay.AlreadyEnded || replay.DurationSec != 12 || replay.UtteranceCount != 2 {
		t.Fatalf("unexpected replay: %+v", replay)
	}

	rows, err := store.ListUtterances(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("ListUtterances: %v", err)
	}
	if len(rows) != 2 || rows[0].Text != "ready" || rows[1].Speaker != SpeakerUser {
		t.Fatalf("utterances = %+v", rows)
	}

	ok, err := svc.ProcessNextJob(context.Background(), "test-worker")
	if err != nil {
		t.Fatalf("ProcessNextJob: %v", err)
	}
	if !ok {
		t.Fatal("expected a job to process")
	}
	got, err := store.GetSession(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("GetSession after review: %v", err)
	}
	if got.Status != StatusReviewed || len(got.ReviewJSON) == 0 {
		t.Fatalf("expected reviewed session with review_json, got %+v", got)
	}
	ok, err = svc.ProcessNextJob(context.Background(), "test-worker")
	if err != nil {
		t.Fatalf("ProcessNextJob empty: %v", err)
	}
	if ok {
		t.Fatal("expected empty queue")
	}
}

func TestEndReenqueuesMissingFinishedJob(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   time.Minute,
		AuthJWTSecret:      config.DevJWTSecret,
		AppEnv:             "development",
		HTTPAddr:           ":0",
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "demo"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Activate(context.Background(), created.SessionID); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	ended, err := svc.End(context.Background(), EndRequest{
		SessionID:   created.SessionID,
		DurationSec: 5,
		Utterances:  []EndUtteranceItem{{Seq: 1, Speaker: SpeakerAI, Text: "ready"}},
	})
	if err != nil {
		t.Fatalf("End: %v", err)
	}
	if ended.AlreadyEnded {
		t.Fatal("first End should not be already ended")
	}

	// Simulate End that committed before enqueue (drop outbox rows).
	store.mu.Lock()
	store.jobs = map[string]Job{}
	store.mu.Unlock()

	replay, err := svc.End(context.Background(), EndRequest{
		SessionID: created.SessionID,
		Utterances: []EndUtteranceItem{
			{Seq: 1, Speaker: SpeakerAI, Text: "ignored-invalid-would-fail-if-validated"},
		},
	})
	if err != nil {
		t.Fatalf("End replay: %v", err)
	}
	if !replay.AlreadyEnded {
		t.Fatal("expected already ended")
	}
	exists, err := store.HasSessionJob(context.Background(), created.SessionID, JobTypeSessionFinished,
		JobStatusPending, JobStatusProcessing, JobStatusDone)
	if err != nil || !exists {
		t.Fatalf("expected re-enqueued job, exists=%v err=%v", exists, err)
	}
}

func TestClaimNextJobReclaimsStaleProcessing(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	lockedAt := now.Add(-DefaultJobLease - time.Second)
	job := Job{
		ID:          "job-1",
		SessionID:   "s1",
		JobType:     JobTypeSessionFinished,
		Status:      JobStatusProcessing,
		Attempts:    1,
		AvailableAt: now.Add(-time.Hour),
		LockedAt:    &lockedAt,
		LockedBy:    strPtr("dead-worker"),
		CreatedAt:   now.Add(-time.Hour),
		UpdatedAt:   lockedAt,
	}
	if err := store.EnqueueJob(context.Background(), Job{
		ID: job.ID, SessionID: job.SessionID, JobType: job.JobType,
		Status: JobStatusPending, AvailableAt: job.AvailableAt,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	store.mu.Lock()
	store.jobs[job.ID] = job
	store.mu.Unlock()

	claimed, err := store.ClaimNextJob(context.Background(), "worker-2", now)
	if err != nil {
		t.Fatalf("ClaimNextJob: %v", err)
	}
	if claimed.Status != JobStatusProcessing || claimed.Attempts != 2 {
		t.Fatalf("unexpected claim: %+v", claimed)
	}
	if claimed.LockedBy == nil || *claimed.LockedBy != "worker-2" {
		t.Fatalf("locked_by = %v", claimed.LockedBy)
	}
}

func strPtr(v string) *string { return &v }
