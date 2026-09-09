package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
