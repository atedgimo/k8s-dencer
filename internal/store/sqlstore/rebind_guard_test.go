package sqlstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// Every query in this package is written with `?` placeholders and rewritten
// for Postgres by rebind, which only runs if the call goes through exec,
// query, queryRow or txExec. A statement handed straight to the driver keeps
// its `?` and Postgres rejects it — "syntax error at end of input", pointing
// at nothing in particular.
//
// Two calls did exactly that, and neither was caught by review or by the
// SQLite suite; they surfaced only when the store first spoke to a real
// Postgres, as thirteen failures with one cause. The cost of finding them that
// way is what this test exists to avoid: it is a compile-time-shaped question
// — does a placeholder reach the driver unrewritten — so it should not need a
// server to answer.
//
// Raw calls are still allowed. DDL is already translated by the time it is
// executed, the seq backfill is SQLite-only, and PRAGMA takes no parameters —
// what none of them may contain is a `?`.
func TestNoPlaceholderBypassesRebind(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	// The driver-level methods. Reaching one of these means rebind was
	// skipped, whatever the receiver is called.
	// PrepareContext belongs here too, and its absence is not hypothetical:
	// SaveNodeSamples prepared a `?`-bearing statement directly and this
	// guard waved it through, so per-node history was silently dead on
	// Postgres for the whole of v0.6.0. A guard is only worth the confidence
	// it gives if it covers every way to reach the driver.
	raw := map[string]bool{
		"ExecContext":     true,
		"QueryContext":    true,
		"QueryRowContext": true,
		"PrepareContext":  true,
	}

	var checked int
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			// dialect.go is where the helpers wrap the driver; those calls are
			// the rewrite, not a bypass of it.
			if strings.HasSuffix(name, "dialect.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !raw[sel.Sel.Name] || len(call.Args) < 2 {
					return true
				}
				checked++
				if !containsPlaceholder(call.Args[1]) {
					return true
				}
				pos := fset.Position(call.Pos())
				t.Errorf("%s:%d: %s is called directly with a query containing `?`, so rebind never runs and Postgres will reject it — route it through s.exec/s.query/s.queryRow/s.txExec",
					pos.Filename, pos.Line, sel.Sel.Name)
				return true
			})
		}
	}

	// A test that silently inspects nothing would pass forever after a
	// refactor renamed the methods it looks for.
	if checked == 0 {
		t.Fatal("found no direct driver calls at all; this guard is no longer looking at anything")
	}
}

// containsPlaceholder reports whether a query expression has a `?` in any of
// its literal parts. Concatenation is handled because the claim query builds
// its locking clause that way.
func containsPlaceholder(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING && strings.Contains(v.Value, "?")
	case *ast.BinaryExpr:
		return containsPlaceholder(v.X) || containsPlaceholder(v.Y)
	case *ast.ParenExpr:
		// `(\`…?…\`)` is the same literal wearing a hat, and a guard that a
		// pair of brackets defeats is not a guard.
		return containsPlaceholder(v.X)
	case *ast.Ident:
		// A constant holding a query. Resolve it if it is declared in this
		// file, so schema constants and query constants are both covered.
		if v.Obj == nil {
			return false
		}
		spec, ok := v.Obj.Decl.(*ast.ValueSpec)
		if !ok || len(spec.Values) == 0 {
			return false
		}
		return containsPlaceholder(spec.Values[0])
	}
	return false
}
