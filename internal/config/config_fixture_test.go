package config

// This file is the fourth repo-wide guard next to env_declaration_test.go,
// doc_attachment_test.go and readme_layout_test.go, and it lives here for the
// reason the second one gives: the question spans the whole repository and no
// package is a natural owner.
//
// The question: **does a test file write out the full service configuration
// again, instead of asking for the one shared copy?**
//
// `config.Config` has one shape that says "a couple of fields are what this
// test is about" and another that says "a configuration a service can be
// started with". Only the second one is a fixture, and the repository had
// thirty-five copies of it across fourteen files. They had already drifted:
//
//   - four different field sets, none of them written down anywhere;
//   - one duration with three spellings: SessionTicketTTL as time.Minute in 17
//     copies, 60*time.Second in 11 and 60_000_000_000 in 1; AccessTokenTTL as
//     2*time.Hour in 14 and time.Hour in 3;
//   - the same voice-gateway URL given two hosts, ws://example.test and
//     ws://127.0.0.1:8081, with nothing saying which was meant;
//   - five of them, missing InternalAPIToken, could not pass Config.Validate at
//     all — so "the config a test runs against" had two mutually contradictory
//     definitions, and no check anywhere noticed.
//
// Nothing caught any of this. The compiler accepts a keyed literal with three
// fields as readily as one with eight, and every copy that was already wrong
// still ran.
//
// Where the line is drawn: a literal that spells out maxInlineFields or more
// fields is a fixture. The threshold is not freehand — the field counts in this
// repository are 0, 1, 2, 3, then 5, 6, 8, 9. Four has never been used, so the
// rule falls in a gap and cannot force anyone to rewrite a test that was
// already right.
//
// What this guard does NOT claim:
//
//   - It reads `_test.go` files only. The one surviving definition lives in a
//     non-test file (internal/configtest/config.go), which is out of scope by
//     construction rather than by an exemption list.
//   - It matches `config.Config` exactly. `voicegateway.Config` is deliberately
//     left alone: its fixtures differ from one another *because* the tests are
//     about validation, so a differing field set is the point, not drift.
//   - A renamed import (`cfg ".../internal/config"`) would hide a literal from
//     this guard. No such import exists today; closing it is a different rule.
//   - It says nothing about what a config literal contains, only how many
//     fields it spells out.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// maxInlineFields is the first field count that means "fixture" rather than
// "this test needs a couple of fields". See the file header for why it is 4.
const maxInlineFields = 4

// configScan is what one test file contributed.
type configScan struct {
	total  int      // every `config.Config{...}` literal in the file
	inline []string // the ones spelling out maxInlineFields or more
}

// scanConfigLiterals parses one file and counts the fields of every
// `config.Config` composite literal in it. Anything that is not a `_test.go`
// file is out of scope and reports nothing.
func scanConfigLiterals(filename string, src []byte) (configScan, error) {
	if !strings.HasSuffix(filename, "_test.go") {
		return configScan{}, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return configScan{}, err
	}
	var scan configScan
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isConfigSelector(lit.Type) {
			return true
		}
		scan.total++
		var fields []string
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok {
				fields = append(fields, key.Name)
			}
		}
		if len(fields) < maxInlineFields {
			return true
		}
		scan.inline = append(scan.inline, fmt.Sprintf(
			"%s:%d: config.Config literal spells out %d fields (%s) — the shared fixture is configtest.Config(); override only the field this test is about",
			filename, fset.Position(lit.Pos()).Line, len(fields), strings.Join(fields, ", "),
		))
		return true
	})
	return scan, nil
}

// isConfigSelector reports whether a composite literal's type is written
// `config.Config`.
func isConfigSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Config" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "config"
}

func TestTheFullTestConfigIsWrittenOnce(t *testing.T) {
	// Positive controls come in both directions: the guard has to bite on the
	// shapes it exists for and stay quiet on the shapes that only look close.
	t.Run("positive controls", func(t *testing.T) {
		cases := []struct {
			name  string
			file  string
			src   string
			total int
			want  int
		}{
			{
				name:  "a literal that is about a couple of fields",
				file:  "a_test.go",
				src:   "package p\n\nvar _ = config.Config{SessionTicketTTL: time.Minute}\n",
				total: 1,
			},
			{
				name:  "the empty literal",
				file:  "a_test.go",
				src:   "package p\n\nvar _ = config.Config{}\n",
				total: 1,
			},
			{
				name:  "three fields is still about a couple of them",
				file:  "a_test.go",
				src:   "package p\n\nvar _ = config.Config{HTTPAddr: \":0\", AppEnv: \"development\", AuthJWTSecret: config.DevJWTSecret}\n",
				total: 1,
			},
			{
				name:  "four fields is where the fixture starts",
				file:  "a_test.go",
				src:   "package p\n\nvar _ = config.Config{HTTPAddr: \":0\", AppEnv: \"development\", AuthJWTSecret: config.DevJWTSecret, AccessTokenTTL: time.Hour}\n",
				total: 1,
				want:  1,
			},
			{
				name:  "a copy of the old fixture",
				file:  "a_test.go",
				src:   "package p\n\nvar _ = config.Config{HTTPAddr: \":0\", AppEnv: \"development\", AuthJWTSecret: config.DevJWTSecret, AccessTokenTTL: time.Hour, RefreshTokenTTL: time.Hour, VoiceGatewayWSSURL: \"x\", SessionTicketTTL: time.Minute, InternalAPIToken: \"y\"}\n",
				total: 1,
				want:  1,
			},
			{
				name:  "nested literals count their own fields, not the outer one's",
				file:  "a_test.go",
				src:   "package p\n\nvar _ = config.Config{HTTPAddr: \":0\", AppEnv: \"development\"}\n",
				total: 1,
			},
			{
				name:  "another package's Config is not this fixture",
				file:  "a_test.go",
				src:   "package p\n\nvar _ = voicegateway.Config{HTTPAddr: \":0\", AppEnv: \"development\", AppServerInternalURL: \"u\", InternalAPIToken: \"t\", Provider: \"p\", IdleTimeout: time.Minute}\n",
				total: 0,
				want:  0,
			},
			{
				name:  "a non-test file is out of scope",
				file:  "configtest/config.go",
				src:   "package p\n\nfunc Config() config.Config {\n\treturn config.Config{HTTPAddr: \":0\", AppEnv: \"development\", AuthJWTSecret: config.DevJWTSecret, AccessTokenTTL: time.Hour, RefreshTokenTTL: time.Hour, VoiceGatewayWSSURL: \"x\", SessionTicketTTL: time.Minute, InternalAPIToken: \"y\"}\n}\n",
				total: 0,
				want:  0,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := scanConfigLiterals(tc.file, []byte(tc.src))
				if err != nil {
					t.Fatalf("scan: %v", err)
				}
				if got.total != tc.total {
					t.Fatalf("counted %d config.Config literals, want %d", got.total, tc.total)
				}
				if len(got.inline) != tc.want {
					t.Fatalf("reported %d fixtures, want %d\n%s", len(got.inline), tc.want, strings.Join(got.inline, "\n"))
				}
			})
		}
	})

	t.Run("repo", func(t *testing.T) {
		root := backendRoot(t)
		var (
			testFiles int
			literals  int
			problems  []string
		)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if name := entry.Name(); name == ".git" || name == "vendor" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			testFiles++
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			scan, scanErr := scanConfigLiterals(rel, src)
			if scanErr != nil {
				return fmt.Errorf("%s: %w", rel, scanErr)
			}
			literals += scan.total
			problems = append(problems, scan.inline...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}

		// Anti-silence: a parser that found nothing would report the same empty
		// problem list as a repository that has nothing to report.
		const (
			minTestFiles = 150
			minLiterals  = 15
		)
		if testFiles < minTestFiles {
			t.Fatalf("walked %d test files, expected at least %d — the tree or the walker is not being read, so this guard proved nothing", testFiles, minTestFiles)
		}
		if literals < minLiterals {
			t.Fatalf("found %d config.Config literals, expected at least %d — the extractor is not seeing what it is looking for, so this guard proved nothing", literals, minLiterals)
		}

		slices.Sort(problems)
		for _, problem := range problems {
			t.Errorf("%s", problem)
		}
	})
}
