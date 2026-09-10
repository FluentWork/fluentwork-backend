package voicegateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
)

// sixtySecondsOfPCM16 is the client's own per-turn cap expressed in bytes:
// 60s of 16 kHz mono PCM16. Nothing in the protocol says audio arrives in small
// chunks — the client's 20ms framing is a client choice, not a rule the gateway
// may assume.
const sixtySecondsOfPCM16 = 60 * 16_000 * 2

// incompressibleBytes returns data the websocket layer cannot shrink, so the
// read limit is exercised on the real message size rather than a compressed one.
func incompressibleBytes(n int) []byte {
	out := make([]byte, n)
	seed := uint32(0x9E3779B9)
	for i := range out {
		seed = seed*1664525 + 1013904223
		out[i] = byte(seed >> 24)
	}
	return out
}

// A single 1.8 MiB binary frame must not kill the session. coder/websocket reads
// at most 32 KiB per frame by default, so the gateway's read failed and the
// session went down — the same class of bug as the duplex side (docs/47), on the
// side that faces the client.
func TestHandler_AcceptsASingleLargeBinaryAudioFrame(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	provider := &stubProvider{}
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil,
		voicegateway.Options{InsecureSkipOrigin: true})
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn := dialVoice(ctx, t, srv)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	authAndStart(ctx, t, conn)

	blob := incompressibleBytes(sixtySecondsOfPCM16)
	if err := conn.Write(ctx, websocket.MessageBinary, blob); err != nil {
		t.Fatalf("write large binary frame: %v", err)
	}

	// The pong also orders the server's handling of the blob before the reads below.
	assertSessionAlive(ctx, t, conn, "after a single 1.8 MiB binary frame")

	if got := len(provider.session.audioPayload); got != 1 {
		t.Fatalf("provider saw %d audio frames, want 1", got)
	}
	if got := len(provider.session.audioPayload[0]); got != sixtySecondsOfPCM16 {
		t.Fatalf("provider payload = %d bytes, want %d", got, sixtySecondsOfPCM16)
	}
}
