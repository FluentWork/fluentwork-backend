package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

const testInternalToken = "test-internal-token"

type recordingProvider struct {
	mockProvider
	lastVoice VoiceConfig
	lastText  string
}

func (r *recordingProvider) Stream(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
	r.lastText = text
	r.lastVoice = voice
	r.chunks = []AudioChunk{
		{Data: []byte("AA"), Seq: 0, DetectedAt: 1},
		{Data: []byte("BB"), Seq: 1, IsFinal: true, DetectedAt: 1},
	}
	return r.mockProvider.Stream(ctx, text, voice)
}

func newTTSTestEngine(t *testing.T, provider Provider) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterInternalRoutes(engine.Group("/internal/v1"), NewHandler(provider), testInternalToken)
	return engine
}

func TestPostSynthesize_PassesVoiceID(t *testing.T) {
	recProv := &recordingProvider{}
	engine := newTTSTestEngine(t, recProv)
	body, _ := json.Marshal(SynthesizeRequest{Text: "hello", VoiceID: VoiceIDAIMaleTech})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/tts/synthesize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", testInternalToken)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp SynthesizeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VoiceID != VoiceIDAIMaleTech || resp.Chunks != 2 || resp.AudioBase64 == "" {
		t.Fatalf("resp = %+v", resp)
	}
	if recProv.lastVoice.VoiceID != VoiceIDAIMaleTech || recProv.lastVoice.Speed != 0.9 {
		t.Fatalf("provider voice = %+v", recProv.lastVoice)
	}
	if recProv.lastText != "hello" {
		t.Fatalf("text = %q", recProv.lastText)
	}
}

func TestPostSynthesize_RejectsMissingToken(t *testing.T) {
	engine := newTTSTestEngine(t, &recordingProvider{})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/tts/synthesize", bytes.NewReader([]byte(`{"text":"hi"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

// The package ships dormant (see the package doc): app-server hands over a nil
// provider whenever VOLC_SPEECH_API_KEY is empty, which is the state every
// environment is in today. The first caller to arrive before the credential does
// must get a clean UNAVAILABLE rather than a panic or a 500 that reads as a bug
// in the caller.
//
// This is the caller-visible contract only. Two mechanisms produce it — the
// handler's early guard and Collect's own nil check, which maps to the same 503 /
// UNAVAILABLE — so on its own this test cannot tell them apart.
// TestPostSynthesize_UnconfiguredOutranksMalformedBody is the one that does.
func TestPostSynthesize_UnconfiguredProviderIsUnavailable(t *testing.T) {
	engine := newTTSTestEngine(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/tts/synthesize", bytes.NewReader([]byte(`{"text":"hi"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", testInternalToken)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s, want 503", rec.Code, rec.Body.String())
	}
	var body apierr.Body
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (body = %s)", err, rec.Body.String())
	}
	if body.Code != "UNAVAILABLE" {
		t.Fatalf("code = %q, want UNAVAILABLE — a dormant provider must not look like a caller error", body.Code)
	}
}

// The handler's nil-provider guard is redundant with Collect's — both end in the
// same 503 — so the only thing that proves it is still there is the ordering it
// imposes: with no provider, a request that is *also* malformed must be answered
// as unconfigured, not as a bad request.
//
// Remove the guard and this goes red: ShouldBindJSON runs first and answers 400,
// sending whoever is debugging "TTS doesn't work" to check their own JSON instead
// of the missing credential.
func TestPostSynthesize_UnconfiguredOutranksMalformedBody(t *testing.T) {
	engine := newTTSTestEngine(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/tts/synthesize", bytes.NewReader([]byte(`{"text":`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", testInternalToken)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"status = %d body = %s, want 503 — an unconfigured provider must be reported before the body is judged",
			rec.Code, rec.Body.String(),
		)
	}
}

func TestPostSynthesize_RejectsEmptyText(t *testing.T) {
	engine := newTTSTestEngine(t, &recordingProvider{})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/tts/synthesize", bytes.NewReader([]byte(`{"text":"  "}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", testInternalToken)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}
