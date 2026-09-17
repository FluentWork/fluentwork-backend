package session

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
)

// P0-2 acceptance 1+2: a generation failure logs its classification and the
// model's raw response, so a JSON truncation is distinguishable from an empty
// session without reading the worker's stdout by hand.
func TestBuildReviewArtifacts_LogsRawResponseOnFailure(t *testing.T) {
	var buf bytes.Buffer
	svc := NewService(NewMemoryStore(), config.Config{}, slog.New(slog.NewTextHandler(&buf, nil)))
	svc.SetReviewGenerator(&fakeReviewGenerator{err: &reviewgen.GenerateError{
		Kind:         reviewgen.FailureTruncatedJSON,
		Err:          errors.New("decode generated json: unexpected end of JSON input"),
		SessionID:    "s1",
		FinishReason: "length",
		RawContent:   `{"review":{"goal_achievement":{"met":true`,
	}})

	artifacts, err := svc.buildReviewArtifacts(context.Background(), Session{
		ID:        "s1",
		UserID:    "u1",
		SceneType: "standup",
	}, []Utterance{{Speaker: SpeakerUser, Text: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.Generator != stubReviewGenerator {
		t.Fatalf("generator = %q, want stub fallback", artifacts.Generator)
	}

	logs := buf.String()
	for _, want := range []string{
		"failure_kind=truncated_json",
		"finish_reason=length",
		"raw_response=",
		`goal_achievement`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("log missing %q:\n%s", want, logs)
		}
	}
}

// An unclassified error still names a kind, so dashboards never see an empty
// field when a provider change introduces a new failure shape.
func TestBuildReviewArtifacts_UnclassifiedFailureStillLabelled(t *testing.T) {
	var buf bytes.Buffer
	svc := NewService(NewMemoryStore(), config.Config{}, slog.New(slog.NewTextHandler(&buf, nil)))
	svc.SetReviewGenerator(&fakeReviewGenerator{err: errors.New("boom")})

	if _, err := svc.buildReviewArtifacts(context.Background(), Session{
		ID: "s1", UserID: "u1", SceneType: "standup",
	}, []Utterance{{Speaker: SpeakerUser, Text: "hello"}}); err != nil {
		t.Fatal(err)
	}
	if logs := buf.String(); !strings.Contains(logs, "failure_kind=unclassified") {
		t.Fatalf("log missing failure_kind=unclassified:\n%s", logs)
	}
}

// P0-2 acceptance 2: an empty session is a correct rejection. It is logged at
// INFO, and it is not retried — the second attempt would see the same empty
// transcript and return the same answer.
func TestBuildReviewArtifacts_EmptySessionIsNotRetried(t *testing.T) {
	var buf bytes.Buffer
	gen := &fakeReviewGenerator{err: &reviewgen.GenerateError{
		Kind:      reviewgen.FailureEmptySession,
		Err:       errors.New("transcript is required"),
		SessionID: "s1",
	}}
	svc := NewService(NewMemoryStore(), config.Config{}, slog.New(slog.NewTextHandler(&buf, nil)))
	svc.SetReviewGenerator(gen)

	artifacts, err := svc.buildReviewArtifacts(context.Background(), Session{
		ID: "s1", UserID: "u1", SceneType: "standup",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.Generator != stubReviewGenerator {
		t.Fatalf("generator = %q, want stub fallback", artifacts.Generator)
	}
	if gen.calls != 1 {
		t.Fatalf("generator calls = %d, want 1 (no retry for empty session)", gen.calls)
	}
	logs := buf.String()
	if !strings.Contains(logs, "review generator skipped empty session") {
		t.Fatalf("log missing skip message:\n%s", logs)
	}
	if strings.Contains(logs, "level=WARN") {
		t.Fatalf("empty session must not warn:\n%s", logs)
	}
}
