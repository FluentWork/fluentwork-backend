package voicegateway_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// sourceFile is one of this package's own production files, parsed.
type sourceFile struct {
	name string
	fset *token.FileSet
	file *ast.File
}

// productionSources parses the package's non-test files.
//
// `go test` runs the test binary with its working directory set to the
// package's source directory, so the sources are "./*.go". No repository-root
// discovery, and no chance of picking up another package's copies of the same
// names.
func productionSources(t *testing.T) []sourceFile {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	fset := token.NewFileSet()
	var out []sourceFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		out = append(out, sourceFile{name: name, fset: fset, file: file})
	}
	// Anti-vacuity floor: this package has dozens of production files. Finding
	// a handful means the directory walk broke, not that the package shrank —
	// and a broken walk reports a clean tree.
	if len(out) < 10 {
		t.Fatalf("parsed only %d production files; the source scan is broken, so anything it reports means nothing", len(out))
	}
	return out
}

// isDecodeType reports whether fun is the voiceproto.DecodeType call.
func isDecodeType(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "DecodeType" {
		return false
	}
	qualifier, ok := sel.X.(*ast.Ident)
	return ok && qualifier.Name == "voiceproto"
}

// One control frame used to be parsed up to nine times: once by the dispatcher
// to find out what the frame was, and once more by each handler it was offered
// to — because "is this frame mine?" could only be answered by re-reading the
// bytes.
//
// The dispatcher decides ownership from the frame type, so by the time a
// handler runs the answer is already known and the bytes have already been
// read. A handler that parses the type a second time is a second opinion about
// a settled fact — and where the two opinions can disagree, the second one
// quietly wins: a decode failure inside a handler returned controlNotMine, so
// the frame fell through to the unknown-frame path even though the dispatcher
// had already accepted it.
//
// Only this package is scanned. A frame type decoded in cmd/ or in a test is
// outside the dispatch and outside this criterion.
func TestControlDispatch_DecodesTheFrameTypeOnce(t *testing.T) {
	t.Parallel()

	// caller name → "file:line" evidence.
	callers := map[string][]string{}
	for _, src := range productionSources(t) {
		recorded := map[token.Pos]bool{}

		for _, decl := range src.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isDecodeType(call.Fun) {
					return true
				}
				callers[fn.Name.Name] = append(callers[fn.Name.Name],
					fmt.Sprintf("%s:%d", src.name, src.fset.Position(call.Pos()).Line))
				recorded[call.Pos()] = true
				return true
			})
		}

		// A decode outside any function body — a package-level variable
		// initialiser — has no enclosing name to report, so the loop above
		// cannot see it. That exact blind spot once shipped inside another
		// guard in this repo (internal/config's environment-declaration scan
		// only walked function bodies), so it is closed here rather than
		// rediscovered.
		ast.Inspect(src.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isDecodeType(call.Fun) || recorded[call.Pos()] {
				return true
			}
			t.Errorf("%s:%d decodes a control frame outside any function body; the scan cannot attribute it to a handler",
				src.name, src.fset.Position(call.Pos()).Line)
			return true
		})

		// The scan matches on the qualifier `voiceproto`. An aliased import
		// would make every call in this file invisible while leaving the file
		// looking scanned — the same failure mode as a broken walk, one file
		// at a time.
		if alias, aliased := aliasedVoiceprotoImport(src); aliased {
			t.Errorf("%s imports voiceproto as %q; this scan matches the plain qualifier, so every DecodeType call in the file would go unseen",
				src.name, alias)
		}
	}

	// Positive control. If the matcher stops working — a renamed package, a
	// different call shape — the offender list below comes back empty and an
	// unchecked tree looks exactly like a clean one. This is what tells the
	// two apart.
	if len(callers["HandleControl"]) == 0 {
		t.Fatal("the scan did not find the dispatcher decoding a frame type; the scan is broken, not the tree")
	}

	// handshake decodes the auth frame before a dispatch exists, and
	// HandleControl is the dispatch. Every other decode is a handler asking
	// again.
	allowed := map[string]string{
		"handshake":     "the auth frame is read before a session, and therefore before a dispatch, exists",
		"HandleControl": "the dispatcher decides ownership from the type, so it is the one that reads it",
	}
	var offenders []string
	for name, sites := range callers {
		if _, ok := allowed[name]; ok {
			continue
		}
		offenders = append(offenders, fmt.Sprintf("%s (%s)", name, strings.Join(sites, ", ")))
	}
	sort.Strings(offenders)

	if len(offenders) > 0 {
		t.Errorf("control frame types are decoded outside the dispatcher:\n"+
			"  %s\n"+
			"handlers are handed the frame the dispatcher already decoded; a handler that parses the type again is a second opinion about a settled fact",
			strings.Join(offenders, "\n  "))
	}
}

// aliasedVoiceprotoImport reports the name this file imports voiceproto under,
// when it is not the package's own name.
func aliasedVoiceprotoImport(src sourceFile) (string, bool) {
	for _, spec := range src.file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		if !strings.HasSuffix(path, "/internal/voiceproto") {
			continue
		}
		if spec.Name != nil && spec.Name.Name != "voiceproto" {
			return spec.Name.Name, true
		}
	}
	return "", false
}

// The dispatch table is the answer to "which frames does this gateway accept?".
// Before it existed, that question was answerable only by reading all eight
// handlers top to bottom.
//
// The list is written out rather than recomputed from the table. Recomputing
// would make the criterion agree with whatever the table happens to say —
// including after somebody deletes a row.
func TestControlRoutes_CoverTheClientSurfaceExactly(t *testing.T) {
	t.Parallel()

	// The C→S frames of WSS V2. `client.asr.transcription` is deliberately
	// absent: it travels the other way (gateway → client, see voiceproto). So is
	// `ai.tts.client.ack`, which the gateway does not accept at all.
	clientSurface := []string{
		voiceproto.TypeAuth,
		voiceproto.TypePing,
		voiceproto.TypeSessionStart,
		voiceproto.TypeUserSpeechStart,
		voiceproto.TypeUserSpeechEnd,
		voiceproto.TypeClientTurnAbort,
		voiceproto.TypeClientRescueRequest,
		voiceproto.TypeInterrupt,
		voiceproto.TypeSessionEnd,
	}

	routes := voicegateway.ControlRoutes()
	if len(routes) == 0 {
		t.Fatal("the dispatch table is empty; every control frame would be counted as unknown and ignored")
	}

	for _, frameType := range clientSurface {
		if _, claimed := routes[frameType]; !claimed {
			t.Errorf("%s is a frame a client may send, but no handler owns it; the gateway would count it as unknown and ignore it", frameType)
		}
	}

	surface := make(map[string]bool, len(clientSurface))
	for _, frameType := range clientSurface {
		surface[frameType] = true
	}
	for frameType, handler := range routes {
		if !surface[frameType] {
			t.Errorf("the dispatch claims %s (→ %s), which is not a frame a client sends; either a frame is missing from the list above, or the row is a type name that no longer exists", frameType, handler)
		}
	}
}

// The compiler checks one direction of the table: a row pointing at a method
// that does not exist does not build. It cannot check the other — a controlXxx
// method sitting in the package with no frame type routed to it.
//
// That is the shape this dispatch exists to remove. Adding a frame class used to
// mean editing eight handlers; now it means adding one row, so the way to get it
// wrong moved from "I have to touch eight files" (loud) to "I wrote the handler
// and forgot the row" (silent). This is the half that has to be said out loud.
func TestControlDispatch_EveryControlHandlerIsRouted(t *testing.T) {
	t.Parallel()

	routed := map[string]string{} // handler name → a frame type that reaches it
	for frameType, handler := range voicegateway.ControlRoutes() {
		routed[handler] = frameType
	}

	defined := map[string]string{} // handler name → the file defining it
	for _, src := range productionSources(t) {
		for _, decl := range src.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !isMethodOnHandler(fn) || !strings.HasPrefix(fn.Name.Name, "control") {
				continue
			}
			defined[fn.Name.Name] = src.name
		}
	}
	// Anti-vacuity floor. This package has several control handlers; finding
	// almost none means the receiver or the name prefix stopped matching, and a
	// matcher that matches nothing reports a perfectly routed table.
	if len(defined) < 3 {
		t.Fatalf("found only %d control* methods on *Handler; the source scan is broken, so this criterion checked nothing", len(defined))
	}

	for name, file := range defined {
		if _, ok := routed[name]; !ok {
			t.Errorf("%s (%s) is a control handler that no frame type routes to; add its row to controlDispatch, or rename it if it is not a handler", name, file)
		}
	}
	for name := range routed {
		if _, ok := defined[name]; !ok {
			t.Errorf("controlDispatch routes to %s, which is not a control* method on *Handler in this package", name)
		}
	}
}

// isMethodOnHandler reports whether fn is `func (h *Handler) …`.
func isMethodOnHandler(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "Handler"
}
