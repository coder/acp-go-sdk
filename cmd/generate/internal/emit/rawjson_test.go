package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// A value that the schema leaves free is raw JSON, so that a number keeps
// every digit. _meta is the SDK's Meta.
func TestWriteTypesJen_FreeFormValuesAreRawJSON(t *testing.T) {
	src, _ := emitUnionFiles(t, `{"$defs": {
  "Holder": {"type": "object", "properties": {
    "_meta": {"type": ["object", "null"], "additionalProperties": true, "x-deserialize-default-on-error": true},
    "rawInput": {"description": "Anything.", "x-deserialize-default-on-error": true},
    "data": {"description": "Anything, null kept."},
    "params": {"type": ["object", "null"], "additionalProperties": true},
    "open": {"type": "object"},
    "items": {"type": "array", "items": {}},
    "either": {"anyOf": [{"type": "string"}, {"type": "integer"}, {"type": "boolean"}]},
    "env": {"type": "object", "additionalProperties": {"type": "string"}},
    "loose": {"type": "object", "additionalProperties": {"description": "Anything."}}
  }},
  "Free": {"description": "A payload of any kind."}
}}`)
	file, err := parser.ParseFile(token.NewFileSet(), "types_gen.go", src, 0)
	if err != nil {
		t.Fatalf("parse the generated code: %v", err)
	}
	fields := map[string]string{}
	var free *ast.TypeSpec
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		spec := gen.Specs[0].(*ast.TypeSpec)
		switch spec.Name.Name {
		case "Free":
			free = spec
		case "Holder":
			for _, f := range spec.Type.(*ast.StructType).Fields.List {
				fields[f.Names[0].Name] = types.ExprString(f.Type)
			}
		}
	}
	want := map[string]string{
		"Meta":     "Meta",
		"RawInput": "json.RawMessage",
		"Data":     "json.RawMessage",
		"Params":   "map[string]json.RawMessage",
		"Open":     "map[string]json.RawMessage",
		"Items":    "[]json.RawMessage",
		"Either":   "json.RawMessage",
		"Env":      "map[string]any",
		"Loose":    "map[string]json.RawMessage",
	}
	for name, typ := range want {
		if fields[name] != typ {
			t.Errorf("field %s has type %q, want %q", name, fields[name], typ)
		}
	}
	if free == nil || !free.Assign.IsValid() || types.ExprString(free.Type) != "json.RawMessage" {
		t.Errorf("Free is not an alias of json.RawMessage: %#v", free)
	}
}
