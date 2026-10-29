package emit

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// emitUnionFiles runs the types and helpers emitters for schemaJSON into a
// fresh directory and returns the two generated sources.
func emitUnionFiles(t *testing.T, schemaJSON string) (types, helpers string) {
	t.Helper()
	var schema load.Schema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	dir := t.TempDir()
	if err := WriteTypesJen(dir, &schema, &load.Meta{}); err != nil {
		t.Fatalf("WriteTypesJen: %v", err)
	}
	if err := WriteHelpersJen(dir, &schema, &load.Meta{}); err != nil {
		t.Fatalf("WriteHelpersJen: %v", err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	return read("types_gen.go"), read("helpers_gen.go")
}

// Each variant has two const properties and the union has no explicit
// discriminator, so the emitters must pick the discriminator key and order
// the const injections without depending on Go map iteration order.
const multiConstUnionSchema = `{"$defs": {
  "Shape": {"oneOf": [
    {"type": "object", "required": ["kind", "shape", "radius"], "properties": {
      "kind": {"const": "round"}, "shape": {"const": "circle"}, "radius": {"type": "number"}}},
    {"type": "object", "required": ["kind", "shape", "side"], "properties": {
      "kind": {"const": "angular"}, "shape": {"const": "square"}, "side": {"type": "number"}}}
  ]}
}}`

func TestWriteTypesJen_MultiConstUnionIsDeterministic(t *testing.T) {
	wantTypes, wantHelpers := emitUnionFiles(t, multiConstUnionSchema)
	// Go randomizes map iteration order, so 20 runs of a map-order-dependent
	// emitter differ with probability 1 - 2^-19.
	for i := 0; i < 20; i++ {
		gotTypes, gotHelpers := emitUnionFiles(t, multiConstUnionSchema)
		if gotTypes != wantTypes {
			t.Fatalf("run %d: types_gen.go differs between runs", i)
		}
		if gotHelpers != wantHelpers {
			t.Fatalf("run %d: helpers_gen.go differs between runs", i)
		}
	}
}

// The second variant has no $ref, no properties, no null type, and no title,
// so emitUnion skips it. WriteHelpersJen must skip it too.
const phantomVariantSchema = `{"$defs": {
  "Named": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
  "Pick": {"oneOf": [
    {"$ref": "#/$defs/Named"},
    {"description": "phantom variant"}
  ]}
}}`

func TestWriteHelpersJen_SkipsPhantomVariant(t *testing.T) {
	types, helpers := emitUnionFiles(t, phantomVariantSchema)

	declared := map[string]bool{}
	for _, d := range parseGo(t, "types_gen.go", types).Decls {
		if gd, ok := d.(*ast.GenDecl); ok {
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					declared[ts.Name.Name] = true
				}
			}
		}
	}
	if !declared["Pick"] || !declared["Named"] {
		t.Fatalf("types_gen.go lacks Pick or Named; declared: %v", declared)
	}

	constructors := 0
	ast.Inspect(parseGo(t, "helpers_gen.go", helpers), func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncDecl); ok {
			constructors++
		}
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if id, ok := lit.Type.(*ast.Ident); ok && !declared[id.Name] {
			t.Errorf("helpers_gen.go builds undeclared type %s", id.Name)
		}
		return true
	})
	if constructors != 1 {
		t.Errorf("helpers_gen.go has %d constructors, want 1 (for the Named variant)", constructors)
	}
}

// PickNotification sorts before PickRequest, so WriteTypesJen registers the
// nested type PickItem (for its inline "item" property) before it names the
// PickRequest variants. The "item" variant then cannot take PickItem and gets
// another name. WriteHelpersJen must build that same type.
const nestedNameCollisionSchema = `{"$defs": {
  "PickNotification": {"type": "object", "properties": {
    "item": {"type": "object", "properties": {"note": {"type": "string"}}}}},
  "PickRequest": {"oneOf": [
    {"title": "item", "type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
    {"title": "other", "type": "object", "required": ["count"], "properties": {"count": {"type": "integer"}}}
  ]}
}}`

func TestWriteHelpersJen_UsesUnionVariantTypes(t *testing.T) {
	types, helpers := emitUnionFiles(t, nestedNameCollisionSchema)

	// union struct name -> field name -> type that the pointer field points to
	fields := map[string]map[string]string{}
	for _, d := range parseGo(t, "types_gen.go", types).Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, fl := range st.Fields.List {
				star, ok := fl.Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				elem, ok := star.X.(*ast.Ident)
				if !ok {
					continue
				}
				for _, n := range fl.Names {
					if fields[ts.Name.Name] == nil {
						fields[ts.Name.Name] = map[string]string{}
					}
					fields[ts.Name.Name][n.Name] = elem.Name
				}
			}
		}
	}
	if len(fields["PickRequest"]) != 2 {
		t.Fatalf("PickRequest pointer fields = %v, want 2", fields["PickRequest"])
	}

	checked := 0
	ast.Inspect(parseGo(t, "helpers_gen.go", helpers), func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		union, ok := lit.Type.(*ast.Ident)
		if !ok || fields[union.Name] == nil {
			return true
		}
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, _ := kv.Key.(*ast.Ident)
			addr, _ := kv.Value.(*ast.UnaryExpr)
			if key == nil || addr == nil {
				continue
			}
			inner, _ := addr.X.(*ast.CompositeLit)
			if inner == nil {
				continue
			}
			got, _ := inner.Type.(*ast.Ident)
			want := fields[union.Name][key.Name]
			if got == nil || got.Name != want {
				t.Errorf("helpers_gen.go sets %s.%s to &%v{...}, but the field has type *%s", union.Name, key.Name, got, want)
			}
			checked++
		}
		return true
	})
	if checked != 2 {
		t.Errorf("checked %d union constructor fields, want 2", checked)
	}
}

func parseGo(t *testing.T, name, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}
