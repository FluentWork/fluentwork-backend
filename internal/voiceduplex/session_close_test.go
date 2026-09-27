package voiceduplex

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// startQuietDuplexServer stands up a duplex that completes the handshake and
// then says nothing, so a session opened against it stays idle and silent.
//
// It keeps reading after the handshake on purpose: that is what lets Close
// finish its close handshake at once instead of waiting out the library's 5s
// read (coder/websocket, close.go:198).
func startQuietDuplexServer(t *testing.T) string {
	t.Helper()
	return startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-quiet"}}`)
		for {
			readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, _, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}
		}
	})
}

// startClosedSession opens a session against the quiet duplex and closes it.
func startClosedSession(t *testing.T) *DuplexSession {
	t.Helper()
	session := openTestSession(t, startQuietDuplexServer(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return session
}

// After Close, every frame-shaped method must answer with an error.
//
// Close sets the socket field to nil so later frames fail fast — but only four
// of the eight methods that put a frame on the wire checked it, and the check
// itself (`if s.conn == nil`) is a **read of a field another goroutine writes**,
// so it never made the concurrent case safe either.
//
// The four that skipped the check dereference the nil socket, and the honest
// size of that: coder/websocket's Conn.Write has no nil-receiver guard anywhere
// down the chain (read from the module cache, not inferred), so the panic would
// land in whichever goroutine sent the frame — including collectTurn's silence
// pump, which has no recover, which would take the whole voice-gateway process
// with it.
//
// What this does **not** claim is an incident. Writers in this repo reach the
// socket before the teardown gets there and lose to a transport error rather
// than to a nil, so no production caller that sends *after* Close returns could
// be named. This pins the contract the type already meant to have — "a closed
// session answers" — not a crash someone had.
//
// Asserting the error is ErrDuplexClosed rather than merely non-nil keeps the
// caller contract that already exists: internal/voicegateway/provider_volc_duplex.go:652
// decides "replace the duplex" on exactly this sentinel.
func TestAClosedSessionAnswersEveryFrameWithAnError(t *testing.T) {
	t.Parallel()

	session := startClosedSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The list is the whole wire-touching surface of *DuplexSession. The turn
	// entry points (WaitTurn and friends) are covered by the next test, which
	// shows they inherit this through recv.
	frames := []struct {
		name string
		call func(context.Context) error
	}{
		{"CommitAudio", session.CommitAudio},
		{"CommitInputMute", session.CommitInputMute},
		{"CommitInputUnmute", session.CommitInputUnmute},
		{"AppendPCMChunk", func(ctx context.Context) error {
			return session.AppendPCMChunk(ctx, []byte{1, 2, 3, 4})
		}},
		{"SendPCM", func(ctx context.Context) error {
			return session.SendPCM(ctx, []byte{1, 2, 3, 4})
		}},
		{"RequestTextTTS", func(ctx context.Context) error {
			return session.RequestTextTTS(ctx, "hello")
		}},
		{"UpdateInstructions", func(ctx context.Context) error {
			_, err := session.UpdateInstructions(ctx, "instructions")
			return err
		}},
		{"Recv", func(ctx context.Context) error {
			_, err := session.Recv(ctx)
			return err
		}},
	}

	for _, frame := range frames {
		t.Run(frame.name, func(t *testing.T) {
			mustAnswerWithAnError(t, frame.name, func() error { return frame.call(ctx) })
		})
	}
}

// The turn entry point reaches the socket through recv, so it must refuse too.
//
// This is the path the teardown actually overlaps with, and the one the race
// detector reported: rt.close closes the upstream **on purpose** so the
// in-flight turn unblocks (internal/voicegateway/handler.go:1000), which means
// Close and collectTurn are live at the same time.
//
// The overlap itself is **not** pinned anywhere, and that was measured rather
// than assumed: a test that runs the two against each other lands on an error
// from the closed transport before it can read the detached field, so it
// reproduces nothing on the unfixed code — with or without -race. Pinning the
// race would need it to happen on demand, and it happens on scheduling. What
// is pinned is the half that can be: the state it must never produce (a panic),
// and the access rule that makes both impossible.
func TestATurnOnAClosedSessionEndsInAnErrorNotAPanic(t *testing.T) {
	t.Parallel()

	session := startClosedSession(t)
	mustAnswerWithAnError(t, "WaitTurn", func() error {
		_, err := session.WaitTurn(context.Background(), time.Now(), nil, 50*time.Millisecond)
		return err
	})
}

// Close announces itself on the wire.
//
// A characterisation guard, not a reproduction: the frame is sent today. It
// exists because the guarded fix had to write it **around** send — by then
// Close has already detached the socket, so routing it back through send would
// get errSessionClosed and drop the frame, silently, with nothing red. Nothing
// else in the repo asserts this frame.
func TestCloseSendsSessionCloseOnTheWire(t *testing.T) {
	t.Parallel()

	frames := make(chan string, 8)
	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-close-frame"}}`)
		for {
			readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, data, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}
			var frame struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &frame) == nil {
				select {
				case frames <- frame.Type:
				default:
				}
			}
		}
	})

	session := openTestSession(t, url)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case kind := <-frames:
			if kind == "session.close" {
				return
			}
		case <-deadline:
			t.Fatal("Close did not put session.close on the wire")
		}
	}
}

// mustAnswerWithAnError runs call and reports a panic as a failure instead of
// letting it take the test binary down.
//
// The recover is the point: the defect is a process-wide crash, so a test that
// dies with it can only report "the package panicked", not which of the eight
// methods did it or why.
func mustAnswerWithAnError(t *testing.T, name string, call func() error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s on a closed session panicked: %v\n"+
				"a closed session must answer with an error; dereferencing the nil socket "+
				"panics in whichever goroutine sent the frame, and the turn's silence pump has no recover",
				name, r)
		}
	}()
	err := call()
	if err == nil {
		t.Errorf("%s on a closed session returned nil, want an error naming the closed session", name)
		return
	}
	if !errors.Is(err, ErrDuplexClosed) {
		t.Errorf("%s on a closed session returned %v, want an error wrapping ErrDuplexClosed — "+
			"callers decide \"replace the duplex\" on that sentinel", name, err)
	}
}

// Reads of the socket field only happen under its mutex.
//
// `conn` is written twice — once at construction, once by Close — and read by
// every frame-shaped method plus the silence pump collectTurn starts on its own
// goroutine. A read that is not under the mutex is the race the detector
// reported; a read that skips the nil check is the panic. Both are visible in
// the source, without a race detector run — which matters, because the landing
// gate still runs plain `go test`.
//
// Reads and writes are held to different rules on purpose. A write at
// construction is safe because nothing else holds the session yet; a read
// anywhere but currentConn is not safe at any point, because by then Close may
// have run.
func TestTheSocketFieldIsOnlyReadByTheLockedAccessors(t *testing.T) {
	t.Parallel()

	// Where the field may be *read*, and why each is allowed to. Both hold
	// connMu across the read; the list is the size it is because two is enough,
	// not because a third would be harmless.
	readsAllowedIn := map[string]string{
		"currentConn": "the accessor: reads under connMu, and refuses when the socket is gone",
		"Close":       "captures the socket under the same connMu before detaching it",
	}
	// Where the field may be *written*, and why each is allowed to.
	writesAllowedIn := map[string]string{
		// The one place the session is published, from a freshly dialled
		// socket that no other goroutine can see yet.
		"OpenDuplex": "construction, before the session is shared",
		// Detaches the socket so every later frame fails fast instead of
		// writing to a connection being torn down.
		"Close": "the deliberate detach",
	}

	type use struct {
		fn   string
		file string
		line int
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	var reads, writes []use
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			site := func(pos token.Pos) use {
				return use{fn.Name.Name, name, fset.Position(pos).Line}
			}
			// Writes first: an assignment target or a composite-literal key.
			// Their positions are remembered so the read pass can skip the
			// same node.
			written := map[token.Pos]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case nil:
					// Inspect reports the end of a node's children as a nil
					// visit; there is nothing to classify there.
					return false
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "conn" {
							writes = append(writes, site(sel.Pos()))
							written[sel.Pos()] = true
						}
					}
				case *ast.KeyValueExpr:
					// DuplexSession{conn: …}
					if key, ok := node.Key.(*ast.Ident); ok && key.Name == "conn" {
						writes = append(writes, site(node.Pos()))
						written[node.Pos()] = true
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if n == nil {
					return false
				}
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "conn" || written[sel.Pos()] {
					return true
				}
				reads = append(reads, site(sel.Pos()))
				return true
			})
		}
	}

	// Anti-vacuity, per rule: a scan that stopped seeing the field must say so
	// rather than report a clean tree. A missing write is not a clean tree
	// either — it means the field moved and these lists are stale.
	countIn := func(uses []use, fn string) int {
		n := 0
		for _, u := range uses {
			if u.fn == fn {
				n++
			}
		}
		return n
	}
	for fn, why := range readsAllowedIn {
		if countIn(reads, fn) == 0 {
			t.Fatalf("the scan found no read of the socket field in %s (%s); if the field moved, update this list rather than deleting the rule", fn, why)
		}
	}
	for fn, why := range writesAllowedIn {
		if countIn(writes, fn) == 0 {
			t.Fatalf("the scan found no write of the socket field in %s (%s); if the field moved, update this list rather than deleting the rule", fn, why)
		}
	}
	for _, u := range reads {
		if _, ok := readsAllowedIn[u.fn]; ok {
			continue
		}
		t.Errorf("%s:%d reads the socket field from %s, which does not hold connMu.\n"+
			"Close nils that field from the teardown path while the turn's silence pump is still sending, "+
			"so every read must go through currentConn.",
			u.file, u.line, u.fn)
	}
	for _, u := range writes {
		if _, ok := writesAllowedIn[u.fn]; ok {
			continue
		}
		t.Errorf("%s:%d writes the socket field from %s, which is neither construction nor Close.\n"+
			"Only a session that no other goroutine can see yet, or a deliberate detach, may assign it.",
			u.file, u.line, u.fn)
	}
}
