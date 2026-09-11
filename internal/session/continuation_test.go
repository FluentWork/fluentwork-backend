package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/config"
)

func continuationService() *Service {
	return NewService(NewMemoryStore(), config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// endedSession creates a session for userID, ends it with the given transcript,
// and returns its id. Ending is what persists utterances; a session that never
// ended has nothing to continue from.
func endedSession(t *testing.T, svc *Service, userID string, turns []EndUtteranceItem) string {
	t.Helper()
	created, err := svc.Create(context.Background(), userID, CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create(%s): %v", userID, err)
	}
	if _, err := svc.End(context.Background(), EndRequest{
		SessionID:  created.SessionID,
		Reason:     "user_ended",
		Utterances: turns,
	}); err != nil {
		t.Fatalf("End: %v", err)
	}
	return created.SessionID
}

func turnsOf(n int) []EndUtteranceItem {
	out := make([]EndUtteranceItem, 0, n)
	for i := 1; i <= n; i++ {
		speaker := "user"
		if i%2 == 0 {
			speaker = "ai"
		}
		out = append(out, EndUtteranceItem{
			Seq:     i,
			Speaker: speaker,
			Text:    "turn-" + strconv.Itoa(i),
		})
	}
	return out
}

func TestContinuationContextReturnsTheTailNotTheHead(t *testing.T) {
	t.Parallel()
	svc := continuationService()
	previous := endedSession(t, svc, "user-1", turnsOf(5))
	current, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.ContinuationContext(context.Background(), current.SessionID, previous, 2)
	if err != nil {
		t.Fatalf("ContinuationContext: %v", err)
	}
	if len(got.Utterances) != 2 {
		t.Fatalf("utterances = %d, want 2", len(got.Utterances))
	}
	// "Where we left off" is the end of the conversation. Returning the first
	// two turns would look identical in a one-turn test and be wrong in every
	// real one.
	if got.Utterances[0].Text != "turn-4" || got.Utterances[1].Text != "turn-5" {
		t.Fatalf("got %q,%q; want turn-4,turn-5", got.Utterances[0].Text, got.Utterances[1].Text)
	}
}

// The security-critical one. `previous_session_id` comes from a frame the
// client wrote, so without this check any authenticated user could name any
// other user's session and have its transcript pulled into their own model
// context — and then simply ask what it said.
func TestContinuationContextRefusesAnotherUsersSession(t *testing.T) {
	t.Parallel()
	svc := continuationService()
	victim := endedSession(t, svc, "user-victim", turnsOf(3))
	attacker, err := svc.Create(context.Background(), "user-attacker", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.ContinuationContext(context.Background(), attacker.SessionID, victim, 0)
	if err == nil {
		t.Fatalf("expected refusal, got %+v", got)
	}
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "NOT_FOUND" {
		t.Fatalf("err = %v, want NOT_FOUND", err)
	}
	if len(got.Utterances) != 0 {
		t.Fatalf("refusal leaked %d utterances", len(got.Utterances))
	}
}

// A wrong-owner answer must be indistinguishable from a missing one: telling a
// caller that a session exists but is not theirs is itself the leak.
func TestContinuationContextRefusalIsIndistinguishableFromAMiss(t *testing.T) {
	t.Parallel()
	svc := continuationService()
	victim := endedSession(t, svc, "user-victim", turnsOf(3))
	attacker, err := svc.Create(context.Background(), "user-attacker", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, wrongOwner := svc.ContinuationContext(context.Background(), attacker.SessionID, victim, 0)
	_, missing := svc.ContinuationContext(context.Background(), attacker.SessionID, "no-such-session", 0)

	var a, b *apierr.Error
	if !errors.As(wrongOwner, &a) || !errors.As(missing, &b) {
		t.Fatalf("both should be api errors: %v / %v", wrongOwner, missing)
	}
	if a.Code != b.Code || a.Message != b.Message || a.HTTPStatus != b.HTTPStatus {
		t.Fatalf("answers differ: %+v vs %+v", a, b)
	}
}

// This text goes into a system prompt verbatim, so an unbounded `limit` off a
// client-written frame is a way to make one session's opening turn arbitrarily
// expensive.
func TestContinuationContextClampsTheLimit(t *testing.T) {
	t.Parallel()
	svc := continuationService()
	previous := endedSession(t, svc, "user-1", turnsOf(25))
	current, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.ContinuationContext(context.Background(), current.SessionID, previous, 10_000)
	if err != nil {
		t.Fatalf("ContinuationContext: %v", err)
	}
	if len(got.Utterances) != continuationMaxLimit {
		t.Fatalf("utterances = %d, want the cap %d", len(got.Utterances), continuationMaxLimit)
	}

	// Zero means "unspecified", not "none" — a caller that omits the field
	// wants the default, and an empty answer would silently disable the
	// feature for every client that does not set it.
	got, err = svc.ContinuationContext(context.Background(), current.SessionID, previous, 0)
	if err != nil {
		t.Fatalf("ContinuationContext: %v", err)
	}
	if len(got.Utterances) != continuationDefaultLimit {
		t.Fatalf("utterances = %d, want the default %d", len(got.Utterances), continuationDefaultLimit)
	}
}

// A session with nothing said in it is a normal answer, not an error: the user
// can open a room, say nothing, close it, and then continue from it.
func TestContinuationContextOnAnEmptySessionIsEmptyNotAnError(t *testing.T) {
	t.Parallel()
	svc := continuationService()
	previous := endedSession(t, svc, "user-1", nil)
	current, err := svc.Create(context.Background(), "user-1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.ContinuationContext(context.Background(), current.SessionID, previous, 0)
	if err != nil {
		t.Fatalf("ContinuationContext: %v", err)
	}
	if len(got.Utterances) != 0 {
		t.Fatalf("utterances = %d, want 0", len(got.Utterances))
	}
}

func TestContinuationContextRequiresBothIDs(t *testing.T) {
	t.Parallel()
	svc := continuationService()
	if _, err := svc.ContinuationContext(context.Background(), "", "some-session", 0); err == nil {
		t.Fatal("expected an error for a missing current session id")
	}
	if _, err := svc.ContinuationContext(context.Background(), "some-session", "", 0); err == nil {
		t.Fatal("expected an error for a missing previous session id")
	}
}
