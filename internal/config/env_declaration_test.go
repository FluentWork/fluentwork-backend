package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file is a repo-wide guard, not an app-server one. It lives in
// internal/config because this package owns environment-driven configuration
// for the backend, and because nobody else is a natural owner of a question
// that spans every process.
//
// The question it answers: **is every environment variable the code reads
// reachable from a committed template?**
//
// Why it needs to be executable: `configs/app-server.env.example` is not a
// poster. `scripts/dev-up.sh:123-124` *loads* it as the environment when no
// real `.env` exists, and `scripts/smoke-review-ready.sh:73-74` does the same.
// So an example file that omits a key is not a documentation gap — the knob
// does not exist in that developer's environment. The 2026-09-20 device
// detour (see volc.env.example's header, and
// TestVolcEnvExampleCarriesTheGatewayWiring in internal/voicegateway) is what
// that costs.
//
// What the criterion does NOT claim: it does not claim the value is filled.
// There is exactly one template per surface and several of them are templates
// for people to copy, so "mentioned" is the bar for a *documented* knob;
// `Config.Validate` and its gateway twin are what refuse to start when a
// required value is missing. The two are complementary, not substitutes.

// envNameShape matches the environment-variable names this repo writes: an
// upper-case word, digits and underscores, at least three characters.
var envNameShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,}$`)

// envReaderFuncs maps a function name to whether *every* argument names an
// environment variable (true) or only the first one does (false).
//
// Deliberately excluded: firstNonEmpty, which is used on non-environment
// values too. Every entry here either comes from the stdlib or is one of this
// repo's own readers; TestEveryEnvironmentReaderHelperIsRegistered is what
// makes a new helper that is not added here fail loudly instead of silently
// shrinking the scan.
var envReaderFuncs = map[string]bool{
	"Getenv":         true,
	"LookupEnv":      true,
	"envFirst":       true,
	"envOr":          false,
	"intOr":          false,
	"boolOr":         false,
	"durationOr":     false,
	"durationStrict": false,
	"secretOr":       false,
	"envTruthy":      false,
	"envBool":        false,
}

// templateAssignment matches an assignment line in a .env.example, commented
// out or not. A commented assignment counts: the house style in
// configs/volc.env.example is to document an optional knob next to its
// compiled-in default, and a knob documented-but-off is documented.
//
// It has to be assignment-shaped, so prose such as "# fill ARK_API_KEY(_DEV)"
// does not count — that line does not tell a reader the key's name as a key.
var templateAssignment = regexp.MustCompile(`(?m)^\s*#?\s*([A-Z][A-Z0-9_]{2,})\s*=`)

// envSurface is one process (or one family of developer tools) plus the
// templates its environment is assembled from.
type envSurface struct {
	name string
	// template is the committed file that documents this surface's own knobs.
	template string
	// shared are files the same dev path loads alongside template, so a knob
	// documented there is reachable too. Both app-server surfaces share
	// configs/volc.env.example because dev-up.sh loads .env.volc.local right
	// after app-server.env.example (lines 123-128).
	shared []string
	// dirs are the module-relative directories whose env reads belong here.
	dirs []string
	// why records the decision, so the next reader does not have to re-derive
	// whether a dir was put here on purpose.
	why string
}

var envSurfaces = []envSurface{
	{
		name:     "app-server",
		template: "configs/app-server.env.example",
		shared:   []string{"configs/volc.env.example"},
		dirs: []string{
			"internal/config",
			"cmd/app-server",
			"cmd/worker",
			// The corpus seeder writes into the app's own MySQL and talks to a
			// running app-server, so its knobs (MYSQL_DSN, APP_BASE_URL) are
			// app-server knobs pointed at from outside.
			"cmd/corpus-seed",
			// These read APP_ENV and nothing else; they point at a running
			// app-server, so they belong to its environment rather than to an
			// environment of their own.
			"cmd/smoke-corpus",
			"cmd/smoke-daily-read",
			"cmd/smoke-review-ready",
			"cmd/smoke-moat",
			"cmd/eval-moat-flow",
		},
		why: "the app-server process and the tools pointed at it",
	},
	{
		name:     "voice-gateway",
		template: "configs/voice-gateway.env.example",
		shared:   []string{"configs/volc.env.example"},
		dirs:     []string{"internal/voicegateway", "cmd/voice-gateway"},
		why:      "the WSS gateway process",
	},
	{
		name:     "volc tooling",
		template: "configs/volc.env.example",
		dirs: []string{
			"internal/voicepoc",
			// Every one of these already tells the operator to fill
			// .env.volc.local in its own error messages; volc.env.example is
			// that file's template.
			"cmd/poc-injection-window",
			"cmd/smoke-volc-realtime",
			"cmd/asr-wer",
			"cmd/ark-endpoint-probe",
		},
		why: "the voice POC / smoke tools that run against the vendor directly",
	},
}

// envScan is what one walk of the source tree found.
type envScan struct {
	// vars maps a variable name to the module-relative files that read it.
	vars map[string][]string
	// dirs are the module-relative directories containing those files.
	dirs []string
	// unresolved records reader call sites whose key argument could not be
	// traced back to a string literal or a string constant. This is the
	// anti-silence signal: a scan that cannot resolve keys would otherwise
	// look exactly like a scan that found nothing wrong.
	unresolved []string
}

func backendRoot(t *testing.T) string {
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

func scanEnvReads(t *testing.T) envScan {
	t.Helper()
	root := backendRoot(t)
	fset := token.NewFileSet()
	scan := envScan{vars: map[string][]string{}}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (name == "vendor" || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		found := readsInFile(file, fset, rel, &scan)
		if len(found) == 0 {
			return nil
		}
		for _, name := range found {
			scan.vars[name] = append(scan.vars[name], rel)
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if !slices.Contains(scan.dirs, dir) {
			scan.dirs = append(scan.dirs, dir)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	for name, files := range scan.vars {
		slices.Sort(files)
		scan.vars[name] = slices.Compact(files)
	}
	slices.Sort(scan.dirs)
	slices.Sort(scan.unresolved)
	return scan
}

// readsInFile returns the sorted, de-duplicated variable names one file reads.
//
// Four shapes are covered, because all four exist in this tree:
//  1. a literal in a reader call — os.Getenv("MYSQL_DSN"), envOr("HTTP_ADDR", …);
//  2. a package- or function-level string constant passed to a reader —
//     internal/voicepoc's meta12QuotaEnv = "VOLC_META12_QUOTA_OK";
//  3. a []struct{ key string; … } table whose elements are fed to a reader
//     through a field selector — internal/voicegateway's rescue ladder calls
//     durationStrict(knob.key, …), so the key never reaches a call expression;
//  4. a named []string of keys iterated into a reader —
//     cmd/ark-endpoint-probe's `for _, env := range endpointEnvVars`.
//
// Shapes 3 and 4 are scoped to a function that actually feeds a reader, so
// scanning for them cannot turn every unrelated string table in the module into
// a false positive.
func readsInFile(file *ast.File, fset *token.FileSet, rel string, scan *envScan) []string {
	consts, keyLists := declarationsIn(file)

	seen := map[string]bool{}
	var found []string
	add := func(name string) {
		if !envNameShape.MatchString(name) || seen[name] {
			return
		}
		seen[name] = true
		found = append(found, name)
	}

	for _, decl := range file.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc || fn.Body == nil {
			// Package-level declarations read the environment too:
			// `var probeOnlyVar = os.Getenv("PROBE_ONLY_VAR")` is a read, and a
			// scan that only looked inside function bodies would not see it.
			// (A mutation probe found exactly that gap.)
			inspectForReads(decl, map[string]bool{}, nil, false, rel, fset, consts, add, scan)
			continue
		}
		// bound names: parameters, range variables and `:=` locals. A key that
		// this function was handed, or that it derived, is not a call site —
		// `func envFirst(keys ...string)` and the dotenv applier both read a
		// key they were given.
		bound := map[string]bool{}
		if fn.Type.Params != nil {
			for _, field := range fn.Type.Params.List {
				for _, name := range field.Names {
					bound[name.Name] = true
				}
			}
		}
		// rangeKeys: range variables bound to a slice whose elements are keys.
		rangeKeys := map[string][]string{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.RangeStmt:
				name := rangedName(node)
				bound[name] = true
				switch source := node.X.(type) {
				case *ast.Ident:
					rangeKeys[name] = keyLists[source.Name]
				case *ast.CompositeLit:
					rangeKeys[name] = stringElems(source)
				}
			case *ast.AssignStmt:
				if node.Tok == token.DEFINE {
					for _, lhs := range node.Lhs {
						if ident, ok := lhs.(*ast.Ident); ok {
							bound[ident.Name] = true
						}
					}
				}
			}
			return true
		})

		inspectForReads(fn.Body, bound, rangeKeys, feedsReaderANonLiteralKey(fn), rel, fset, consts, add, scan)
	}
	sort.Strings(found)
	return found
}

// inspectForReads walks one body — a function body or a package-level
// declaration — and records every environment variable it reads.
func inspectForReads(
	node ast.Node,
	bound map[string]bool,
	rangeKeys map[string][]string,
	carriesKeyTable bool,
	rel string,
	fset *token.FileSet,
	consts map[string]string,
	add func(string),
	scan *envScan,
) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			everyArgIsAKey, isReader := envReaderFuncs[calleeName(node.Fun)]
			if !isReader {
				return true
			}
			for i, arg := range node.Args {
				if !everyArgIsAKey && i > 0 {
					break
				}
				// Shape 4: the range variable stands for a whole list.
				if ident, ok := arg.(*ast.Ident); ok {
					if keys, ok := rangeKeys[ident.Name]; ok {
						for _, name := range keys {
							add(name)
						}
						continue
					}
				}
				// A field selector is what shape 3 looks like from the
				// call side; the table scan below supplies the value.
				if _, ok := arg.(*ast.SelectorExpr); ok && carriesKeyTable {
					continue
				}
				if name, ok := literalOrConst(arg, consts); ok {
					add(name)
					continue
				}
				if ident, ok := arg.(*ast.Ident); ok && bound[ident.Name] {
					continue
				}
				scan.unresolved = append(scan.unresolved,
					fmt.Sprintf("%s:%d %s(...) key argument is not a literal, a string constant, a key table or a bound name",
						rel, fset.Position(node.Pos()).Line, calleeName(node.Fun)))
			}
		case *ast.CompositeLit:
			if carriesKeyTable {
				for _, name := range tableKeys(node) {
					add(name)
				}
			}
		}
		return true
	})
}

// declarationsIn returns the file's string constants (any scope, so a
// function-local `const k = "X"` resolves) and its package-level []string
// tables whose elements look like environment variable names.
func declarationsIn(file *ast.File) (map[string]string, map[string][]string) {
	consts := map[string]string{}
	tables := map[string][]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				if i >= len(value.Values) {
					continue
				}
				switch expr := value.Values[i].(type) {
				case *ast.BasicLit:
					if expr.Kind != token.STRING {
						continue
					}
					if unquoted, err := strconv.Unquote(expr.Value); err == nil {
						consts[name.Name] = unquoted
					}
				case *ast.CompositeLit:
					if keys := stringElems(expr); len(keys) > 0 {
						tables[name.Name] = keys
					}
				}
			}
		}
	}
	// Function-local constants and key tables are common enough to matter.
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok || i >= len(assign.Rhs) {
				continue
			}
			switch expr := assign.Rhs[i].(type) {
			case *ast.BasicLit:
				if expr.Kind == token.STRING {
					if unquoted, err := strconv.Unquote(expr.Value); err == nil {
						consts[ident.Name] = unquoted
					}
				}
			case *ast.CompositeLit:
				if keys := stringElems(expr); len(keys) > 0 {
					tables[ident.Name] = keys
				}
			}
		}
		return true
	})
	return consts, tables
}

// stringElems returns the env-name-shaped string literals directly inside a
// composite literal, and nothing else — so a []struct{…} table contributes
// nothing here and is left to tableKeys.
func stringElems(lit *ast.CompositeLit) []string {
	var out []string
	for _, element := range lit.Elts {
		basic, ok := element.(*ast.BasicLit)
		if !ok || basic.Kind != token.STRING {
			return nil
		}
		unquoted, err := strconv.Unquote(basic.Value)
		if err != nil || !envNameShape.MatchString(unquoted) {
			return nil
		}
		out = append(out, unquoted)
	}
	return out
}

func rangedName(stmt *ast.RangeStmt) string {
	for _, expr := range []ast.Expr{stmt.Key, stmt.Value} {
		if ident, ok := expr.(*ast.Ident); ok && ident.Name != "_" {
			return ident.Name
		}
	}
	return ""
}

// readBy reports whether any of the module-relative files sit in one of dirs.
func readBy(files, dirs []string) bool {
	for _, file := range files {
		if slices.Contains(dirs, filepath.ToSlash(filepath.Dir(file))) {
			return true
		}
	}
	return false
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// feedsReaderANonLiteralKey reports whether fn passes a field selector to a
// reader, which is how a key table reaches the reader.
func feedsReaderANonLiteralKey(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if _, isReader := envReaderFuncs[calleeName(call.Fun)]; !isReader {
			return true
		}
		for _, arg := range call.Args {
			if _, ok := arg.(*ast.SelectorExpr); ok {
				found = true
			}
		}
		return true
	})
	return found
}

// tableKeys pulls the key column out of a []struct{ key string; … }{…} table:
// the first string literal of each element.
func tableKeys(lit *ast.CompositeLit) []string {
	var keys []string
	for _, element := range lit.Elts {
		inner, ok := element.(*ast.CompositeLit)
		if !ok || len(inner.Elts) == 0 {
			continue
		}
		if _, keyed := inner.Elts[0].(*ast.KeyValueExpr); keyed {
			continue
		}
		first, ok := inner.Elts[0].(*ast.BasicLit)
		if !ok || first.Kind != token.STRING {
			continue
		}
		if unquoted, err := strconv.Unquote(first.Value); err == nil {
			keys = append(keys, unquoted)
		}
	}
	return keys
}

func literalOrConst(arg ast.Expr, consts map[string]string) (string, bool) {
	if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		if unquoted, err := strconv.Unquote(lit.Value); err == nil {
			return unquoted, true
		}
		return "", false
	}
	if ident, ok := arg.(*ast.Ident); ok {
		if value, ok := consts[ident.Name]; ok {
			return value, true
		}
	}
	return "", false
}

// templateMentions reads configs/*.env.example and returns, per file name, the
// set of keys it documents.
func templateMentions(t *testing.T, root string) map[string]map[string]bool {
	t.Helper()
	mentions := map[string]map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(root, "configs"))
	if err != nil {
		t.Fatalf("read configs: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".env.example") {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(root, "configs", entry.Name()))
		if readErr != nil {
			t.Fatalf("read %s: %v", entry.Name(), readErr)
		}
		keys := map[string]bool{}
		for _, match := range templateAssignment.FindAllStringSubmatch(string(body), -1) {
			keys[match[1]] = true
		}
		mentions["configs/"+entry.Name()] = keys
	}
	if len(mentions) == 0 {
		t.Fatal("no configs/*.env.example found; the criterion would pass vacuously")
	}
	return mentions
}

// TestEveryEnvironmentVariableTheBackendReadsIsDeclared is the criterion.
//
// It fails when the code reads a variable that no template its own process is
// assembled from documents, and it fails when a directory that reads variables
// has not been classified — so a new reader cannot quietly land in a tree no
// template covers.
func TestEveryEnvironmentVariableTheBackendReadsIsDeclared(t *testing.T) {
	scan := scanEnvReads(t)

	// Anti-vacuity: a broken walk or a broken extractor finds nothing and
	// looks exactly like a clean tree. The floors are below today's numbers
	// (70 variables across 16 files) so ordinary churn does not trip them.
	if len(scan.vars) < 60 {
		t.Fatalf("source scan found %d variables; it must see at least the ~70 that exist today — check the walk and envReaderFuncs", len(scan.vars))
	}
	if len(scan.dirs) < 14 {
		t.Fatalf("source scan found env reads in %d directories (%v); expected at least 14", len(scan.dirs), scan.dirs)
	}
	// One positive control per extraction shape, so a shape that silently
	// stops working is reported as a shape, not as a missing variable.
	for _, control := range []struct {
		name   string
		reader string
		shape  string
	}{
		{"MYSQL_DSN", "internal/config/config.go", "a literal in a reader call"},
		{"VOLC_META12_QUOTA_OK", "internal/voicepoc/meta12.go", "a string constant in a reader call"},
		{"VOICE_RESCUE_LEVEL1_AFTER", "internal/voicegateway/config.go", "a key table read through a field selector"},
		{"ARK_EP_REVIEW_REFINE", "cmd/ark-endpoint-probe/main.go", "a named []string of keys iterated into a reader"},
	} {
		if !slices.Contains(scan.vars[control.name], control.reader) {
			t.Errorf("the scan did not find %s read by %s, which is %s; that extraction shape has stopped working (found instead in %v)",
				control.name, control.reader, control.shape, scan.vars[control.name])
		}
	}
	if len(scan.unresolved) > 0 {
		t.Errorf("the scan could not resolve %d reader key arguments; a key it cannot see is a key it cannot check:\n  %s",
			len(scan.unresolved), strings.Join(scan.unresolved, "\n  "))
	}

	mentions := templateMentions(t, backendRoot(t))
	documented := func(files []string) map[string]bool {
		keys := map[string]bool{}
		for _, file := range files {
			set, ok := mentions[file]
			if !ok {
				t.Fatalf("%s does not exist", file)
			}
			for key := range set {
				keys[key] = true
			}
		}
		return keys
	}

	for _, surface := range envSurfaces {
		files := append([]string{surface.template}, surface.shared...)
		allowed := documented(files)
		var missing []string
		for name, readers := range scan.vars {
			if allowed[name] || !readBy(readers, surface.dirs) {
				continue
			}
			missing = append(missing, fmt.Sprintf("%s  (read by %s)", name, strings.Join(readers, ", ")))
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Errorf("%s reads %d environment variables that %s does not document.\n"+
				"The template is not a poster: scripts/dev-up.sh loads app-server.env.example as the environment.\n"+
				"Add each of these — commented out next to its compiled-in default is the house style, and is faithful\n"+
				"precisely because every one of them has a default.  Templates checked: %s\n  %s",
				surface.name, len(missing), strings.Join(files, ", "), strings.Join(files, ", "),
				strings.Join(missing, "\n  "))
		}
	}
}

// TestEveryEnvironmentReaderHelperIsRegistered is what keeps the criterion
// above from quietly weakening.
//
// A helper that forwards the key it was handed — func envOr(key, fallback
// string) — can only be seen by the scan if its name is in envReaderFuncs.
// Register a new one late, or not at all, and the keys passed to it are skipped
// as "bound names": the scan still succeeds, still reports no unresolved
// arguments, and silently stops checking whatever that helper reads.
//
// The signal is a parameter: a function that hands Getenv or LookupEnv a name
// it was given is generic over keys, so its call sites are where the keys live.
// A call site that iterates its own key list is not a helper and is not
// reported — which is also why no exception list is needed for the dotenv
// loader, whose key comes out of a file rather than out of a parameter.
func TestEveryEnvironmentReaderHelperIsRegistered(t *testing.T) {
	for _, site := range keyForwardingFunctions(t) {
		if _, registered := envReaderFuncs[site.name]; registered {
			continue
		}
		t.Errorf("%s reads the environment with a key it was handed (%s:%d) but is not in envReaderFuncs,\n"+
			"so every variable passed to it is invisible to the criterion — it will still pass, having checked less.",
			site.name, site.file, site.line)
	}
}

type forwardingSite struct {
	name string
	file string
	line int
}

// keyForwardingFunctions finds the functions that hand Getenv or LookupEnv a
// key derived from one of their own parameters.
func keyForwardingFunctions(t *testing.T) []forwardingSite {
	t.Helper()
	root := backendRoot(t)
	fset := token.NewFileSet()
	var sites []forwardingSite
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (name == "vendor" || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			parameter := map[string]bool{}
			if fn.Type.Params != nil {
				for _, field := range fn.Type.Params.List {
					for _, name := range field.Names {
						parameter[name.Name] = true
					}
				}
			}
			// A variadic parameter is usually consumed by ranging over it, so
			// the range variable counts as the parameter too.
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				stmt, ok := n.(*ast.RangeStmt)
				if !ok {
					return true
				}
				source, ok := stmt.X.(*ast.Ident)
				if !ok || !parameter[source.Name] {
					return true
				}
				if name := rangedName(stmt); name != "" {
					parameter[name] = true
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if name := calleeName(call.Fun); name != "Getenv" && name != "LookupEnv" {
					return true
				}
				for _, arg := range call.Args {
					ident, ok := arg.(*ast.Ident)
					if !ok || !parameter[ident.Name] {
						continue
					}
					sites = append(sites, forwardingSite{
						name: fn.Name.Name,
						file: rel,
						line: fset.Position(call.Pos()).Line,
					})
					return true
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	// Anti-vacuity: this tree defines reader helpers in four packages. Finding
	// none means the walk or the call matching broke, not that the tree is clean.
	if len(sites) < 6 {
		t.Fatalf("found only %d key-forwarding functions; the tree has reader helpers in internal/config, internal/voicegateway and cmd/", len(sites))
	}
	return sites
}

// TestEveryEnvironmentReadingDirectoryIsClassified guards the guard: the
// criterion above can only see the directories it was told about.
func TestEveryEnvironmentReadingDirectoryIsClassified(t *testing.T) {
	scan := scanEnvReads(t)
	if len(scan.dirs) < 14 {
		t.Fatalf("source scan found env reads in only %d directories; expected at least 14", len(scan.dirs))
	}
	classified := map[string]string{}
	for _, surface := range envSurfaces {
		for _, dir := range surface.dirs {
			if previous, clash := classified[dir]; clash {
				t.Errorf("%s is listed under both %s and %s", dir, previous, surface.name)
			}
			classified[dir] = surface.name
		}
	}
	for _, dir := range scan.dirs {
		if _, ok := classified[dir]; !ok {
			t.Errorf("%s reads environment variables but belongs to no surface in envSurfaces.\n"+
				"Put it under the surface whose template should document its knobs — do not leave it unclassified,\n"+
				"or the criterion above will never look at it.", dir)
		}
	}
	for dir, surface := range classified {
		if !slices.Contains(scan.dirs, dir) {
			t.Errorf("%s is listed under %s but reads no environment variables; drop the stale entry", dir, surface)
		}
	}
}
