package config

// This file is the second repo-wide guard next to env_declaration_test.go, and
// it lives here for the same reason that one gives: the question spans every
// package and nobody else is a natural owner.
//
// The question: **is a doc comment attached to the declaration it names?**
//
// Why this is not pedantry. In Go the doc comment is the comment group whose
// last line sits immediately above the declaration. Two edits break that
// relationship, and neither is visible to the compiler, the tests, or the
// linter:
//
//   - a blank line between the comment and the declaration. `Decl.Doc` becomes
//     nil. The prose stays in the file, still reads like documentation, and is
//     now invisible to `go doc` and to every editor's hover. That is how
//     extractServerASRText came to sit under seven lines of documentation of
//     which the compiler saw none — three of them describing
//     `resolvedUserText`, a function that exists nowhere in this repo.
//   - a second doc block pasted onto the first with no blank line. The two
//     merge into one group, and the declaration below it is now documented by
//     prose about something else — while the thing that prose describes is left
//     with no doc at all.
//
// The line this guard walks. It inspects only comment blocks whose first word
// is the name of a top-level declaration. Go's documenting convention is that a
// doc comment opens with the name of the thing it documents, so that is a
// precise signal rather than a heuristic: it steps over section dividers
// (`// --- types ---`), file-level preambles, ticket references (`// #21 …`) and
// ordinary prose, which is what keeps it from crying wolf.
//
// The three things it deliberately does NOT claim:
//
//   - A comment above a grouped declaration (`// Session statuses …` over
//     `const ( … )`) is left alone even when its first word happens to name some
//     other declaration in the file. The group has no name of its own to compare
//     against, and "Session statuses" is a phrase, not a claim that the comment
//     documents `type Session`. Guessing there means guessing with English.
//   - Interface method docs are left alone. They attach correctly already; the
//     name they open with is a method of the interface, not a top-level
//     declaration.
//   - A block that opens with a name this file does not declare is left alone,
//     even when a declaration of that name exists elsewhere in the repo. The
//     only signal this guard trusts is "the name it opens with is a declaration
//     in the same file" — widening it to the whole repo would start flagging
//     ordinary prose that happens to open with a common word ("Todo:", "Check
//     the caller."). Both of the last two were measured against the tree: every
//     block they would flag was prose, not a detached doc.

import (
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

// unattachedDoc is one comment block whose opening name and whose attachment
// disagree.
type unattachedDoc struct {
	path  string // repo-relative
	line  int    // 1-based line of the block's first line
	word  string // the declaration name the block opens with
	first string // the block's first line, trimmed, for the message
	why   string // which of the two shapes this is
}

// docScan is what one full pass over the tree saw. The counters are the
// anti-silence signal: a walk that parsed nothing would otherwise produce an
// empty `found` and look exactly like a clean tree.
type docScan struct {
	files int
	decls int
	found []unattachedDoc
}

// docShapes are the two ways a doc block and its declaration come apart.
const (
	shapeDetached = "a blank line separates this block from the declaration below it, so Decl.Doc is nil"
	shapeMerged   = "this block is pasted onto the doc of the declaration below it, so that declaration is documented by prose about something else"
)

// unattachedDocComments reports the doc-like comment blocks in one file that do
// not agree with the declaration they name.
//
// A block is attached when its last line is exactly one line above a top-level
// declaration — the relationship `go/ast` uses to populate Decl.Doc. A grouped
// declaration (`const (`) is deliberately not keyed: it has no name of its own.
func unattachedDocComments(path string, src []byte) ([]unattachedDoc, int, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, 0, err
	}

	// Line -> the top-level declaration that starts there. Grouped declarations
	// contribute their specs, not the `const (`/`type (` line itself.
	declAtLine := map[int]string{}
	declNames := map[string]bool{}
	declCount := 0
	note := func(name string, line int) {
		if name == "" || name == "_" {
			return
		}
		declCount++
		declNames[name] = true
		if _, dup := declAtLine[line]; !dup {
			declAtLine[line] = name
		}
	}
	for _, d := range file.Decls {
		switch x := d.(type) {
		case *ast.FuncDecl:
			note(x.Name.Name, fset.Position(x.Pos()).Line)
		case *ast.GenDecl:
			for _, spec := range x.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					note(s.Name.Name, fset.Position(s.Pos()).Line)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						note(n.Name, fset.Position(s.Pos()).Line)
					}
				}
			}
		}
	}

	lines := strings.Split(string(src), "\n")
	// codeLine reports the first line at or after `from` that is neither blank
	// nor a whole-line comment, or 0 if there is none.
	codeLine := func(from int) int {
		for ln := from; ln >= 1 && ln <= len(lines); ln++ {
			t := strings.TrimSpace(lines[ln-1])
			if t != "" && !strings.HasPrefix(t, "//") {
				return ln
			}
		}
		return 0
	}
	// Comments inside a function body are code comments; they are allowed to
	// open with anything, including the name of the enclosing function.
	inBody := func(ln int) bool {
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if ln >= fset.Position(fd.Body.Lbrace).Line && ln <= fset.Position(fd.Body.Rbrace).Line {
				return true
			}
		}
		return false
	}

	var out []unattachedDoc
	for _, cg := range file.Comments {
		if len(cg.List) == 0 {
			continue
		}
		start := fset.Position(cg.Pos()).Line
		end := fset.Position(cg.End()).Line
		if inBody(start) || inBody(end) {
			continue
		}
		word := leadingIdentifier(cg.List[0].Text)
		if word == "" {
			continue
		}
		first := strings.TrimSpace(strings.TrimPrefix(cg.List[0].Text, "//"))

		// Merge shape: the block does attach to something, but it opens with
		// the name of some other declaration.
		if attachedName, ok := declAtLine[end+1]; ok {
			if word != attachedName && declNames[word] {
				out = append(out, unattachedDoc{path, start, word, first, shapeMerged})
			}
			continue
		}

		// Detach shape: the next declaration is the one this block names, but a
		// blank line (or another block) keeps them apart.
		below := codeLine(end + 1)
		if below == 0 || below == end+1 {
			continue
		}
		if name, ok := declAtLine[below]; ok && name == word {
			out = append(out, unattachedDoc{path, start, word, first, shapeDetached})
		}
	}
	return out, declCount, nil
}

// leadingIdentifier returns the first token of a comment line with Go
// punctuation trimmed: "// Foo returns a thing." yields "Foo", "// Foo(" yields
// "Foo", and "// --- types ---" yields "---" (which names nothing, so it is
// skipped by every caller).
func leadingIdentifier(text string) string {
	text = strings.TrimSpace(strings.TrimPrefix(text, "//"))
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimRight(fields[0], "([{,;:.)]}=*")
}

func TestDocumentationCommentsAttachToTheirDeclaration(t *testing.T) {
	// Positive controls come in both directions: the guard has to bite on the
	// shapes it exists for and stay quiet on the shapes that merely look
	// adjacent. If the extractor breaks, these say so before the repo-wide pass
	// can report a clean tree.
	t.Run("positive controls", func(t *testing.T) {
		cases := []struct {
			name string
			src  string
			want []string
		}{
			{
				name: "blank line between doc and declaration",
				src:  "package p\n\n// Foo returns a thing.\n\nfunc Foo() {}\n",
				want: []string{"Foo"},
			},
			{
				name: "second block stacked above the attached one",
				src:  "package p\n\n// Foo returns a thing.\n\n// Foo returns a thing, twice.\nfunc Foo() {}\n",
				want: []string{"Foo"},
			},
			{
				name: "doc naming a declaration that sits elsewhere in the file",
				src:  "package p\n\n// Foo returns a thing.\n\nfunc Bar() {}\n\nfunc Foo() {}\n",
				want: nil, // beyond the line: see the boundary note in the header
			},
			{
				name: "method doc separated by a blank line",
				src:  "package p\n\ntype S struct{}\n\n// Start begins.\n\nfunc (s *S) Start() {}\n",
				want: []string{"Start"},
			},
			{
				name: "two docs merged with no blank line between them",
				src:  "package p\n\n// Foo returns a thing.\n// Bar returns a thing.\nfunc Bar() {}\n\nfunc Foo() {}\n",
				want: []string{"Foo"},
			},
			{
				name: "merged block naming a word that only looks like a declaration",
				src:  "package p\n\n// Todo: revisit this.\n// Bar returns a thing.\nfunc Bar() {}\n",
				want: nil, // beyond the line: only a real declaration name is a signal
			},
			{
				name: "attached doc is left alone",
				src:  "package p\n\n// Foo returns a thing.\nfunc Foo() {}\n",
				want: nil,
			},
			{
				name: "each declaration carrying its own attached doc",
				src:  "package p\n\n// Bar returns a thing.\nfunc Bar() {}\n\n// Foo returns a thing.\nfunc Foo() {}\n",
				want: nil,
			},
			{
				name: "section divider names nothing",
				src:  "package p\n\n// --- types ---\n\ntype Foo struct{}\n",
				want: nil,
			},
			{
				name: "prose opening with an ordinary word",
				src:  "package p\n\n// This is a note about the file.\n\ntype Foo struct{}\n",
				want: nil,
			},
			{
				name: "a block opening with a name that exists in another file",
				src:  "package p\n\n// Elsewhere returns a thing.\n\ntype Foo struct{}\n",
				want: nil,
			},
			{
				name: "comment inside a body may open with anything",
				src:  "package p\n\nfunc Foo() {\n\t// Foo does the thing below.\n\t_ = 1\n}\n",
				want: nil,
			},
			{
				name: "group doc naming another declaration in the file",
				src:  "package p\n\n// Session statuses for the lifecycle.\nconst (\n\tStatusCreated = \"created\"\n)\n\ntype Session struct{}\n",
				want: nil,
			},
			{
				name: "interface method doc",
				src:  "package p\n\ntype S interface {\n\t// Insert appends one attempt.\n\tInsert() error\n}\n",
				want: nil,
			},
			{
				name: "doc splitting a word that is only part of a declaration name",
				src:  "package p\n\n// Session statuses for the lifecycle.\n\nconst StatusCreated = \"created\"\n",
				want: nil,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, _, err := unattachedDocComments("sample.go", []byte(tc.src))
				if err != nil {
					t.Fatalf("parse sample: %v", err)
				}
				var words []string
				for _, f := range got {
					words = append(words, f.word)
				}
				if !slices.Equal(words, tc.want) {
					t.Fatalf("got %v, want %v\n%s", words, tc.want, tc.src)
				}
			})
		}
	})

	t.Run("repo", func(t *testing.T) {
		root := backendRoot(t)
		scan := docScan{}

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
			if !strings.HasSuffix(entry.Name(), ".go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			found, decls, parseErr := unattachedDocComments(rel, src)
			if parseErr != nil {
				return parseErr
			}
			scan.files++
			scan.decls += decls
			scan.found = append(scan.found, found...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}

		// Anti-silence: a walk that failed to read or parse would otherwise
		// present the same empty result as a clean tree.
		const minFiles, minDecls = 120, 1200
		if scan.files < minFiles {
			t.Fatalf("walked %d Go files, expected at least %d — the scan is not seeing the repo", scan.files, minFiles)
		}
		if scan.decls < minDecls {
			t.Fatalf("collected %d top-level declarations, expected at least %d — the extractor is not seeing the source", scan.decls, minDecls)
		}

		for _, f := range scan.found {
			t.Errorf("%s:%d — this block opens with %q, but %s:\n    %s", f.path, f.line, f.word, f.why, f.first)
		}
	})
}
