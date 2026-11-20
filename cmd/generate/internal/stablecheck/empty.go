package stablecheck

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

// emptyAdded rewrites each return of one call in b when the call passes a
// field that the merged type adds and the function it calls returns at once
// for an empty value. A value that a stable caller builds leaves the added
// fields at their zero value, so for it the call returns what that first
// statement does: in the MarshalJSON of a struct that gains
// additionalProperties, return withAdditionalProperties(named,
// v.AdditionalProperties) is return named, nil, since
// withAdditionalProperties starts with if len(additional) == 0 { return
// named, nil }.
func (p *projection) emptyAdded(b *ast.BlockStmt) {
	ast.Inspect(b, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
			if call, ok := ret.Results[0].(*ast.CallExpr); ok {
				if results := p.emptyResults(call); results != nil {
					ret.Results = results
				}
			}
		}
		return true
	})
}

// emptyResults returns the results of call when its added fields are empty,
// or nil when the first statement of the function of the generated files
// that it calls does not show them: an if statement whose condition is
// len(x) == 0, with x a parameter that gets an added field whose zero value
// has length 0, and whose body returns parameters and nil.
func (p *projection) emptyResults(call *ast.CallExpr) []ast.Expr {
	fn := ident(call.Fun)
	if fn == nil {
		return nil
	}
	decl := p.funcs[fn.Name]
	if decl == nil || decl.Recv != nil || p.info.Uses[fn] != p.info.Defs[decl.Name] || call.Ellipsis.IsValid() || len(decl.Body.List) == 0 {
		return nil
	}
	var params []types.Object
	for _, field := range decl.Type.Params.List {
		for _, name := range field.Names {
			params = append(params, p.info.Defs[name])
		}
	}
	guard, ok := decl.Body.List[0].(*ast.IfStmt)
	if !ok || len(params) != len(call.Args) || guard.Init != nil || len(guard.Body.List) != 1 {
		return nil
	}
	cond, ok := guard.Cond.(*ast.BinaryExpr)
	if !ok || cond.Op != token.EQL || !isBuiltin(p.info, cond.X, "len") {
		return nil
	}
	if zero, ok := cond.Y.(*ast.BasicLit); !ok || zero.Value != "0" {
		return nil
	}
	if i := slices.Index(params, p.info.Uses[ident(cond.X.(*ast.CallExpr).Args[0])]); i < 0 || !p.emptyField(call.Args[i]) {
		return nil
	}
	ret, ok := guard.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) == 0 {
		return nil
	}
	var out []ast.Expr
	for _, r := range ret.Results {
		obj := p.info.Uses[ident(r)]
		switch i := slices.Index(params, obj); {
		case obj == nil:
			return nil
		case i >= 0:
			out = append(out, call.Args[i])
		case obj == types.Universe.Lookup("nil"):
			out = append(out, r)
		default:
			return nil
		}
	}
	return out
}

// emptyField reports whether e is a field that only the merged type has, of
// a type whose zero value has length 0.
func (p *projection) emptyField(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	field, ok := p.info.Uses[sel.Sel].(*types.Var)
	if added, stable := p.names(sel.Sel); !ok || !field.IsField() || !added || stable {
		return false
	}
	switch u := field.Type().Underlying().(type) {
	case *types.Map, *types.Slice, *types.Chan:
		return true
	case *types.Basic:
		return u.Info()&types.IsString != 0
	}
	return false
}

// foldMarshal rewrites, in each block of body, the last three statements
//
//	x, err := json.Marshal(e)
//	if err != nil { return nil, err }
//	return x, nil
//
// into return json.Marshal(e), which returns the same: json.Marshal returns
// nil beside an error.
func (s *side) foldMarshal(body *ast.BlockStmt) {
	ast.Inspect(body, func(n ast.Node) bool {
		b, ok := n.(*ast.BlockStmt)
		if !ok || len(b.List) < 3 {
			return true
		}
		k := len(b.List) - 3
		if call := s.marshalThenReturn(b.List[k:]); call != nil {
			b.List = append(b.List[:k], &ast.ReturnStmt{Results: []ast.Expr{call}})
		}
		return true
	})
}

// marshalThenReturn returns the call of json.Marshal of stmts, the three
// statements that foldMarshal folds, or nil.
func (s *side) marshalThenReturn(stmts []ast.Stmt) *ast.CallExpr {
	assign, ok := stmts[0].(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
		return nil
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return nil
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	if f, ok := s.info.Uses[sel.Sel].(*types.Func); !ok || f.Pkg() == nil || f.Pkg().Path() != "encoding/json" || f.Name() != "Marshal" {
		return nil
	}
	x, err := s.info.Defs[ident(assign.Lhs[0])], s.info.Defs[ident(assign.Lhs[1])]
	guard, ok := stmts[1].(*ast.IfStmt)
	if !ok || x == nil || err == nil || guard.Init != nil || guard.Else != nil || len(guard.Body.List) != 1 {
		return nil
	}
	if cond, ok := guard.Cond.(*ast.BinaryExpr); !ok || cond.Op != token.NEQ || !s.is(cond.X, err) || !s.is(cond.Y, nil) {
		return nil
	}
	if !s.returns(guard.Body.List[0], nil, err) || !s.returns(stmts[2], x, nil) {
		return nil
	}
	return call
}

// returns reports whether stmt returns the objects want, with nil for the
// predeclared nil.
func (s *side) returns(stmt ast.Stmt, want ...types.Object) bool {
	ret, ok := stmt.(*ast.ReturnStmt)
	if !ok || len(ret.Results) != len(want) {
		return false
	}
	for i, r := range ret.Results {
		if !s.is(r, want[i]) {
			return false
		}
	}
	return true
}

// is reports whether e names obj, or the predeclared nil for a nil obj.
func (s *side) is(e ast.Expr, obj types.Object) bool {
	if obj == nil {
		obj = types.Universe.Lookup("nil")
	}
	used := s.info.Uses[ident(e)]
	return used != nil && used == obj
}

// ident returns e as an identifier, or nil.
func ident(e ast.Expr) *ast.Ident {
	id, _ := e.(*ast.Ident)
	return id
}

// isBuiltin reports whether e is a call of the builtin function name with
// one argument that is an identifier.
func isBuiltin(info *types.Info, e ast.Expr, name string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || ident(call.Args[0]) == nil {
		return false
	}
	return info.Uses[ident(call.Fun)] == types.Universe.Lookup(name)
}
