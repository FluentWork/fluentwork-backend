package voicegateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

// P1-2: a detected hit has to reach the ledger, or "用上过 N 次" stays 0 forever.
//
// This drives app-server's real hits route over a real HTTP round trip rather
// than a stub recorder, because the bug being fixed was not in any one piece:
// the endpoint could credit a hit, the detector could detect one, and the
// emitter could badge one — nothing joined them up, and every unit test on each
// piece stayed green. Only an end-to-end path can fail when the wire is missing.
func TestHitWriteBack_DetectedHitCreditsTheBlock(t *testing.T) {
	store := corpus.NewMemoryStore()
	seedAt := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if _, err := store.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID:             "block-1",
		UserID:         "u1",
		IntentZH:       "推动上线",
		ExpressionEN:   "Let's ship it today.",
		AnchorUserSaid: "let's ship it",
		SceneTag:       "review",
		FunctionTag:    "commit",
		State:          corpus.StateNew,
		NextDueAt:      seedAt.Add(-time.Hour), // overdue: a success must move it
		CreatedAt:      seedAt,
		UpdatedAt:      seedAt,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	corpus.RegisterInternalRoutes(
		engine.Group("/internal/v1"),
		corpus.NewHandler(corpus.NewService(store, nil), nil),
		"tok",
	)
	server := httptest.NewServer(engine)
	defer server.Close()

	detector := session.NewHitDetector(newStubSource(session.BlockCandidate{
		ID:           "block-1",
		ExpressionEN: "Let's ship it today.",
		IntentZH:     "推动上线",
	}))
	detectedAt := time.Date(2026, 9, 18, 9, 30, 0, 0, time.UTC)
	emitter := NewBadgeEmitter(detector, nil, BadgeEmitterOptions{
		Recorder: &HTTPSessionClient{BaseURL: server.URL, Token: "tok"},
		Now:      func() time.Time { return detectedAt },
	})

	conn := fakeWSConn{}
	emitter.EmitSync(context.Background(), &conn, "u1", "s1", "t1", "Let's ship it today.")

	if len(conn.written) != 1 {
		t.Fatalf("badge frames written = %d, want 1", len(conn.written))
	}
	block, err := store.GetBlock(context.Background(), "u1", "block-1")
	if err != nil {
		t.Fatalf("read back block: %v", err)
	}
	if block.RealUseCount != 1 {
		t.Fatalf("real_use_count = %d, want 1 — the hit reached the badge but not the ledger", block.RealUseCount)
	}
	if block.TotalUses != 1 {
		t.Fatalf("total_uses = %d, want 1", block.TotalUses)
	}
	if block.LastUsedAt == nil || !block.LastUsedAt.Equal(detectedAt) {
		t.Fatalf("last_used_at = %v, want the detection time %v", block.LastUsedAt, detectedAt)
	}
	// 视同一次成功: the shared ladder must advance, or a block the learner
	// keeps using in real conversations would keep coming back as if unused.
	if !block.NextDueAt.After(seedAt) {
		t.Fatalf("next_due_at = %v, want it pushed past the seeded %v", block.NextDueAt, seedAt)
	}
	if block.SuccessStreak == 0 {
		t.Fatal("success_streak = 0: a hit must count as one successful recall (PRD §5.2.3)")
	}
}

// A ledger write that fails must not cost the learner their badge: the frame is
// the product, the count is the statistic. The failure is counted, not raised.
func TestHitWriteBack_RecorderFailureStillWritesTheBadge(t *testing.T) {
	detector := session.NewHitDetector(newStubSource(session.BlockCandidate{
		ID: "block-1", ExpressionEN: "Let's ship it today.", IntentZH: "推动上线",
	}))
	recorder := &failingRecorder{}
	emitter := NewBadgeEmitter(detector, nil, BadgeEmitterOptions{Recorder: recorder})

	conn := fakeWSConn{}
	emitter.EmitSync(context.Background(), &conn, "u1", "s1", "t1", "Let's ship it today.")

	if len(conn.written) != 1 {
		t.Fatalf("badge frames written = %d, want 1 even when the ledger write fails", len(conn.written))
	}
	if got := recorder.calls.Load(); got != 1 {
		t.Fatalf("recorder calls = %d, want 1", got)
	}
	if got := emitter.Stats().RecordErrors; got != 1 {
		t.Fatalf("RecordErrors = %d, want 1", got)
	}
}

// A nil recorder is a supported deployment (dev-echo has no ledger behind it),
// and it must behave exactly as before the seam existed.
func TestHitWriteBack_NoRecorderIsSilent(t *testing.T) {
	detector := session.NewHitDetector(newStubSource(session.BlockCandidate{
		ID: "block-1", ExpressionEN: "Let's ship it today.", IntentZH: "推动上线",
	}))
	emitter := NewBadgeEmitter(detector, nil, BadgeEmitterOptions{})

	conn := fakeWSConn{}
	emitter.EmitSync(context.Background(), &conn, "u1", "s1", "t1", "Let's ship it today.")

	if len(conn.written) != 1 {
		t.Fatalf("badge frames written = %d, want 1", len(conn.written))
	}
	if got := emitter.Stats().RecordErrors; got != 0 {
		t.Fatalf("RecordErrors = %d, want 0 when no recorder is wired", got)
	}
}

type failingRecorder struct{ calls atomic.Int64 }

func (f *failingRecorder) RecordHit(context.Context, string, string, string, string, int64) error {
	f.calls.Add(1)
	return errors.New("ledger unavailable")
}

// The report carries the detection time, not the time the report happened to be
// sent: that timestamp decides which day's schedule the recall lands on.
func TestHTTPSessionClient_RecordHitSendsTheDetectionTime(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/voicegateway/hits" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Internal-Token"); got != "tok" {
			t.Errorf("token = %q", got)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"recorded_count":1}`))
	}))
	defer server.Close()

	client := &HTTPSessionClient{BaseURL: server.URL, Token: "tok"}
	at := time.Date(2026, 9, 18, 9, 30, 0, 0, time.UTC)
	if err := client.RecordHit(context.Background(), "u1", "s1", "t1", "block-1", at.UnixMilli()); err != nil {
		t.Fatalf("RecordHit: %v", err)
	}
	for _, want := range []string{
		`"user_id":"u1"`, `"session_id":"s1"`, `"turn_id":"t1"`, `"block_id":"block-1"`,
		fmt.Sprintf(`"detected_at_ms":%d`, at.UnixMilli()),
	} {
		if !strings.Contains(string(gotBody), want) {
			t.Fatalf("body %s missing %s", gotBody, want)
		}
	}
}
