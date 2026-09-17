package corpus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

func feedbackService(t *testing.T) (*Service, *MemoryStore, time.Time) {
	t.Helper()
	store := NewMemoryStore()
	svc := NewService(store, nil)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	seedBlock(t, store, "block-1", "user-1", "推动上线", "Let's ship it.")
	return svc, store, now
}

// The button is a signal, not a counter: tapping the same reason twice records
// one signal.
func TestRecordFeedback_Idempotent(t *testing.T) {
	svc, store, _ := feedbackService(t)
	first, err := svc.RecordFeedback(context.Background(), FeedbackRequest{
		UserID: "user-1", BlockID: "block-1", Reason: FeedbackNotIdiomatic,
	})
	if err != nil {
		t.Fatalf("RecordFeedback: %v", err)
	}
	if !first.Recorded {
		t.Fatalf("first tap must record: %+v", first)
	}
	second, err := svc.RecordFeedback(context.Background(), FeedbackRequest{
		UserID: "user-1", BlockID: "block-1", Reason: FeedbackNotIdiomatic,
	})
	if err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if second.Recorded {
		t.Fatalf("second tap must be a no-op: %+v", second)
	}
	counts, err := store.CountFeedback(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("CountFeedback: %v", err)
	}
	if counts[FeedbackNotIdiomatic] != 1 {
		t.Fatalf("counts = %+v", counts)
	}
}

// The same block can be disliked for two different reasons — that difference is
// what prompt work reads.
func TestRecordFeedback_MultipleReasonsPerBlock(t *testing.T) {
	svc, store, _ := feedbackService(t)
	for _, reason := range []string{FeedbackNotIdiomatic, FeedbackNotUseful} {
		if _, err := svc.RecordFeedback(context.Background(), FeedbackRequest{
			UserID: "user-1", BlockID: "block-1", Reason: reason,
		}); err != nil {
			t.Fatalf("RecordFeedback(%s): %v", reason, err)
		}
	}
	counts, err := store.CountFeedback(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("CountFeedback: %v", err)
	}
	if counts[FeedbackNotIdiomatic] != 1 || counts[FeedbackNotUseful] != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	summary, err := svc.FeedbackSummary(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("FeedbackSummary: %v", err)
	}
	if len(summary) != 3 {
		t.Fatalf("summary = %+v, want one bucket per reason", summary)
	}
	var total int
	for _, bucket := range summary {
		total += bucket.Count
	}
	if total != 2 {
		t.Fatalf("summary total = %d", total)
	}
}

func TestRecordFeedback_RejectsBadInput(t *testing.T) {
	svc, store, _ := feedbackService(t)
	seedBlock(t, store, "block-gone", "user-1", "x", "y")
	if _, err := store.SoftDeleteAllForUser(context.Background(), "user-1", time.Now().UTC()); err != nil {
		t.Fatalf("wipe: %v", err)
	}

	cases := []struct {
		name string
		req  FeedbackRequest
		want int
	}{
		{"missing user", FeedbackRequest{BlockID: "block-1", Reason: FeedbackNotIdiomatic}, 401},
		{"missing block", FeedbackRequest{UserID: "user-1", Reason: FeedbackNotIdiomatic}, 400},
		{"open reason set", FeedbackRequest{UserID: "user-1", BlockID: "block-1", Reason: "meh"}, 400},
		{"unknown block", FeedbackRequest{UserID: "user-1", BlockID: "nope", Reason: FeedbackNotIdiomatic}, 404},
		{"other user's block", FeedbackRequest{UserID: "user-2", BlockID: "block-1", Reason: FeedbackNotIdiomatic}, 404},
		{"deleted block", FeedbackRequest{UserID: "user-1", BlockID: "block-gone", Reason: FeedbackNotIdiomatic}, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.RecordFeedback(context.Background(), tc.req)
			var apiErr *apierr.Error
			if !errors.As(err, &apiErr) || apiErr.HTTPStatus != tc.want {
				t.Fatalf("err = %v, want %d", err, tc.want)
			}
		})
	}
}

// Feedback is the user's own judgement, so A4's wipe takes it with the blocks.
func TestPrivacyWipe_CoversFeedback(t *testing.T) {
	svc, store, now := feedbackService(t)
	seedBlock(t, store, "block-2", "user-1", "同步进度", "I'll touch base.")
	if _, err := svc.RecordFeedback(context.Background(), FeedbackRequest{
		UserID: "user-1", BlockID: "block-1", Reason: FeedbackNotIdiomatic,
	}); err != nil {
		t.Fatalf("RecordFeedback: %v", err)
	}
	wiper := PrivacyWiper{Store: store}
	affected, err := wiper.Wipe(context.Background(), "user-1", now)
	if err != nil {
		t.Fatalf("Wipe: %v", err)
	}
	// Two blocks plus one feedback row.
	if affected != 3 {
		t.Fatalf("wiped = %d, want blocks + feedback", affected)
	}
	if counts, _ := store.CountFeedback(context.Background(), "user-1"); len(counts) != 0 {
		t.Fatalf("feedback survived the wipe: %+v", counts)
	}
	if _, err := wiper.Restore(context.Background(), "user-1"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if counts, _ := store.CountFeedback(context.Background(), "user-1"); counts[FeedbackNotIdiomatic] != 1 {
		t.Fatalf("feedback not restored: %+v", counts)
	}
}

func TestFeedbackMetrics(t *testing.T) {
	svc, _, _ := feedbackService(t)
	if _, err := svc.RecordFeedback(context.Background(), FeedbackRequest{
		UserID: "user-1", BlockID: "block-1", Reason: FeedbackWrongMeaning,
	}); err != nil {
		t.Fatalf("RecordFeedback: %v", err)
	}
	metrics := PrometheusMetrics()
	if !strings.Contains(metrics, "corpus_block_feedback_total") ||
		!strings.Contains(metrics, FeedbackWrongMeaning) {
		t.Fatalf("metrics missing the reflux counter:\n%s", metrics)
	}
}
