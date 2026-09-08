package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
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

	poll, err := svc.GetReview(context.Background(), "user-1", created.SessionID)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if poll.Status != ReviewPollReady || len(poll.Review) == 0 {
		t.Fatalf("expected ready review, got %+v", poll)
	}
	var payload map[string]any
	if err := json.Unmarshal(poll.Review, &payload); err != nil {
		t.Fatalf("decode poll review: %v", err)
	}
	if _, ok := payload["review"]; !ok {
		t.Fatalf("expected full model review payload, got %+v", payload)
	}
	if _, ok := payload["refine"]; !ok {
		t.Fatalf("expected refine in full model payload, got %+v", payload)
	}
}

func TestGetReviewPendingAndFailedAndAuthz(t *testing.T) {
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

	pending, err := svc.GetReview(context.Background(), "user-1", created.SessionID)
	if err != nil {
		t.Fatalf("GetReview pending: %v", err)
	}
	if pending.Status != ReviewPollPending || pending.Review != nil {
		t.Fatalf("expected pending without review, got %+v", pending)
	}

	if _, err := svc.GetReview(context.Background(), "other-user", created.SessionID); err == nil {
		t.Fatal("expected not found for non-owner")
	}

	now := time.Now().UTC()
	locked := now
	if err := store.EnqueueJob(context.Background(), Job{
		ID:          "job-fail",
		SessionID:   created.SessionID,
		JobType:     JobTypeSessionFinished,
		Status:      JobStatusProcessing,
		Attempts:    MaxJobAttempts,
		AvailableAt: now,
		LockedAt:    &locked,
		LockedBy:    strPtr("w"),
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if err := store.FailJob(context.Background(), "job-fail", now, "boom", time.Second); err != nil {
		t.Fatalf("FailJob: %v", err)
	}
	failed, err := svc.GetReview(context.Background(), "user-1", created.SessionID)
	if err != nil {
		t.Fatalf("GetReview failed: %v", err)
	}
	if failed.Status != ReviewPollFailed {
		t.Fatalf("expected failed, got %+v", failed)
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

func TestBuildReviewArtifactsUsesGeneratorWhenPresent(t *testing.T) {
	svc := NewService(NewMemoryStore(), config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetReviewGenerator(&fakeReviewGenerator{
		result: reviewgen.Result{
			Review:    json.RawMessage(`{"goal_achievement":{},"issues":[],"suggestions":[],"comparisons":[{},{},{}]}`),
			Refine:    json.RawMessage(`{"blocks":[{"intent_zh":"同步","expression_en":"I'll follow up.","anchor_user_said":"follow up","scene_tag":"standup","function_tag":"report"}]}`),
			Generator: "ark-review-refine-v1",
			Model:     "ep-review",
			TokensIn:  11,
			TokensOut: 22,
		},
	})

	artifacts, err := svc.buildReviewArtifacts(context.Background(), Session{
		ID:        "s1",
		UserID:    "u1",
		SceneType: "standup",
	}, []Utterance{{Speaker: SpeakerUser, Text: "I will follow up."}})
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.Generator != "ark-review-refine-v1" || artifacts.Cost == nil || artifacts.Cost.Model != "ep-review" {
		t.Fatalf("unexpected artifacts: %+v", artifacts)
	}
	if artifacts.Cost.TaskType != "review.eval" {
		t.Fatalf("Cost.TaskType = %q, want review.eval", artifacts.Cost.TaskType)
	}
	var reviewDoc map[string]any
	if err := json.Unmarshal(artifacts.ReviewJSON, &reviewDoc); err != nil {
		t.Fatal(err)
	}
	if reviewDoc["generator"] != "ark-review-refine-v1" {
		t.Fatalf("generator metadata missing: %+v", reviewDoc)
	}
	if _, ok := reviewDoc["review"]; !ok {
		t.Fatalf("missing nested review payload: %+v", reviewDoc)
	}
	if _, ok := reviewDoc["refine"]; !ok {
		t.Fatalf("missing nested refine payload: %+v", reviewDoc)
	}
}

func TestBuildReviewArtifactsFallsBackToStubOnGeneratorError(t *testing.T) {
	svc := NewService(NewMemoryStore(), config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetReviewGenerator(&fakeReviewGenerator{err: errors.New("boom")})

	artifacts, err := svc.buildReviewArtifacts(context.Background(), Session{
		ID:        "s1",
		UserID:    "u1",
		SceneType: "standup",
	}, []Utterance{{Speaker: SpeakerUser, Text: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.Generator != stubReviewGenerator || artifacts.Cost != nil {
		t.Fatalf("unexpected fallback artifacts: %+v", artifacts)
	}
	var reviewDoc map[string]any
	if err := json.Unmarshal(artifacts.ReviewJSON, &reviewDoc); err != nil {
		t.Fatal(err)
	}
	if _, ok := reviewDoc["review"]; !ok {
		t.Fatalf("missing stub full-model review wrapper: %+v", reviewDoc)
	}
	if _, ok := reviewDoc["refine"]; !ok {
		t.Fatalf("missing stub refine wrapper: %+v", reviewDoc)
	}
}

func TestGetReviewCanonicalizesLegacyReviewPayload(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   time.Minute,
		AuthJWTSecret:      config.DevJWTSecret,
		AppEnv:             "development",
		HTTPAddr:           ":0",
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, _, err := store.EndSession(context.Background(), created.SessionID, 18, nil, time.Now().UTC()); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	legacy := []byte(`{"goal_achievement":{"met":true,"note":"ok"},"issues":[],"suggestions":[],"comparisons":[{},{},{}],"generator":"ark-review-refine-v1","status":"ready","duration_sec":18}`)
	if _, err := store.MarkSessionReviewed(context.Background(), created.SessionID, legacy, time.Now().UTC()); err != nil {
		t.Fatalf("MarkSessionReviewed: %v", err)
	}

	poll, err := svc.GetReview(context.Background(), "user-1", created.SessionID)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(poll.Review, &payload); err != nil {
		t.Fatalf("decode review: %v", err)
	}
	if payload["generator"] != "ark-review-refine-v1" {
		t.Fatalf("unexpected generator: %+v", payload)
	}
	reviewSection, ok := payload["review"].(map[string]any)
	if !ok {
		t.Fatalf("missing wrapped review section: %+v", payload)
	}
	if _, ok := reviewSection["goal_achievement"]; !ok {
		t.Fatalf("legacy review fields not preserved: %+v", reviewSection)
	}
	refineSection, ok := payload["refine"].(map[string]any)
	if !ok {
		t.Fatalf("missing wrapped refine section: %+v", payload)
	}
	blocks, ok := refineSection["blocks"].([]any)
	if !ok || len(blocks) != 0 {
		t.Fatalf("expected empty refine blocks for legacy payload: %+v", refineSection)
	}
}

func TestGetReviewCanonicalizesLegacyReviewPayloadWithoutGenerator(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   time.Minute,
		AuthJWTSecret:      config.DevJWTSecret,
		AppEnv:             "development",
		HTTPAddr:           ":0",
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, _, err := store.EndSession(context.Background(), created.SessionID, 18, nil, time.Now().UTC()); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	legacy := []byte(`{"goal_achievement":{"met":true,"note":"ok"},"issues":[],"suggestions":[],"comparisons":[],"status":"ready","duration_sec":18}`)
	if _, err := store.MarkSessionReviewed(context.Background(), created.SessionID, legacy, time.Now().UTC()); err != nil {
		t.Fatalf("MarkSessionReviewed: %v", err)
	}

	poll, err := svc.GetReview(context.Background(), "user-1", created.SessionID)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(poll.Review, &payload); err != nil {
		t.Fatalf("decode review: %v", err)
	}
	if payload["generator"] != legacyReviewGenerator {
		t.Fatalf("expected legacy fallback generator, got %+v", payload)
	}
}

func TestPostMessageTextDegradeAndVoiceConflict(t *testing.T) {
	store := NewMemoryStore()
	cfg := config.Config{
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   time.Minute,
		AuthJWTSecret:      config.DevJWTSecret,
		AppEnv:             "development",
		HTTPAddr:           ":0",
	}
	svc := NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = svc.PostMessage(context.Background(), "user-1", created.SessionID, PostMessageRequest{
		Text: "hello",
	})
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Code != "CONFLICT" {
		t.Fatalf("expected voice conflict, got %v", err)
	}

	out, err := svc.PostMessage(context.Background(), "user-1", created.SessionID, PostMessageRequest{
		Text:    "hello",
		Channel: MessageChannelText,
	})
	if err != nil {
		t.Fatalf("PostMessage text: %v", err)
	}
	if out.Channel != MessageChannelText || out.Reply == "" || out.Generator != "stub-text-v1" {
		t.Fatalf("unexpected response: %+v", out)
	}

	if _, err := svc.PostMessage(context.Background(), "other", created.SessionID, PostMessageRequest{
		Text: "x", Channel: MessageChannelText,
	}); err == nil {
		t.Fatal("expected not found for non-owner")
	}
	if _, err := svc.PostMessage(context.Background(), "user-1", created.SessionID, PostMessageRequest{
		Text: "", Channel: MessageChannelText,
	}); err == nil {
		t.Fatal("expected empty text error")
	}

	if _, err := svc.Activate(context.Background(), created.SessionID); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if _, err := svc.End(context.Background(), EndRequest{
		SessionID: created.SessionID,
		Utterances: []EndUtteranceItem{
			{Seq: 1, Speaker: SpeakerAI, Text: "ready"},
		},
	}); err != nil {
		t.Fatalf("End: %v", err)
	}
	_, err = svc.PostMessage(context.Background(), "user-1", created.SessionID, PostMessageRequest{
		Text: "after end", Channel: MessageChannelText,
	})
	if !errors.As(err, &ae) || ae.Code != "CONFLICT" {
		t.Fatalf("expected closed-session conflict, got %v", err)
	}
}

func strPtr(v string) *string { return &v }

type fakeReviewGenerator struct {
	result reviewgen.Result
	err    error

	// calls counts how many times Generate was invoked. Tests assert on this
	// to verify the retry behavior introduced for #21 (B8 followup).
	calls int
	// errOnCalls lets a test fail the first N attempts and succeed afterward.
	// err takes precedence when set; errOnCalls is the alternate path used by
	// the retry-specific tests.
	errOnCalls map[int]error
}

func (f *fakeReviewGenerator) Generate(_ context.Context, _ reviewgen.Request) (reviewgen.Result, error) {
	f.calls++
	if f.errOnCalls != nil {
		if e, ok := f.errOnCalls[f.calls]; ok && e != nil {
			return reviewgen.Result{}, e
		}
	}
	if f.err != nil {
		return reviewgen.Result{}, f.err
	}
	return f.result, nil
}

// #21 (B8 followup) — retry behavior. Acceptance criterion: "失败重试 1 次"
// means the generator is invoked up to reviewRetryAttempts (2) times before
// the orchestration falls back to stub artifacts.

func TestBuildReviewArtifacts_RetriesOnceBeforeStubFallback(t *testing.T) {
	gen := &fakeReviewGenerator{err: errors.New("ark 503")}
	svc := NewService(NewMemoryStore(), config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetReviewGenerator(gen)

	artifacts, err := svc.buildReviewArtifacts(context.Background(), Session{
		ID:        "s1",
		UserID:    "u1",
		SceneType: "standup",
	}, []Utterance{{Speaker: SpeakerUser, Text: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if gen.calls != reviewRetryAttempts {
		t.Fatalf("generator called %d times, want %d (1 try + 1 retry)", gen.calls, reviewRetryAttempts)
	}
	if artifacts.Generator != stubReviewGenerator {
		t.Fatalf("expected stub fallback, got generator=%q", artifacts.Generator)
	}
	if artifacts.Cost != nil {
		t.Fatalf("stub fallback must not record cost, got %+v", artifacts.Cost)
	}
}

func TestBuildReviewArtifacts_SucceedsOnSecondAttempt(t *testing.T) {
	gen := &fakeReviewGenerator{
		errOnCalls: map[int]error{1: errors.New("transient 502")},
		result: reviewgen.Result{
			Review:    json.RawMessage(`{"goal_achievement":{},"issues":[],"suggestions":[],"comparisons":[]}`),
			Refine:    json.RawMessage(`{"blocks":[]}`),
			Generator: "ark-review-refine-v1",
			Model:     "ep-review",
			TokensIn:  100,
			TokensOut: 200,
		},
	}
	svc := NewService(NewMemoryStore(), config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetReviewGenerator(gen)

	artifacts, err := svc.buildReviewArtifacts(context.Background(), Session{
		ID:        "s1",
		UserID:    "u1",
		SceneType: "standup",
	}, []Utterance{{Speaker: SpeakerUser, Text: "follow up"}})
	if err != nil {
		t.Fatal(err)
	}
	if gen.calls != 2 {
		t.Fatalf("generator called %d times, want 2 (1 fail + 1 succeed)", gen.calls)
	}
	if artifacts.Generator != "ark-review-refine-v1" {
		t.Fatalf("expected successful generator result, got %q", artifacts.Generator)
	}
	if artifacts.Cost == nil || artifacts.Cost.TokensIn != 100 || artifacts.Cost.TokensOut != 200 {
		t.Fatalf("unexpected cost: %+v", artifacts.Cost)
	}
}

// #21 (B8 followup) — Ark Mini pricing math.
// Table-driven: 0.3 CNY/M input + 0.6 CNY/M output → 分/token.

func TestComputeCostFen(t *testing.T) {
	const (
		inputFenPerToken  = 0.03 / 1_000_000.0 // 0.3元/M
		outputFenPerToken = 0.06 / 1_000_000.0 // 0.6元/M
	)
	cases := []struct {
		name    string
		in      int
		out     int
		wantFen int
	}{
		{"zero tokens", 0, 0, 0},
		// 1M input = 0.03分 → round-half-up → 0
		{"1M input only", 1_000_000, 0, roundFen(0.03)},
		{"1M output only", 0, 1_000_000, roundFen(0.06)},
		{"1M+1M", 1_000_000, 1_000_000, roundFen(0.03 + 0.06)},
		{"100k+200k", 100_000, 200_000, roundFen(100_000*inputFenPerToken + 200_000*outputFenPerToken)},
		{"negative clamps to zero", -100, -100, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeCostFen(tc.in, tc.out)
			if got != tc.wantFen {
				t.Fatalf("computeCostFen(%d, %d) = %d, want %d", tc.in, tc.out, got, tc.wantFen)
			}
		})
	}
}

// roundFen matches the int(fen + 0.5) rounding in computeCostFen so test
// expected values can be written as plain floats instead of int casts on
// float arithmetic (which Go forbids in const contexts).
func roundFen(f float64) int {
	if f < 0 {
		return 0
	}
	return int(f + 0.5)
}

// #21 (B8 followup) — buildCostLog must produce an aicost.Log with the
// canonical task_type, a non-empty ID, and the right cost in fen. UserID
// falls back to session.UserID when the RecordRequest leaves it blank.

func TestBuildCostLog_FieldMapping(t *testing.T) {
	session := Session{ID: "sess-1", UserID: "user-7"}
	artifacts := reviewArtifacts{
		Generator: "ark-review-refine-v1",
		Cost: &aicost.RecordRequest{
			TaskType:  arkReviewTaskType, // what callers send; buildCostLog must canonicalize
			Model:     "ep-review",
			TokensIn:  1_000,
			TokensOut: 2_000,
			AudioSec:  0,
			CostFen:   0,
			UserID:    "", // blank → falls back to session.UserID
		},
	}
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	log := buildCostLog(session, artifacts, at)

	if log.ID == "" {
		t.Fatal("expected non-empty log ID")
	}
	if log.TaskType != arkReviewTaskType {
		t.Fatalf("TaskType = %q, want %q", log.TaskType, arkReviewTaskType)
	}
	if log.Model != "ep-review" {
		t.Fatalf("Model = %q", log.Model)
	}
	if log.TokensIn != 1000 || log.TokensOut != 2000 {
		t.Fatalf("tokens mismatch: %+v", log)
	}
	if !log.CreatedAt.Equal(at) {
		t.Fatalf("CreatedAt = %v, want %v", log.CreatedAt, at)
	}
	if log.UserID == nil || *log.UserID != "user-7" {
		t.Fatalf("UserID fallback failed: %+v", log.UserID)
	}
	wantFen := computeCostFen(1000, 2000)
	if log.CostFen != wantFen {
		t.Fatalf("CostFen = %d, want %d", log.CostFen, wantFen)
	}
}

func TestBuildCostLog_KeepsExplicitUserID(t *testing.T) {
	session := Session{ID: "sess-1", UserID: "user-7"}
	artifacts := reviewArtifacts{
		Cost: &aicost.RecordRequest{
			TaskType: arkReviewTaskType,
			UserID:   "user-9",
			TokensIn: 100, TokensOut: 200,
		},
	}
	log := buildCostLog(session, artifacts, time.Now().UTC())
	if log.UserID == nil || *log.UserID != "user-9" {
		t.Fatalf("UserID should be preserved when explicit, got %+v", log.UserID)
	}
}

func TestNullableUserID(t *testing.T) {
	if nullableUserID("") != nil {
		t.Fatal("expected nil for empty id")
	}
	if nullableUserID("   ") != nil {
		t.Fatal("expected nil for whitespace id")
	}
	id := "abc"
	if got := nullableUserID(id); got == nil || *got != "abc" {
		t.Fatalf("unexpected pointer: %+v", got)
	}
}

// #21 (B8 followup) — atomic review+cost on the memory store. Both writes
// must land together; idempotent retry must not double-record the cost log.

func TestMarkSessionReviewedWithCost_Memory_BothWritesLand(t *testing.T) {
	store := NewMemoryStore()
	svc := newReviewServiceForStore(t, store)
	created := createEndedSession(t, svc)

	review := []byte(`{"goal_achievement":{"met":true,"note":"ok"},"issues":[],"suggestions":[],"comparisons":[]}`)
	costLog := aicost.Log{
		ID:        "cost-1",
		TaskType:  arkReviewTaskType,
		Model:     "ep-review",
		TokensIn:  100,
		TokensOut: 200,
		CreatedAt: time.Now().UTC(),
	}
	updated, err := store.MarkSessionReviewedWithCost(context.Background(), created.SessionID, review, time.Now().UTC(), costLog)
	if err != nil {
		t.Fatalf("MarkSessionReviewedWithCost: %v", err)
	}
	if updated.Status != StatusReviewed {
		t.Fatalf("status = %q, want reviewed", updated.Status)
	}
	if !bytesContains(updated.ReviewJSON, []byte(`"met":true`)) {
		t.Fatalf("review_json not committed: %s", updated.ReviewJSON)
	}
	stored, ok := store.costLogs[costLog.ID]
	if !ok {
		t.Fatal("expected cost log to be recorded atomically")
	}
	if stored.Model != "ep-review" || stored.TokensIn != 100 {
		t.Fatalf("cost log fields wrong: %+v", stored)
	}
}

func TestMarkSessionReviewedWithCost_Memory_IdempotentNoDoubleBill(t *testing.T) {
	store := NewMemoryStore()
	svc := newReviewServiceForStore(t, store)
	created := createEndedSession(t, svc)

	review := []byte(`{"goal_achievement":{"met":true},"issues":[],"suggestions":[],"comparisons":[]}`)
	costLog := aicost.Log{
		ID:       "cost-dup",
		TaskType: arkReviewTaskType,
		TokensIn: 10, TokensOut: 20,
		CreatedAt: time.Now().UTC(),
	}
	if _, err := store.MarkSessionReviewedWithCost(context.Background(), created.SessionID, review, time.Now().UTC(), costLog); err != nil {
		t.Fatal(err)
	}
	// Second call: session is already reviewed; cost log must NOT be written again.
	if _, err := store.MarkSessionReviewedWithCost(context.Background(), created.SessionID, review, time.Now().UTC(), costLog); err != nil {
		t.Fatal(err)
	}
	if len(store.costLogs) != 1 {
		t.Fatalf("expected exactly 1 cost log entry, got %d (idempotent retry must not double-bill)", len(store.costLogs))
	}
}

func TestMarkSessionReviewedWithCost_Memory_RejectsNonEnded(t *testing.T) {
	store := NewMemoryStore()
	svc := newReviewServiceForStore(t, store)
	created := createEndedSession(t, svc)
	// Force the session back to a non-ended status to simulate "not ready for review".
	raw, ok := store.sessions[created.SessionID]
	if !ok {
		t.Fatal("session missing")
	}
	raw.Status = StatusCreated
	store.sessions[created.SessionID] = raw

	_, err := store.MarkSessionReviewedWithCost(context.Background(), created.SessionID, []byte(`{}`), time.Now().UTC(), aicost.Log{ID: "x", TaskType: arkReviewTaskType, CreatedAt: time.Now().UTC()})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for non-ended session, got %v", err)
	}
}

// --- helpers for the new tests ---

func newReviewServiceForStore(t *testing.T, store *MemoryStore) *Service {
	t.Helper()
	cfg := config.Config{
		HTTPAddr:           ":0",
		AppEnv:             "development",
		AuthJWTSecret:      config.DevJWTSecret,
		VoiceGatewayWSSURL: "ws://example.test/v1/voice",
		SessionTicketTTL:   60 * time.Second,
	}
	return NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func createEndedSession(t *testing.T, svc *Service) CreateResponse {
	t.Helper()
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, _, err := svc.store.EndSession(context.Background(), created.SessionID, 30, nil, time.Now().UTC()); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	return created
}

func bytesContains(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
