package session

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
)

func timeNow() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }

func fakeRefineResult(t *testing.T) reviewgen.Result {
	t.Helper()
	return reviewgen.Result{
		Review:    json.RawMessage(`{"goal_achievement":{},"issues":[],"suggestions":[],"comparisons":[{},{},{}]}`),
		Refine:    json.RawMessage(`{"blocks":[]}`),
		Generator: "ark-review-refine-v1",
	}
}

func newRescueTestService(t *testing.T) (*Service, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	svc := NewService(store, config.Config{SessionTicketTTL: 60_000_000_000}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetReviewGenerator(&fakeReviewGenerator{})
	return svc, store
}

// P1-1 acceptance: the gateway's rescue events reach the store with the
// transcript, in order, and survive the round trip the review pipeline reads.
func TestEnd_PersistsRescueEventsWithTranscript(t *testing.T) {
	svc, store := newRescueTestService(t)
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.End(context.Background(), EndRequest{
		SessionID:   created.SessionID,
		DurationSec: 20,
		Utterances: []EndUtteranceItem{
			{Seq: 1, Speaker: SpeakerAI, Text: "What is the risk?"},
			{Seq: 2, Speaker: SpeakerUser, Text: "I was going to say that the deploy is"},
		},
		RescueEvents: []RescueEventItem{
			{Seq: 1, TurnID: "turn-1", Level: 3, Path: RescuePathIncomplete, Ladder: "The deploy is blocked on the migration.", UserOpened: true, Anchor: "I was going to say that the deploy is"},
			{Seq: 2, TurnID: "turn-2", Level: 1, Path: RescuePathSilent},
		},
	}); err != nil {
		t.Fatalf("End: %v", err)
	}

	events, err := store.ListRescueEvents(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("ListRescueEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	first := events[0]
	if first.Path != RescuePathIncomplete || first.Level != 3 || !first.UserOpened {
		t.Fatalf("first event = %+v", first)
	}
	if first.Anchor != "I was going to say that the deploy is" {
		t.Fatalf("anchor = %q", first.Anchor)
	}
	if first.UserID != "user-1" || first.SessionID != created.SessionID {
		t.Fatalf("ownership not stamped: %+v", first)
	}
	second := events[1]
	if second.Path != RescuePathSilent || second.UserOpened || second.Anchor != "" {
		t.Fatalf("silent event = %+v", second)
	}
}

// §5.2.2: a user who never spoke leaves no anchor. A contradictory report is
// coerced rather than trusted, and never costs the session its transcript.
func TestEnd_DropsAnchorForUserWhoNeverSpoke(t *testing.T) {
	svc, store := newRescueTestService(t)
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.End(context.Background(), EndRequest{
		SessionID: created.SessionID,
		Utterances: []EndUtteranceItem{
			{Seq: 1, Speaker: SpeakerAI, Text: "What is the risk?"},
		},
		RescueEvents: []RescueEventItem{
			{Seq: 1, Level: 2, Path: RescuePathSilent, UserOpened: false, Anchor: "an anchor nobody said"},
		},
	}); err != nil {
		t.Fatalf("End: %v", err)
	}
	events, err := store.ListRescueEvents(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("ListRescueEvents: %v", err)
	}
	if len(events) != 1 || events[0].Anchor != "" {
		t.Fatalf("anchor must be dropped: %+v", events)
	}
}

// A malformed ladder is dropped on its own; the session still ends.
func TestEnd_DropsMalformedRescueEvents(t *testing.T) {
	svc, store := newRescueTestService(t)
	created, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.End(context.Background(), EndRequest{
		SessionID: created.SessionID,
		Utterances: []EndUtteranceItem{
			{Seq: 1, Speaker: SpeakerAI, Text: "What is the risk?"},
		},
		RescueEvents: []RescueEventItem{
			{Seq: 1, Level: 9, Path: RescuePathSilent},
			{Seq: 2, Level: 2, Path: "telepathy"},
			{Seq: 3, Level: 1, Path: RescuePathSilent},
			{Seq: 3, Level: 2, Path: RescuePathSilent},
			{Seq: 4, Level: 1, Path: ""},
		},
	}); err != nil {
		t.Fatalf("End: %v", err)
	}
	events, err := store.ListRescueEvents(context.Background(), created.SessionID)
	if err != nil {
		t.Fatalf("ListRescueEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want the two well-formed ones", events)
	}
	if events[0].Seq != 3 || events[1].Seq != 4 {
		t.Fatalf("surviving seqs = %d, %d", events[0].Seq, events[1].Seq)
	}
	// An unlabelled ladder is the silence detector's, which is the default
	// trigger (PRD §5.4.1).
	if events[1].Path != RescuePathSilent {
		t.Fatalf("path = %q, want silent default", events[1].Path)
	}
}

// P1-1's whole point: the ladders reach the generator as refine's second input.
func TestBuildReviewArtifacts_PassesRescueEventsToGenerator(t *testing.T) {
	svc, store := newRescueTestService(t)
	gen := &fakeReviewGenerator{result: fakeRefineResult(t)}
	svc.SetReviewGenerator(gen)

	created, err := svc.Create(context.Background(), "u1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	session := Session{ID: created.SessionID, UserID: "u1", SceneType: "standup"}
	if _, _, _, err := store.EndSession(context.Background(), session.ID, 12, []Utterance{
		{ID: "u-1", Seq: 1, Speaker: SpeakerUser, Text: "hello"},
	}, []RescueEvent{
		{
			ID: "r-1", SessionID: session.ID, UserID: "u1", Seq: 1, TurnID: "turn-1", Level: 3,
			Path: RescuePathIncomplete, Ladder: "The deploy is blocked.", UserOpened: true,
			Anchor: "the deploy is",
		},
	}, timeNow(), nil); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	if _, err := svc.buildReviewArtifacts(context.Background(), session, []Utterance{
		{Speaker: SpeakerUser, Text: "hello"},
	}); err != nil {
		t.Fatalf("buildReviewArtifacts: %v", err)
	}
	if len(gen.requests) != 1 {
		t.Fatalf("requests = %d", len(gen.requests))
	}
	events := gen.requests[0].StuckEvents
	if len(events) != 1 {
		t.Fatalf("stuck events = %+v", events)
	}
	if events[0].Path != RescuePathIncomplete || events[0].Anchor != "the deploy is" ||
		events[0].Level != 3 || !events[0].UserOpened {
		t.Fatalf("stuck event = %+v", events[0])
	}
}
