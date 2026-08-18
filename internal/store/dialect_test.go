package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRebindSQLiteIsIdentity(t *testing.T) {
	q := `SELECT * FROM t WHERE a = ? AND b = ? AND c = 'x?y'`
	if got := rebind(BackendSQLite, q); got != q {
		t.Fatalf("sqlite rebind changed the query:\n got: %s\nwant: %s", got, q)
	}
}

func TestRebindPostgresPlaceholders(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1", "SELECT 1"},
		{"WHERE a = ?", "WHERE a = $1"},
		{"WHERE a = ? AND b = ?", "WHERE a = $1 AND b = $2"},
		{"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", "VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)"},
	}
	for _, c := range cases {
		if got := rebind(BackendPostgres, c.in); got != c.want {
			t.Errorf("rebind(%q):\n got: %s\nwant: %s", c.in, got, c.want)
		}
	}
}

func TestRebindSkipsSingleQuotedLiterals(t *testing.T) {
	// A `?` inside a literal must be preserved verbatim; placeholders outside
	// still convert, and their numbering is unaffected by the literal.
	in := `SELECT ? , 'a?b' , ? , 'it''s ok?' , ?`
	want := `SELECT $1 , 'a?b' , $2 , 'it''s ok?' , $3`
	if got := rebind(BackendPostgres, in); got != want {
		t.Fatalf("rebind:\n got: %s\nwant: %s", got, want)
	}
}

func TestRebindLikeConcat(t *testing.T) {
	// The team-handle and session-id queries use `LIKE ? || '%'`: the ? is a
	// placeholder, the '%' is a literal.
	in := `uid LIKE ? || '%'`
	want := `uid LIKE $1 || '%'`
	if got := rebind(BackendPostgres, in); got != want {
		t.Fatalf("rebind:\n got: %s\nwant: %s", got, want)
	}
}

// TestNoQuestionMarkInsideLiterals is the guard the rebinder relies on: no
// local-store query may hide a `?` inside a single-quoted SQL literal, because
// the Postgres rewrite treats every out-of-literal `?` as a positional
// placeholder. It parses every Go file in the local (non-cloud) packages and
// inspects each raw-string literal.
func TestNoQuestionMarkInsideLiterals(t *testing.T) {
	root := filepath.Join("..", "..")
	pkgs := []string{
		"internal/store", "internal/stats", "internal/syncer",
		"internal/memsync", "internal/diag", "internal/cli",
	}
	fset := token.NewFileSet()
	for _, pkg := range pkgs {
		dir := filepath.Join(root, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			// Only production .go files: test fixtures deliberately hold
			// `?`-in-literal strings.
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if !strings.HasPrefix(lit.Value, "`") {
					return true // only raw strings hold SQL here
				}
				if q := questionMarkInLiteral(lit.Value); q >= 0 {
					t.Errorf("%s: `?` inside a single-quoted SQL literal at byte %d — the rebinder would mis-count placeholders:\n%s", name, q, lit.Value)
				}
				return true
			})
		}
	}
}

// questionMarkInLiteral returns the byte offset of the first `?` found inside a
// single-quoted literal within s, or -1 if none. Mirrors rebind's scanner.
func questionMarkInLiteral(s string) int {
	inLiteral := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inLiteral {
			if c == '?' {
				return i
			}
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inLiteral = false
			}
			continue
		}
		if c == '\'' {
			inLiteral = true
		}
	}
	return -1
}
