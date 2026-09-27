package config

// This file is the third repo-wide guard next to env_declaration_test.go and
// doc_attachment_test.go, and it lives here for the reason the second one
// gives: the question spans the whole repository and no package is a natural
// owner.
//
// The question: **does the `Path` column of README.md's Layout table describe a
// tree that is actually here?**
//
// The Layout table is the front door — the one place a reader is told what
// this repository holds. It was also the one place nothing ever checked. A row
// can name a directory that was never created, or one that exists as an empty
// shell kept alive by a one-byte `.gitkeep`, and every other check in this
// repository stays green: the compiler does not read Markdown, the linter does
// not either, and `go build ./...` has nothing to say about a path that holds
// no Go.
//
// That was not hypothetical. `test/` sat in this table as "test support" while
// holding exactly one file, `.gitkeep`. A reader who believed the table went
// looking for cross-layer tests and found an empty directory.
//
// Scope, deliberately narrow: the `## Layout` section of README.md and only its
// first column. What it does NOT claim:
//
//   - A token containing a glob metacharacter (`*`, `?`, `[`) is skipped. A
//     glob is a claim about matching, not about identity; checking it is a
//     different rule and none is written yet.
//   - Prose in the Layout section, and every other section of README.md, is not
//     read at all. This guard checks the table, not the file.
//   - A directory is only reported when the table names it. An empty directory
//     the table says nothing about is not this guard's business.
//   - `.gitkeep` is not content. It is the placeholder whose presence next to
//     nothing is the shape being caught, so counting it would leave the guard
//     unable to see what it is looking for.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// readmeLayoutPaths returns the `Path` column of README's Layout table: one
// entry per comma-separated token, with backticks stripped.
func readmeLayoutPaths(readme string) []string {
	var out []string
	inLayout := false
	for _, line := range strings.Split(readme, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inLayout = trimmed == "## Layout"
			continue
		}
		if !inLayout || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) < 3 {
			continue
		}
		first := strings.TrimSpace(cells[1])
		if first == "Path" || isTableRule(first) {
			continue
		}
		for _, raw := range strings.Split(first, ",") {
			if token := strings.Trim(strings.TrimSpace(raw), "`"); token != "" {
				out = append(out, token)
			}
		}
	}
	return out
}

// isTableRule reports whether a cell is a Markdown table's `---` separator row.
func isTableRule(cell string) bool {
	if cell == "" {
		return false
	}
	for _, r := range cell {
		if r != '-' && r != ':' {
			return false
		}
	}
	return true
}

// layoutProblems reports every token in README's Layout table that the tree
// under root does not back up. A token ending in `/` names a directory, which
// has to exist and hold something; any other token names a path, which only has
// to exist.
func layoutProblems(root string, tokens []string) []string {
	var out []string
	for _, token := range tokens {
		if strings.ContainsAny(token, "*?[") {
			continue
		}
		wantDir := strings.HasSuffix(token, "/")
		target := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(token, "/")))
		info, err := os.Stat(target)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: named in README's Layout table, but there is nothing at that path", token))
			continue
		}
		if !wantDir {
			continue
		}
		if !info.IsDir() {
			out = append(out, fmt.Sprintf("%s: written as a directory in README's Layout table, but the path is a file", token))
			continue
		}
		contents, err := layoutContents(target)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: cannot read that directory: %v", token, err))
			continue
		}
		if contents == 0 {
			out = append(out, fmt.Sprintf("%s: named in README's Layout table, but the directory holds nothing (a lone .gitkeep does not count)", token))
		}
	}
	return out
}

// layoutContents counts the files a reader would find under dir, with
// `.gitkeep` excluded (see the note in the file header).
func layoutContents(dir string) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() == ".gitkeep" {
			return nil
		}
		count++
		return nil
	})
	return count, err
}

// makeDir creates root/rel and drops one file per name into it.
func makeDir(t *testing.T, root, rel string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	for _, name := range names {
		makeFile(t, root, rel+name)
	}
}

// makeFile creates root/rel with one byte of content.
func makeFile(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestReadmeLayoutNamesOnlyWhatIsThere(t *testing.T) {
	// Positive controls come in both directions: the guard has to bite on the
	// shapes it exists for and stay quiet on the shapes that only look close.
	t.Run("positive controls", func(t *testing.T) {
		root := t.TempDir()
		makeDir(t, root, "kept/", "main.go")
		makeDir(t, root, "shell/", ".gitkeep")
		makeDir(t, root, "half/", ".gitkeep", "notes.md")
		makeFile(t, root, "single.yml")

		cases := []struct {
			name   string
			tokens []string
			want   []string
		}{
			{
				name:   "a directory that exists and holds something",
				tokens: []string{"kept/"},
				want:   nil,
			},
			{
				name:   "a directory kept alive by .gitkeep alone",
				tokens: []string{"shell/"},
				want:   []string{"shell/"},
			},
			{
				name:   "a directory whose content outweighs its .gitkeep",
				tokens: []string{"half/"},
				want:   nil,
			},
			{
				name:   "a directory that is not there at all",
				tokens: []string{"gone/"},
				want:   []string{"gone/"},
			},
			{
				name:   "a file that is there",
				tokens: []string{"single.yml"},
				want:   nil,
			},
			{
				name:   "a file that is not there",
				tokens: []string{"missing.yml"},
				want:   []string{"missing.yml"},
			},
			{
				name:   "a token written as a directory but backed by a file",
				tokens: []string{"single.yml/"},
				want:   []string{"single.yml/"},
			},
			{
				name:   "a glob is beyond this rule",
				tokens: []string{"kept/**", "kept/*.go"},
				want:   nil,
			},
			{
				name:   "only the tokens with a problem come back",
				tokens: []string{"kept/", "gone/", "half/", "missing.yml", "single.yml"},
				want:   []string{"gone/", "missing.yml"},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := layoutProblems(root, tc.tokens)
				names := make([]string, 0, len(got))
				for _, problem := range got {
					names = append(names, strings.SplitN(problem, ":", 2)[0])
				}
				if !slices.Equal(names, tc.want) {
					t.Fatalf("got %v, want %v\n%v", names, tc.want, got)
				}
			})
		}
	})

	t.Run("the Path column", func(t *testing.T) {
		cases := []struct {
			name   string
			readme string
			want   []string
		}{
			{
				name:   "one path alone",
				readme: "## Layout\n\n| Path | Contents |\n|---|---|\n| `cmd/` | services |\n",
				want:   []string{"cmd/"},
			},
			{
				name:   "two paths sharing a row",
				readme: "## Layout\n\n| Path | Contents |\n|---|---|\n| `eval/`, `test/` | datasets |\n",
				want:   []string{"eval/", "test/"},
			},
			{
				name:   "a path without backticks",
				readme: "## Layout\n\n| Path | Contents |\n|---|---|\n| pkg/ | libraries |\n",
				want:   []string{"pkg/"},
			},
			{
				name:   "rows after the section ends are not read",
				readme: "## Layout\n\n| Path | Contents |\n|---|---|\n| `cmd/` | x |\n\n## CI\n\n| Step | Runs |\n|---|---|\n| `gone/` | y |\n",
				want:   []string{"cmd/"},
			},
			{
				name:   "prose in the section is not a row",
				readme: "## Layout\n\nSee `gone/` for details.\n\n| Path | Contents |\n|---|---|\n| `cmd/` | x |\n",
				want:   []string{"cmd/"},
			},
			{
				name:   "no Layout section at all",
				readme: "# Title\n\n## CI\n\n| Step | Runs |\n|---|---|\n| lint | green |\n",
				want:   nil, // the repo-wide floor below is what catches this shape
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := readmeLayoutPaths(tc.readme); !slices.Equal(got, tc.want) {
					t.Fatalf("got %v, want %v\n%s", got, tc.want, tc.readme)
				}
			})
		}
	})

	t.Run("repo", func(t *testing.T) {
		root := backendRoot(t)
		readme, err := os.ReadFile(filepath.Join(root, "README.md"))
		if err != nil {
			t.Fatalf("read README.md: %v", err)
		}
		tokens := readmeLayoutPaths(string(readme))

		// Anti-silence: a parser that found nothing would report the same empty
		// problem list as a README that tells the truth.
		const minTokens = 8
		if len(tokens) < minTokens {
			t.Fatalf("parsed %d paths from README's Layout table, expected at least %d — the table or the parser is not being read, so this guard proved nothing", len(tokens), minTokens)
		}

		for _, problem := range layoutProblems(root, tokens) {
			t.Errorf("%s", problem)
		}
	})
}
