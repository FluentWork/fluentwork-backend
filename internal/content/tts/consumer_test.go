// Package tts_test (not tts) holds the wire contract between this package's
// synthesis route and the voice gateway's client that calls it.
//
// It lives outside the package on purpose, and that is load-bearing rather than
// stylistic: the gateway must not import this package (.golangci.yml depguard
// rule — the gateway talks to app-server over HTTP, it does not link app-server's
// TTS into the voice process). An in-package test that imported voicegateway
// would be legal Go while making that very boundary invisible, and the import
// graph under test would no longer be the one production gets.
package tts_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/content/tts"
	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
)

// stubProvider answers instantly with fixed PCM, recording what it was asked to
// synthesize. It stands in for the vendor only — everything above it is real.
type stubProvider struct {
	mu       sync.Mutex
	pcm      []byte
	text     string
	voice    tts.VoiceConfig
	sawVoice bool
}

func (p *stubProvider) Stream(_ context.Context, text string, voice tts.VoiceConfig) (<-chan tts.AudioChunk, error) {
	p.mu.Lock()
	p.text, p.voice, p.sawVoice = text, voice, true
	p.mu.Unlock()
	ch := make(chan tts.AudioChunk, 1)
	ch <- tts.AudioChunk{Data: p.pcm, Seq: 0, IsFinal: true}
	close(ch)
	return ch, nil
}

func (p *stubProvider) Ping(context.Context) error { return nil }
func (p *stubProvider) Close() error               { return nil }

func (p *stubProvider) snapshot() (string, tts.VoiceConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.text, p.voice
}

// synthesisServer runs the real TTS route. Both tests below go over a real
// socket to it, so the wire format, the internal-token middleware and the
// request shape are the production ones.
func synthesisServer(t *testing.T, provider tts.Provider, token string) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	tts.RegisterInternalRoutes(engine.Group("/internal/v1"), tts.NewHandler(provider), token)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return server
}

// TestSynthesizeEndpointHasAConsumer answers one question with evidence: does
// anything actually reach this endpoint?
//
// It exists because the answer was wrong in the package's own doc comment. "No
// caller" reads as a fact about missing wiring, and it sent a reader hunting for
// a connection that was already made — so this fails the moment the last caller
// goes away, and the doc comment beside it fails in the same commit.
func TestSynthesizeEndpointHasAConsumer(t *testing.T) {
	var (
		mu     sync.Mutex
		paths  []string
		tokens []string
	)
	provider := &stubProvider{pcm: make([]byte, 6400)}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	tts.RegisterInternalRoutes(engine.Group("/internal/v1"), tts.NewHandler(provider), "tok")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		tokens = append(tokens, r.Header.Get("X-Internal-Token"))
		mu.Unlock()
		engine.ServeHTTP(w, r)
	}))
	defer server.Close()

	client := voicegateway.NewHTTPRescueSynthesizer(server.URL, "tok", "rescue_ladder", slog.Default())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	audio, err := client.Synthesize(ctx, "先说结论，再说原因", "t_1")
	if err != nil {
		t.Fatalf("the gateway's synthesizer could not reach this endpoint: %v", err)
	}
	if len(audio.PCM) == 0 {
		t.Fatal("the endpoint answered but the gateway got no audio")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/internal/v1/tts/synthesize" {
		t.Fatalf("paths = %v, want exactly /internal/v1/tts/synthesize", paths)
	}
	if tokens[0] != "tok" {
		t.Fatalf("token = %q, want the internal token", tokens[0])
	}
	if text, _ := provider.snapshot(); !strings.Contains(text, "先说结论") {
		t.Fatalf("provider saw %q, not the rung text", text)
	}
}

// The rung comes back as bytes the client can already play, with the speaker the
// catalog resolved — the gateway asks for "rescue_ladder" and gets a real voice
// id back, which is what goes into the client's ai.tts.start frame.
func TestHTTPRescueSynthesizer_ReturnsPlayablePCM(t *testing.T) {
	pcm := make([]byte, 6400) // 200 ms of 16 kHz mono s16le
	provider := &stubProvider{pcm: pcm}
	server := synthesisServer(t, provider, "tok")

	client := voicegateway.NewHTTPRescueSynthesizer(server.URL, "tok", "rescue_ladder", nil)
	audio, err := client.Synthesize(context.Background(), "先说结论，再说原因", "t1")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(audio.PCM) != len(pcm) {
		t.Fatalf("pcm = %d bytes, want %d", len(audio.PCM), len(pcm))
	}
	if audio.SampleRate != voicegateway.RescueAudioSampleRate || audio.Codec != "pcm" {
		t.Fatalf("audio = %+v, want 16 kHz pcm", audio)
	}
	if audio.VoiceID == "" || audio.VoiceID == "rescue_ladder" {
		t.Fatalf("voice = %q, want the resolved speaker the catalog maps rescue_ladder to", audio.VoiceID)
	}
	text, voice := provider.snapshot()
	if text != "先说结论，再说原因" {
		t.Fatalf("provider text = %q", text)
	}
	// The pace is the reason the ladder has its own voice entry: slower than the
	// conversation, and speed is a synthesis parameter.
	if voice.Speed >= 1.0 {
		t.Fatalf("speed = %v, want the ladder slower than the conversation", voice.Speed)
	}
}

func TestHTTPRescueSynthesizer_FailuresReturnErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"provider down", http.StatusServiceUnavailable, `{"code":"UNAVAILABLE"}`, "http=503"},
		{"malformed", http.StatusOK, `not json`, "decode"},
		{"empty audio", http.StatusOK, `{"voice_id":"v","chunks":0,"audio_base64":""}`, "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client := voicegateway.NewHTTPRescueSynthesizer(server.URL, "tok", "rescue_ladder", nil)
			_, err := client.Synthesize(context.Background(), "再说一遍", "t1")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}

	// An unconfigured synthesizer fails fast rather than calling anything.
	if _, err := voicegateway.NewHTTPRescueSynthesizer("", "tok", "", nil).Synthesize(context.Background(), "x", "t"); err == nil {
		t.Fatal("an unconfigured synthesizer must error")
	}
}

// The bad-token path is the real middleware, not a fixture that happens to 401.
func TestHTTPRescueSynthesizer_RejectsBadToken(t *testing.T) {
	server := synthesisServer(t, &stubProvider{pcm: []byte{1, 2}}, "tok")
	client := voicegateway.NewHTTPRescueSynthesizer(server.URL, "wrong", "rescue_ladder", nil)
	if _, err := client.Synthesize(context.Background(), "x", "t"); err == nil {
		t.Fatal("expected an error for a bad internal token")
	}
}

// The synth call carries its own budget: it is the half of a rung that may be
// abandoned, and abandoning it must not hold the next rung's schedule.
func TestHTTPRescueSynthesizer_OwnBudgetIsInsideTheRungSpacing(t *testing.T) {
	if voicegateway.DefaultRescueSynthTimeout >= voicegateway.DefaultRescueLevel1After {
		t.Fatalf("synth timeout %s must stay under the %s rung spacing",
			voicegateway.DefaultRescueSynthTimeout, voicegateway.DefaultRescueLevel1After)
	}
}
