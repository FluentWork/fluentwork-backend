package voicegateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/content/tts"
)

// stubTTSProvider returns fixed PCM instead of calling a vendor.
type stubTTSProvider struct {
	pcm      []byte
	gotVoice tts.VoiceConfig
	gotText  string
}

func (s *stubTTSProvider) Stream(_ context.Context, text string, voice tts.VoiceConfig) (<-chan tts.AudioChunk, error) {
	s.gotText, s.gotVoice = text, voice
	ch := make(chan tts.AudioChunk, 1)
	ch <- tts.AudioChunk{Data: s.pcm, Seq: 0, IsFinal: true}
	close(ch)
	return ch, nil
}

func (s *stubTTSProvider) Ping(context.Context) error { return nil }
func (s *stubTTSProvider) Close() error               { return nil }

// synthesisServer runs app-server's real TTS route, so these tests exercise the
// wire contract itself rather than a second opinion about it.
func synthesisServer(t *testing.T, provider tts.Provider, token string) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	tts.RegisterInternalRoutes(engine.Group("/internal/v1"), tts.NewHandler(provider), token)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return server
}

// The rung comes back as bytes the client can already play, with the speaker the
// catalog resolved — the gateway passes "rescue_ladder" and gets a real voice id
// back, which is what goes in the client's ai.tts.start frame.
func TestHTTPRescueSynthesizer_ReturnsPlayablePCM(t *testing.T) {
	pcm := make([]byte, 6400) // 200 ms of 16 kHz mono s16le
	provider := &stubTTSProvider{pcm: pcm}
	server := synthesisServer(t, provider, "tok")

	client := NewHTTPRescueSynthesizer(server.URL, "tok", "rescue_ladder", nil)
	audio, err := client.Synthesize(context.Background(), "先说结论，再说原因", "t1")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(audio.PCM) != len(pcm) {
		t.Fatalf("pcm = %d bytes, want %d", len(audio.PCM), len(pcm))
	}
	if audio.SampleRate != RescueAudioSampleRate || audio.Codec != "pcm" {
		t.Fatalf("audio = %+v, want 16 kHz pcm", audio)
	}
	if audio.VoiceID == "" || audio.VoiceID == "rescue_ladder" {
		t.Fatalf("voice = %q, want the resolved speaker the catalog maps rescue_ladder to", audio.VoiceID)
	}
	if provider.gotText != "先说结论，再说原因" {
		t.Fatalf("provider text = %q", provider.gotText)
	}
	// The pace is the reason the ladder has its own voice entry: slower than the
	// conversation, and speed is a synthesis parameter.
	if provider.gotVoice.Speed >= 1.0 {
		t.Fatalf("speed = %v, want the ladder slower than the conversation", provider.gotVoice.Speed)
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

			client := NewHTTPRescueSynthesizer(server.URL, "tok", "rescue_ladder", nil)
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
	if _, err := NewHTTPRescueSynthesizer("", "tok", "", nil).Synthesize(context.Background(), "x", "t"); err == nil {
		t.Fatal("an unconfigured synthesizer must error")
	}
}

// The bad-token path is the real middleware, not a fixture that happens to 401.
func TestHTTPRescueSynthesizer_RejectsBadToken(t *testing.T) {
	server := synthesisServer(t, &stubTTSProvider{pcm: []byte{1, 2}}, "tok")
	client := NewHTTPRescueSynthesizer(server.URL, "wrong", "rescue_ladder", nil)
	if _, err := client.Synthesize(context.Background(), "x", "t"); err == nil {
		t.Fatal("expected an error for a bad internal token")
	}
}

// The synth call carries its own budget: it is the half of a rung that may be
// abandoned, and abandoning it must not hold the next rung's schedule.
func TestHTTPRescueSynthesizer_OwnBudgetIsInsideTheRungSpacing(t *testing.T) {
	if DefaultRescueSynthTimeout >= DefaultRescueLevel1After {
		t.Fatalf("synth timeout %s must stay under the %s rung spacing", DefaultRescueSynthTimeout, DefaultRescueLevel1After)
	}
}
