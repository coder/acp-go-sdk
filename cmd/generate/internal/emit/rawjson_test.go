package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// A value that the schema leaves free is raw JSON, so that a number keeps
// every digit. _meta is the SDK's Meta. A map takes the type of its
// additionalProperties schema.
func TestWriteTypesJen_FreeFormValuesAreRawJSON(t *testing.T) {
	src, _ := emitUnionFiles(t, `{"$defs": {
  "LooseMeta": {"type": "object", "properties": {
    "_meta": {"type": "object", "additionalProperties": {}}
  }},
  "Holder": {"type": "object", "properties": {
    "_meta": {"type": ["object", "null"], "additionalProperties": true, "x-deserialize-default-on-error": true},
    "rawInput": {"description": "Anything.", "x-deserialize-default-on-error": true},
    "data": {"description": "Anything, null kept."},
    "params": {"type": ["object", "null"], "additionalProperties": true},
    "open": {"type": "object"},
    "items": {"type": "array", "items": {}},
    "either": {"anyOf": [{"type": "string"}, {"type": "integer"}, {"type": "boolean"}]},
    "env": {"type": "object", "additionalProperties": {"type": "string"}},
    "loose": {"type": "object", "additionalProperties": {"description": "Anything."}},
    "frees": {"type": "object", "additionalProperties": {"$ref": "#/$defs/Free"}},
    "count": {"type": "integer", "format": "uint32"},
    "size": {"type": ["integer", "null"], "format": "int64"}
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
		case "LooseMeta":
			if typ := types.ExprString(spec.Type.(*ast.StructType).Fields.List[0].Type); typ != "Meta" {
				t.Errorf("a _meta whose additionalProperties is {} has type %q, want Meta", typ)
			}
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
		"Env":      "map[string]string",
		"Loose":    "map[string]json.RawMessage",
		"Frees":    "map[string]Free",
		"Count":    "int",
		"Size":     "*int64",
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

// A patch field whose schema is an inline object keeps the struct that the
// generator writes for it.
func TestWriteTypesJen_PatchOfAnInlineObjectUsesItsStruct(t *testing.T) {
	src, _ := emitUnionFiles(t, `{"$defs": {
  "Update": {"description": "Only changed fields need to be included.", "type": "object", "properties": {
    "owner": {"type": ["object", "null"], "properties": {"name": {"type": "string"}}}
  }}
}}`)
	if !strings.Contains(src, "Owner Patch[UpdateOwner]") {
		t.Errorf("types_gen.go does not declare Owner as Patch[UpdateOwner]:\n%s", src)
	}
}

// A patch field for which the schema defines no clear says how to read null.
func TestWriteTypesJen_NullAsAbsentPatchFieldSaysSo(t *testing.T) {
	src, _ := emitUnionFiles(t, `{"$defs": {
  "Update": {"description": "Only changed fields need to be included.", "type": "object", "properties": {
    "title": {"type": ["string", "null"], "description": "Update the title."},
    "name": {"type": ["string", "null"], "description": "Sending null leaves the name unchanged."}
  }}
}}`)
	const want = "// Update the title.\n\t//\n\t// ACP v1 does not say that null clears this field, and the Rust SDK\n\t// reads null here as absent: treat IsNull like IsUnset.\n\tTitle Patch[string]"
	if !strings.Contains(src, want) {
		t.Errorf("types_gen.go lacks the null note on Title:\n%s", src)
	}
	if strings.Count(src, "ACP v1 does not say") != 1 {
		t.Errorf("types_gen.go has the null note on a field whose description says what null does:\n%s", src)
	}
}
