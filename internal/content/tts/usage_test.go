package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type stubUsageRecorder struct {
	calls   int
	voiceID string
	chars   int
	err     error
}

func (s *stubUsageRecorder) RecordTTSUsage(_ context.Context, voiceID string, chars int) error {
	s.calls++
	s.voiceID = voiceID
	s.chars = chars
	return s.err
}

func newTTSEngineWithRecorder(t *testing.T, recorder UsageRecorder) (*gin.Engine, *recordingProvider) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	provider := &recordingProvider{}
	handler := NewHandler(provider)
	handler.SetUsageRecorder(recorder)
	RegisterInternalRoutes(engine.Group("/internal/v1"), handler, testInternalToken)
	return engine, provider
}

func postSynthesize(t *testing.T, engine *gin.Engine, text string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(SynthesizeRequest{Text: text, VoiceID: VoiceIDAIMaleTech})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/tts/synthesize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", testInternalToken)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// P1-5: synthesis must reach the ledger, billed by character — the unit the
// vendor charges in.
func TestPostSynthesize_RecordsUsage(t *testing.T) {
	recorder := &stubUsageRecorder{}
	engine, _ := newTTSEngineWithRecorder(t, recorder)

	// Mixed scripts on purpose: the count is runes, not bytes.
	rec := postSynthesize(t, engine, "hi 你好")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if recorder.calls != 1 {
		t.Fatalf("recorder calls = %d", recorder.calls)
	}
	if recorder.chars != 5 {
		t.Fatalf("chars = %d, want 5 runes", recorder.chars)
	}
	if recorder.voiceID != VoiceIDAIMaleTech {
		t.Fatalf("voice = %q", recorder.voiceID)
	}
}

// Usage we failed to file is a bookkeeping problem, not a reason to refuse to
// speak.
func TestPostSynthesize_RecordingFailureStillAnswers(t *testing.T) {
	recorder := &stubUsageRecorder{err: errors.New("ledger down")}
	engine, _ := newTTSEngineWithRecorder(t, recorder)

	rec := postSynthesize(t, engine, "hello")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if recorder.calls != 1 {
		t.Fatalf("recorder calls = %d", recorder.calls)
	}
}

// Without a recorder the endpoint behaves exactly as it did before P1-5: it
// synthesizes, and nothing is accounted.
func TestPostSynthesize_NoRecorderIsSupported(t *testing.T) {
	engine, _ := newTTSEngineWithRecorder(t, nil)
	rec := postSynthesize(t, engine, "hello")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
