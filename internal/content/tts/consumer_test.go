package tts

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

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
)

// TestSynthesizeEndpointHasAConsumer answers one question with evidence: does
// anything actually reach this endpoint?
//
// It exists because the answer was wrong in the package's own doc comment. "No
// caller" reads as a fact about missing wiring, and it sent a reader hunting for
// a connection that was already made — so this test fails the moment the last
// caller goes away, and the doc comment beside it fails in the same commit.
//
// It drives the gateway's real client over a real socket at the real route
// rather than re-implementing the call: a second copy of the request shape could
// disagree with the client and still pass.
func TestSynthesizeEndpointHasAConsumer(t *testing.T) {
	var (
		mu     sync.Mutex
		paths  []string
		tokens []string
	)
	provider := &recordingProvider{mockProvider: mockProvider{chunks: []AudioChunk{{Data: make([]byte, 6400), IsFinal: true}}}}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterInternalRoutes(engine.Group("/internal/v1"), NewHandler(provider), "tok")
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
	if !strings.Contains(provider.lastText, "先说结论") {
		t.Fatalf("provider saw %q, not the rung text", provider.lastText)
	}
}
