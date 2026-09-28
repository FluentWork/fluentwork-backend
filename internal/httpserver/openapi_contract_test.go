package httpserver

// This file is the fifth repo-wide guard next to internal/config's
// env_declaration_test.go, doc_attachment_test.go, readme_layout_test.go and
// config_fixture_test.go. It lives here rather than there because this package
// owns both halves of the question: server.go mounts every RegisterRoutes, and
// serveOpenAPI publishes api/openapi-v1.yaml to whoever asks for it.
//
// The question: **does the contract this server publishes name every route this
// server serves?**
//
// `api/openapi-v1.yaml` is not a description, it is the interface.
// FluentWorkAPI.swift in fluentwork-ios opens by saying so ("REST targets
// aligned to `fluentwork-backend/api/openapi-v1.yaml`"), and the app-server
// hands the same file out at GET /openapi.yaml. A route that is served but
// absent from that file is invisible to every client built from it: the client
// is not stopped by a 404 it can see, it is stopped by an endpoint it cannot
// find. Six were already missing — POST /drill/appeal (PRD E2's one-tap
// appeal), POST /topic-cards/{id}/dismiss, GET /topic-cards/stats,
// GET /corpus/recommendations, GET /corpus/feedback and
// POST /corpus/blocks/{id}/feedback — all live, all tested, none documented.
//
// What it does NOT claim:
//
//   - It compares endpoints, not payloads. A route whose request or response
//     shape has drifted from its component schema still passes.
//   - It reads the contract as text and only its `paths:` section, so it does
//     not check that the YAML is valid. Nothing does today, CI included: the CI
//     check only asserts the file exists.
//   - It reads `RegisterRoutes` only. `RegisterInternalRoutes` is deliberately
//     a different function — that surface is behind a shared token — so a
//     `/internal/v1/...` path in the contract is checked in one direction only
//     (nothing may name an internal endpoint that is not there, but the
//     scanner does not claim to have looked for it).
//   - A public route mounted any other way is invisible to it. Every package in
//     this repository registers through `rg.METHOD("literal")` inside
//     `RegisterRoutes`, and the floor below is what catches a walker that stops
//     seeing them.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/materials"
	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/internal/topic"
)

// payloads pairs each contract schema with the Go type the server actually
// serialises into it. The mapping is written out by hand rather than inferred:
// picked by similarity, `TopicCard` matches `topic.Card`, while the handler
// returns `topic.CardView` — and the two differ by exactly the field this part
// of the guard exists to check.
//
// Only flat schemas are listed. A schema built from `allOf` (`Recommendation`)
// or written inline in a path (`POST /topic-cards/{id}/dismiss`'s body) is not
// checked here; see the file header.
var payloads = []struct {
	schema string
	value  any
}{
	{schema: "PhraseBlock", value: corpus.PhraseBlockView{}},
	{schema: "BatchAcceptBlocksResponse", value: corpus.BatchAcceptResponse{}},
	{schema: "RecommendationListResponse", value: corpus.RecommendationResponse{}},
	{schema: "BlockFeedbackResponse", value: corpus.FeedbackResponse{}},
	{schema: "DrillRound", value: drill.Round{}},
	{schema: "DrillCard", value: drill.Card{}},
	{schema: "DrillJudgeResponse", value: drill.JudgeResponse{}},
	{schema: "DrillAppealRequest", value: drill.AppealRequest{}},
	{schema: "DrillAppealResponse", value: drill.AppealResponse{}},
	{schema: "TopicCard", value: topic.CardView{}},
	{schema: "TopicCardList", value: topic.ListResponse{}},
	{schema: "TopicCheckinResult", value: topic.CheckinResult{}},
	{schema: "TopicDismissResult", value: topic.DismissResult{}},
	{schema: "TopicPracticeStats", value: topic.PracticeStats{}},
	{schema: "Material", value: materials.Material{}},
	{schema: "CreateMaterialResponse", value: materials.CreateResponse{}},
	{schema: "CreateSessionResponse", value: session.CreateResponse{}},
}

// contractProperties returns, per schema, the property names it declares. Only
// a flat `properties:` block is read, which is why the table above lists flat
// schemas only.
func contractProperties(spec string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	var (
		inSchemas bool
		current   string
		inProps   bool
	)
	for _, line := range strings.Split(spec, "\n") {
		if !inSchemas {
			if line == "  schemas:" {
				inSchemas = true
			}
			continue
		}
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case indent == 4:
			current = keyName(line)
			out[current] = map[string]bool{}
			inProps = false
		case indent == 6:
			inProps = keyName(line) == "properties"
		case indent == 8 && inProps && current != "":
			out[current][keyName(line)] = true
		}
	}
	return out
}

// jsonFields returns the JSON member names a value serialises to, flattening
// embedded structs and dropping everything tagged `-`.
func jsonFields(value any) map[string]bool {
	out := map[string]bool{}
	typ := reflect.TypeOf(value)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" { // unexported
			continue
		}
		if field.Anonymous {
			embedded := reflect.New(field.Type).Elem().Interface()
			for name := range jsonFields(embedded) {
				out[name] = true
			}
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = true
	}
	return out
}

// httpMethods are the method names both sides spell in upper case.
var httpMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

// pathParameter matches gin's `:name`, which the contract writes `{name}`.
var pathParameter = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`)

// normalisePath rewrites gin path parameters into the contract's spelling.
func normalisePath(path string) string {
	return pathParameter.ReplaceAllString(path, "{$1}")
}

// keyName returns the key of a YAML mapping line, dropping any inline value:
// `get:` and `get: {}` both answer `get`.
func keyName(line string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(line), ":")
	return name
}

// contractPairs returns every `METHOD /path` the contract declares under its
// `paths:` section.
func contractPairs(spec string) []string {
	var (
		out     []string
		inPaths bool
		current string
	)
	for _, line := range strings.Split(spec, "\n") {
		if !inPaths {
			if line == "paths:" {
				inPaths = true
			}
			continue
		}
		if line != "" && !strings.HasPrefix(line, " ") {
			break // the section ends at the next top-level key
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		token := strings.TrimSpace(line)
		switch {
		case indent == 2 && strings.HasPrefix(token, "/"):
			current = keyName(line)
		case indent == 4 && current != "":
			if method := strings.ToUpper(keyName(line)); slices.Contains(httpMethods, method) {
				out = append(out, method+" "+current)
			}
		}
	}
	slices.Sort(out)
	return out
}

// routePairs returns every `METHOD /path` declared by a `RegisterRoutes`
// function under root, plus how many files declared one.
func routePairs(root string) ([]string, int, error) {
	var (
		pairs          []string
		declaringFiles int
	)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == ".git" || name == "vendor" || name == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		declared, err := routesInFile(path, src, &pairs)
		if err != nil {
			return err
		}
		if declared {
			declaringFiles++
		}
		return nil
	})
	return pairs, declaringFiles, err
}

// routesInFile appends the routes one file's `RegisterRoutes` declares, and
// reports whether the file declares such a function at all.
func routesInFile(path string, src []byte, out *[]string) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		return false, err
	}
	declared := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "RegisterRoutes" {
			continue
		}
		declared = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !slices.Contains(httpMethods, sel.Sel.Name) || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			route, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			*out = append(*out, sel.Sel.Name+" "+normalisePath(route))
			return true
		})
	}
	return declared, nil
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

func TestTheContractCoversEveryRouteThisServerServes(t *testing.T) {
	// Positive controls come in both directions: the guard has to bite on the
	// shapes it exists for and stay quiet on the shapes that only look close.
	t.Run("contract parsing", func(t *testing.T) {
		cases := []struct {
			name string
			spec string
			want []string
		}{
			{
				name: "one path carrying two methods",
				spec: "paths:\n  /sessions:\n    get:\n      summary: list\n    post:\n      summary: create\n",
				want: []string{"GET /sessions", "POST /sessions"},
			},
			{
				name: "a parameter keeps the contract's spelling",
				spec: "paths:\n  /sessions/{id}:\n    get: {}\n",
				want: []string{"GET /sessions/{id}"},
			},
			{
				name: "keys under a method are not endpoints",
				spec: "paths:\n  /x:\n    get:\n      parameters:\n        - name: size\n          in: query\n",
				want: []string{"GET /x"},
			},
			{
				name: "info, servers and components are not paths",
				spec: "openapi: 3.0.3\ninfo:\n  title: t\nservers:\n  - url: /api/v1\npaths:\n  /x:\n    get: {}\ncomponents:\n  schemas:\n    Error:\n      type: object\n",
				want: []string{"GET /x"},
			},
			{
				name: "no paths section at all",
				spec: "openapi: 3.0.3\ninfo:\n  title: t\n",
				want: nil,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := contractPairs(tc.spec); !slices.Equal(got, tc.want) {
					t.Fatalf("got %v, want %v\n%s", got, tc.want, tc.spec)
				}
			})
		}
	})

	t.Run("route scanning", func(t *testing.T) {
		cases := []struct {
			name     string
			src      string
			want     []string
			declares bool
		}{
			{
				name:     "a gin parameter becomes the contract's braces",
				src:      "package p\n\nfunc RegisterRoutes(rg R) {\n\trg.GET(\"/sessions/:id\", h)\n}\n",
				want:     []string{"GET /sessions/{id}"},
				declares: true,
			},
			{
				name: "the internal surface is a different function",
				src:  "package p\n\nfunc RegisterInternalRoutes(rg R) {\n\trg.POST(\"/tts/synthesize\", h)\n}\n",
				want: nil,
			},
			{
				name:     "a route mounted by a helper is not a literal in this body",
				src:      "package p\n\nfunc mount(rg R) {\n\trg.GET(\"/elsewhere\", h)\n}\n\nfunc RegisterRoutes(rg R) {\n\tmount(rg)\n}\n",
				want:     nil,
				declares: true,
			},
			{
				name:     "methods the contract never carries are skipped",
				src:      "package p\n\nfunc RegisterRoutes(rg R) {\n\trg.OPTIONS(\"/x\", h)\n\trg.GET(\"/y\", h)\n}\n",
				want:     []string{"GET /y"},
				declares: true,
			},
			{
				name:     "a route built from a variable is not a literal",
				src:      "package p\n\nfunc RegisterRoutes(rg R) {\n\trg.GET(prefix+\"/x\", h)\n}\n",
				want:     nil,
				declares: true,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var got []string
				declared, err := routesInFile(tc.name+".go", []byte(tc.src), &got)
				if err != nil {
					t.Fatalf("scan: %v", err)
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
				if declared != tc.declares {
					t.Fatalf("declared = %v, want %v", declared, tc.declares)
				}
			})
		}
	})

	t.Run("payload fields", func(t *testing.T) {
		t.Run("json fields", func(t *testing.T) {
			card := jsonFields(topic.CardView{})
			if !card["blocks"] {
				t.Errorf("CardView's own field is missing: %v", card)
			}
			if !card["id"] {
				t.Errorf("an embedded struct's field is not flattened: %v", card)
			}
			material := jsonFields(materials.Material{})
			if material["user_id"] || material["deleted_at"] {
				t.Errorf("a field tagged `-` is being treated as sent: %v", material)
			}
		})

		t.Run("contract properties", func(t *testing.T) {
			spec := "components:\n" +
				"  securitySchemes:\n    bearerAuth:\n      type: http\n" +
				"  schemas:\n" +
				"    Flat:\n      type: object\n      properties:\n" +
				"        one: { type: string }\n" +
				"        two:\n          type: object\n          properties:\n" +
				"            nested: { type: string }\n" +
				"    Built:\n      allOf:\n" +
				"        - $ref: \"#/components/schemas/Flat\"\n" +
				"        - type: object\n          properties:\n            three: { type: integer }\n"
			got := contractProperties(spec)
			if want := map[string]bool{"one": true, "two": true}; !reflect.DeepEqual(got["Flat"], want) {
				t.Errorf("a flat schema = %v, want %v", got["Flat"], want)
			}
			if len(got["Built"]) != 0 {
				t.Errorf("an allOf schema reports properties of its own: %v", got["Built"])
			}
			if len(got["bearerAuth"]) != 0 {
				t.Errorf("securitySchemes is being read as a schema: %v", got["bearerAuth"])
			}
		})

		t.Run("repository", func(t *testing.T) {
			root := repoRoot(t)
			spec, err := os.ReadFile(filepath.Join(root, "api", "openapi-v1.yaml"))
			if err != nil {
				t.Fatalf("read the contract: %v", err)
			}
			declared := contractProperties(string(spec))

			// Anti-silence: an empty table or an empty schema set would report
			// the same clean result as a contract that agrees with the server.
			const minPayloads = 15
			if len(payloads) < minPayloads {
				t.Fatalf("only %d payloads are mapped, expected at least %d — this check is not looking at the surface it claims to", len(payloads), minPayloads)
			}
			for _, payload := range payloads {
				props, ok := declared[payload.schema]
				if !ok {
					t.Errorf("%s: no schema of that name in the contract", payload.schema)
					continue
				}
				fields := jsonFields(payload.value)
				if len(fields) == 0 {
					t.Fatalf("%s: reflecting over %T found no JSON fields — this check proved nothing", payload.schema, payload.value)
				}
				for name := range fields {
					if !props[name] {
						t.Errorf("%s.%s: the server sends it and the contract never declares it", payload.schema, name)
					}
				}
				for name := range props {
					if !fields[name] {
						t.Errorf("%s.%s: the contract declares it and the server never sends it", payload.schema, name)
					}
				}
			}
		})
	})

	t.Run("repo", func(t *testing.T) {
		root := repoRoot(t)
		spec, err := os.ReadFile(filepath.Join(root, "api", "openapi-v1.yaml"))
		if err != nil {
			t.Fatalf("read the contract: %v", err)
		}
		contract := contractPairs(string(spec))
		routes, declaringFiles, err := routePairs(root)
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
		unique := slices.Clone(routes)
		slices.Sort(unique)
		unique = slices.Compact(unique)

		// Anti-silence: a parser that found nothing would report the same empty
		// problem list as a contract that covers everything.
		const (
			minDeclaringFiles = 7
			minRoutes         = 25
			minContractPairs  = 25
		)
		if declaringFiles < minDeclaringFiles {
			t.Fatalf("%d files declare RegisterRoutes, expected at least %d — the walker is not reading the tree, so this guard proved nothing", declaringFiles, minDeclaringFiles)
		}
		if len(unique) < minRoutes {
			t.Fatalf("found %d routes, expected at least %d — the extractor is not seeing what it looks for, so this guard proved nothing", len(unique), minRoutes)
		}
		if len(contract) < minContractPairs {
			t.Fatalf("found %d endpoints in the contract, expected at least %d — the contract or the parser is not being read, so this guard proved nothing", len(contract), minContractPairs)
		}

		for _, route := range unique {
			if !slices.Contains(contract, route) {
				t.Errorf("%s: served under /api/v1 but absent from api/openapi-v1.yaml — a client built from the published contract cannot see it", route)
			}
		}
		for _, endpoint := range contract {
			if strings.Contains(endpoint, " /internal/v1/") {
				continue
			}
			if !slices.Contains(unique, endpoint) {
				t.Errorf("%s: named in the contract but no RegisterRoutes serves it — a client would get a 404", endpoint)
			}
		}
	})
}
