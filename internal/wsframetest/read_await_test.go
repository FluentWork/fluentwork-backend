package wsframetest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestAwaitTypeSkipsFramesUntilTheWantedOneArrives(t *testing.T) {
	_, conn := serveFrames(t, `{"type":"ai.text.delta"}`, `{"type":"ai.turn.end"}`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := AwaitType(ctx, conn, "ai.turn.end")
	if err != nil {
		t.Fatalf("AwaitType: %v", err)
	}
	if got["type"] != "ai.turn.end" {
		t.Fatalf("got %v, want the ai.turn.end frame", got)
	}
}

// The whole reason this helper exists. A wait that reads until it sees the type
// it wants throws away everything else it read — so if the frame never comes,
// the failure has to hand back what did, or the only trace of the bug is
// "context deadline exceeded".
func TestAwaitTypeReportsWhatArrivedInsteadOfTheWantedFrame(t *testing.T) {
	_, conn := serveFrames(t,
		`{"type":"session.ready"}`,
		`{"type":"ai.text.delta"}`,
		`{"type":"ai.text.delta"}`,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	_, err := AwaitType(ctx, conn, "ai.turn.end")
	if err == nil {
		t.Fatal("want an error when the frame never arrives")
	}

	msg := err.Error()
	for _, want := range []string{"ai.turn.end", "session.ready", "ai.text.delta×2", "3 frame(s)"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not mention %q", msg, want)
		}
	}
}

func TestAwaitTypeSaysWhenNothingArrivedAtAll(t *testing.T) {
	_, conn := serveFrames(t)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	_, err := AwaitType(ctx, conn, "ai.turn.end")
	if err == nil {
		t.Fatal("want an error when the frame never arrives")
	}
	if msg := err.Error(); !strings.Contains(msg, "none at all") {
		t.Fatalf("error %q should say the socket was silent", msg)
	}
}

// serveFrames accepts one WebSocket connection, writes raw, and then keeps
// reading so the connection stays open.
//
// It reads rather than blocking on a channel on purpose: a peer that never
// reads cannot answer a close handshake, and the client side would then spend
// its whole close timeout waiting — which is how this helper made its own first
// test take five seconds. Reading also keeps "the socket is silent" and "the
// socket is closed" distinguishable, which is the difference the tests below
// turn on.
func serveFrames(t *testing.T, raw ...string) (*httptest.Server, *websocket.Conn) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for _, frame := range raw {
			if err := conn.Write(context.Background(), websocket.MessageText, []byte(frame)); err != nil {
				return
			}
		}
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return srv, conn
}
