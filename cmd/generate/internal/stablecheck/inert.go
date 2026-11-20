package stablecheck

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

// pruneInert removes from body, at any depth, each statement that has no
// effect, until none is left: the code around a part that a projection
// removed can stay behind, such as the local that a new switch on a new
// discriminator reads. bodyChanges prunes both versions of a body, so that a
// statement without effect that both have counts in neither.
//
// A statement has no effect when it does not return, branch, defer, start a
// goroutine, send, select, loop, or hold a function literal or a type
// declaration; when it cannot panic: it does not dereference, assert a type,
// divide, slice, or index anything but a map; when it writes no field and
// names no package-level name of the acp package; when each call in it is a
// conversion to a basic type, len, cap, a function of strings or bytes, a
// json.Unmarshal into the address of a local, or a method that reads the
// object of a union (unionReader); and when each local that it
// declares, assigns, or passes to json.Unmarshal lives only inside it or is
// dead. A local is dead when no code reads it: it is only assigned with = or
// := and passed to json.Unmarshal. A case of a switch is not a statement
// here, since a value that it takes would go to the default case without it.
func (s *side) pruneInert(body *ast.BlockStmt) {
	for changed := true; changed; {
		changed = false
		dead, named := s.deadLocals(body)
		cases := map[*ast.BlockStmt]bool{}
		ast.Inspect(body, func(n ast.Node) bool {
			var list *[]ast.Stmt
			switch n := n.(type) {
			case *ast.SwitchStmt:
				cases[n.Body] = true
			case *ast.TypeSwitchStmt:
				cases[n.Body] = true
			case *ast.SelectStmt:
				cases[n.Body] = true
			case *ast.BlockStmt:
				if !cases[n] {
					list = &n.List
				}
			case *ast.CaseClause:
				list = &n.Body
			}
			if list != nil {
				before := len(*list)
				*list = slices.DeleteFunc(*list, func(stmt ast.Stmt) bool { return s.inert(stmt, dead, named) })
				changed = changed || len(*list) != before
			}
			return true
		})
	}
}

// deadLocals returns the locals that body declares and does not read, and
// the objects that body names.
func (s *side) deadLocals(body *ast.BlockStmt) (dead, named map[types.Object]bool) {
	writes := map[*ast.Ident]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.ASSIGN || n.Tok == token.DEFINE {
				for _, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						writes[id] = true
					}
				}
			}
		case *ast.CallExpr:
			if id := s.unmarshalTarget(n); id != nil {
				writes[id] = true
			}
		}
		return true
	})
	declared, read := map[types.Object]bool{}, map[types.Object]bool{}
	named = map[types.Object]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if v, ok := s.info.Defs[id].(*types.Var); ok && !v.IsField() {
				declared[v] = true
			}
			if obj := s.info.Uses[id]; obj != nil {
				named[obj] = true
				read[obj] = read[obj] || !writes[id]
			}
		}
		return true
	})
	dead = map[types.Object]bool{}
	for v := range declared {
		if !read[v] {
			dead[v] = true
		}
	}
	return dead, named
}

// unmarshalTarget returns x of a call json.Unmarshal(data, &x), or nil.
func (s *side) unmarshalTarget(call *ast.CallExpr) *ast.Ident {
	if f := s.callee(call); f == nil || f.Pkg().Path() != "encoding/json" || f.Name() != "Unmarshal" || len(call.Args) != 2 {
		return nil
	}
	if addr, ok := call.Args[1].(*ast.UnaryExpr); ok && addr.Op == token.AND {
		id, _ := addr.X.(*ast.Ident)
		return id
	}
	return nil
}

// callee returns the package-level function that call calls, or nil.
func (s *side) callee(call *ast.CallExpr) *types.Func {
	var id *ast.Ident
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		id = fun
	case *ast.SelectorExpr:
		id = fun.Sel
	}
	f, _ := s.info.Uses[id].(*types.Func)
	if f == nil || f.Pkg() == nil || f.Type().(*types.Signature).Recv() != nil {
		return nil
	}
	return f
}

// inert reports whether stmt has no effect once the locals in dead are gone
// (see pruneInert). A declaration of a dead local goes only once named, the
// objects that the body names, no longer holds it.
func (s *side) inert(stmt ast.Stmt, dead, named map[types.Object]bool) bool {
	// writable reports whether stmt may write the local that e names, or
	// declare it with decl set.
	writable := func(e ast.Expr, decl bool) bool {
		id, ok := e.(*ast.Ident)
		if !ok {
			return false
		}
		if id.Name == "_" {
			return true
		}
		obj := s.info.Defs[id]
		if obj == nil {
			obj = s.info.Uses[id]
		}
		v, ok := obj.(*types.Var)
		if !ok || v.IsField() || v.Parent() == nil || v.Parent() == s.pkg.Scope() {
			return false
		}
		inside := stmt.Pos() <= v.Parent().Pos() && v.Parent().End() <= stmt.End()
		return inside || dead[v] && !(decl && named[v])
	}
	ok := true
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ReturnStmt, *ast.BranchStmt, *ast.DeferStmt, *ast.GoStmt, *ast.SendStmt, *ast.LabeledStmt, *ast.FuncLit,
			*ast.SelectStmt, *ast.ForStmt, *ast.RangeStmt, *ast.StarExpr, *ast.TypeAssertExpr, *ast.SliceExpr:
			ok = false
		case *ast.IndexExpr:
			_, isMap := s.info.TypeOf(n.X).Underlying().(*types.Map)
			ok = ok && isMap
		case *ast.BinaryExpr:
			ok = ok && n.Op != token.QUO && n.Op != token.REM
		case *ast.GenDecl:
			if n.Tok != token.VAR {
				ok = false
			}
		case *ast.ValueSpec:
			for _, name := range n.Names {
				ok = ok && writable(name, true)
			}
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				ok = ok && writable(lhs, n.Tok == token.DEFINE)
			}
		case *ast.IncDecStmt:
			ok = ok && writable(n.X, false)
		case *ast.UnaryExpr:
			// The address of a local, which only json.Unmarshal may take.
			ok = ok && n.Op != token.AND
		case *ast.CallExpr:
			if target := s.unmarshalTarget(n); target != nil {
				ok = ok && writable(target, false) && s.inert(&ast.ExprStmt{X: n.Args[0]}, dead, named)
				return false
			}
			ok = ok && s.pureCall(n)
		case *ast.Ident:
			switch obj := s.info.Uses[n].(type) {
			case nil, *types.PkgName, *types.Builtin, *types.Nil:
			case *types.Func:
				ok = ok && (obj.Pkg() != s.pkg || s.unionReader(obj))
			case *types.Var:
				ok = ok && obj.Parent() != s.pkg.Scope()
			default:
				ok = ok && obj.Pkg() != s.pkg
			}
		}
		return ok
	})
	return ok
}

// pureCall reports whether call is a conversion to a basic type, len, cap,
// a call of a function of strings or bytes, or of a unionReader, which change
// nothing.
func (s *side) pureCall(call *ast.CallExpr) bool {
	if tv, ok := s.info.Types[call.Fun]; ok && tv.IsType() {
		_, basic := tv.Type.Underlying().(*types.Basic)
		return basic
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if f, ok := s.info.Uses[sel.Sel].(*types.Func); ok && s.unionReader(f) {
			return true
		}
	}
	if id, ok := call.Fun.(*ast.Ident); ok {
		if b, ok := s.info.Uses[id].(*types.Builtin); ok {
			return b.Name() == "len" || b.Name() == "cap"
		}
	}
	f := s.callee(call)
	return f != nil && (f.Pkg().Path() == "strings" || f.Pkg().Path() == "bytes")
}

// unionReader reports whether f is get or tag of the type unionObject of the
// acp package, with which a union reads the members of its object
// (union.go). Neither changes anything.
func (s *side) unionReader(f *types.Func) bool {
	recv := f.Type().(*types.Signature).Recv()
	if f.Pkg() != s.pkg || recv == nil || (f.Name() != "get" && f.Name() != "tag") {
		return false
	}
	t := recv.Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	return ok && named.Obj().Name() == "unionObject"
}
