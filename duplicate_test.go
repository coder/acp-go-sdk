package acp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"
)

// TestRejectDuplicateMembers_EveryPropertyHasAField checks each call in the
// generated decoders: the receiver has a field that json tags with each
// property that the call names, or duplicateMemberError panics.
func TestRejectDuplicateMembers_EveryPropertyHasAField(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "types_gen.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]reflect.Type{}
	for _, typ := range append(append([]reflect.Type{}, fastTypes...), fastUnions...) {
		types[typ.Name()] = typ
	}
	calls := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "UnmarshalJSON" {
			continue
		}
		typ := types[fn.Recv.List[0].Type.(*ast.StarExpr).X.(*ast.Ident).Name]
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "rejectDuplicateMembers" {
				return true
			}
			calls++
			for _, arg := range call.Args[2:] {
				prop, _ := strconv.Unquote(arg.(*ast.BasicLit).Value)
				if err := duplicateMemberError(reflect.New(typ).Interface(), prop); err.Error() == "" {
					t.Errorf("%v: no error text for the property %s", typ, prop)
				}
			}
			return true
		})
	}
	if calls == 0 {
		t.Error("types_gen.go calls rejectDuplicateMembers nowhere")
	}
}
