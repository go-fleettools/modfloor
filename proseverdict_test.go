package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// ⛔⛔ THIS TEST DERIVES ITS POPULATION INSTEAD OF LISTING IT, and that is the
// whole point of it.
//
// `TestProseIsNotAVerdict` next door asserts the BEHAVIOUR of the two readers
// it knows about. It is a sample. When #17's fix went in, five rules were
// discriminating on the text of `Row.Note` — the exposure split, the floor
// that gets judged, `byDirective`, `reposOf`, and the unreleased count — and I
// found all five with `grep`, which is a hand-written list of doors. A
// hand-written list of doors is the defect recorded in
// `a-mechanism-wired-at-only-some-of-its-doors`, whose standing lesson is that
// such a list had ALREADY drifted by the time it was written down: the sixth
// door is the one nobody greps for.
//
// So this walks the package's own syntax tree and fails on any comparison of
// `.Note` against a string, wherever it appears and whenever it is added. A
// new rule keyed on prose cannot be introduced without this going red, which
// is a ratchet rather than a sample.
//
// `== ""` is allowed: "did an earlier step already leave a note" is a question
// about freshly written local state, not a verdict about a module. Judging a
// module is what the boolean fields on Row are for.
func TestNoVerdictIsTakenFromTheTextOfANote(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// ⛔ A WALK THAT READ NOTHING MUST NOT PASS. Zero findings over zero files
	// is the best possible value in this column, and it is also what a moved
	// package or a changed filter produces.
	files := 0
	for _, p := range pkgs {
		files += len(p.Files)
	}
	if files < 5 {
		t.Fatalf("parsed only %d source file(s) of this package, so nothing was checked — "+
			"not the same as nothing being wrong", files)
	}

	var bad []string
	note := func(pos token.Pos, what string) {
		bad = append(bad, fmt.Sprintf("%s: %s", fset.Position(pos), what))
	}
	for _, p := range pkgs {
		for _, f := range p.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BinaryExpr:
					if x.Op != token.EQL && x.Op != token.NEQ {
						return true
					}
					if (isNote(x.X) && isProse(x.Y)) || (isNote(x.Y) && isProse(x.X)) {
						note(x.Pos(), "a note's TEXT decides a verdict here; use a field on Row")
					}
				case *ast.CallExpr:
					// strings.Contains / HasPrefix / HasSuffix / EqualFold on a
					// note is the same defect spelled as a call.
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok || len(x.Args) == 0 || !isNote(x.Args[0]) {
						return true
					}
					pkg, isIdent := sel.X.(*ast.Ident)
					if !isIdent || pkg.Name != "strings" {
						return true
					}
					note(x.Pos(), "strings."+sel.Sel.Name+" on a note decides a verdict here; "+
						"use a field on Row")
				}
				return true
			})
		}
	}
	if len(bad) > 0 {
		t.Errorf("a classification is being read out of prose again — the thing #17's fix "+
			"existed to stop, and a reworded note silently flips every one of these:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

func isNote(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == "Note"
}

// isProse reports whether an expression is a string literal with something in
// it. The empty string is not prose: it asks whether a note exists at all.
func isProse(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && lit.Value != `""` && lit.Value != "``"
}
