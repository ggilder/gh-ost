package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

const doc = `ctxrecv reports blocking channel receives that cannot be woken by
migration-context cancellation.

A channel qualifies only if every send to it in non-test code goes through
base.SendWithContext, which abandons the send once the migration context is
cancelled. A receiver on such a channel that is not inside a select with an
escape arm can therefore park forever: the sole sender has already given up.

This is the defect fixed by github/gh-ost#1677 (rowCopyComplete) and
github/gh-ost#1758 (ghostTableMigrated).

Channels with any bare send, or that are closed, are exempt: those senders do
not abandon on cancellation, so the receive has an independent wakeup.`

var Analyzer = &analysis.Analyzer{
	Name:     "ctxrecv",
	Doc:      doc,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

type sendKinds struct {
	guarded bool // sent via base.SendWithContext
	bare    bool // plain ch <- v, or close(ch)
}

func isTestFile(fset *token.FileSet, n ast.Node) bool {
	return strings.HasSuffix(fset.Position(n.Pos()).Filename, "_test.go")
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// fieldObj resolves a channel expression to the struct field it names, or nil
// if the expression is not a field selector (a local variable, say).
func fieldObj(info *types.Info, e ast.Expr) *types.Var {
	sel, ok := unparen(e).(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	v, ok := info.ObjectOf(sel.Sel).(*types.Var)
	if !ok || !v.IsField() {
		return nil
	}
	if _, ok := v.Type().Underlying().(*types.Chan); !ok {
		return nil
	}
	return v
}

func isDoneOrTimerRecv(e ast.Expr) bool {
	u, ok := e.(*ast.UnaryExpr)
	if !ok || u.Op != token.ARROW {
		return false
	}
	switch x := unparen(u.X).(type) {
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if sel.Sel.Name == "Done" {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == "time" && (sel.Sel.Name == "After" || sel.Sel.Name == "Tick")
	case *ast.SelectorExpr:
		return x.Sel.Name == "C" // ticker.C / timer.C
	}
	return false
}

func commExpr(s ast.Stmt) ast.Expr {
	switch c := s.(type) {
	case *ast.ExprStmt:
		return c.X
	case *ast.AssignStmt:
		if len(c.Rhs) == 1 {
			return c.Rhs[0]
		}
	}
	return nil
}

// selectHasEscape reports whether the select can proceed without the channel
// operation completing.
func selectHasEscape(sel *ast.SelectStmt) bool {
	for _, stmt := range sel.Body.List {
		cc, ok := stmt.(*ast.CommClause)
		if !ok {
			continue
		}
		if cc.Comm == nil {
			return true // default:
		}
		if e := commExpr(cc.Comm); e != nil && isDoneOrTimerRecv(e) {
			return true
		}
	}
	return false
}

// inSelectHead reports whether the node is the communication clause of a
// select (as opposed to sitting in a case body, which is an ordinary
// blocking op), and returns the enclosing select.
func inSelectHead(stack []ast.Node) (*ast.SelectStmt, bool) {
	for i := len(stack) - 1; i >= 0; i-- {
		cc, ok := stack[i].(*ast.CommClause)
		if !ok {
			continue
		}
		if i+1 < len(stack) && cc.Comm != nil && ast.Node(cc.Comm) == stack[i+1] {
			// The CommClause's parent is the select's Body block, so the
			// SelectStmt sits further up than i-1.
			for j := i - 1; j >= 0; j-- {
				if sel, ok := stack[j].(*ast.SelectStmt); ok {
					return sel, true
				}
			}
		}
		return nil, false
	}
	return nil, false
}

func run(pass *analysis.Pass) (interface{}, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	info := pass.TypesInfo
	sends := map[*types.Var]*sendKinds{}

	kindFor := func(v *types.Var) *sendKinds {
		if sends[v] == nil {
			sends[v] = &sendKinds{}
		}
		return sends[v]
	}

	// Phase 1: classify every send to a field channel. Test files are excluded
	// so that a bare send in a test cannot exempt a production channel.
	insp.Preorder([]ast.Node{
		(*ast.SendStmt)(nil),
		(*ast.CallExpr)(nil),
	}, func(n ast.Node) {
		if isTestFile(pass.Fset, n) {
			return
		}
		switch x := n.(type) {
		case *ast.SendStmt:
			if v := fieldObj(info, x.Chan); v != nil {
				kindFor(v).bare = true
			}
		case *ast.CallExpr:
			fnName := ""
			switch f := x.Fun.(type) {
			case *ast.SelectorExpr:
				fnName = f.Sel.Name
			case *ast.Ident:
				fnName = f.Name
			}
			switch fnName {
			case "SendWithContext":
				if len(x.Args) >= 2 {
					if v := fieldObj(info, x.Args[1]); v != nil {
						kindFor(v).guarded = true
					}
				}
			case "close":
				// A closed channel wakes every receiver, so the receive has an
				// escape that does not depend on the sender.
				if len(x.Args) == 1 {
					if v := fieldObj(info, x.Args[0]); v != nil {
						kindFor(v).bare = true
					}
				}
			}
		}
	})

	// Phase 2: report blocking receives on channels whose only sender abandons
	// on cancellation.
	insp.WithStack([]ast.Node{(*ast.UnaryExpr)(nil)}, func(n ast.Node, push bool, stack []ast.Node) bool {
		if !push || isTestFile(pass.Fset, n) {
			return true
		}
		u := n.(*ast.UnaryExpr)
		if u.Op != token.ARROW {
			return true
		}
		v := fieldObj(info, u.X)
		if v == nil {
			return true
		}
		k := sends[v]
		if k == nil || !k.guarded || k.bare {
			return true
		}
		if sel, ok := inSelectHead(stack); ok && selectHasEscape(sel) {
			return true
		}
		pass.Reportf(n.Pos(),
			"blocking receive from %s cannot be woken by cancellation: every send to it uses base.SendWithContext, which abandons the send once the migration context is cancelled; select on migrationContext.GetContext().Done() alongside it",
			v.Name())
		return true
	})

	return nil, nil
}
