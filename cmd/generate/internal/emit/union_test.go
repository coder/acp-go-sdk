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

func parseGo(t *testing.T, name, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}
