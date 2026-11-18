package emit

import (
	"encoding/json"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/ir"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// shapedSchema has hinted properties whose values decode into their Go types
// but break a rule of the schema inside: a required member, a closed enum, a
// member that must not be null, a union variant, and a map value.
const shapedSchema = `{"$defs": {
  "Priority": {"type": "string", "enum": ["high", "low"]},
  "Kind": {"oneOf": [{"type": "string", "const": "read"}, {"type": "string", "const": "other"}]},
  "Location": {"type": "object", "required": ["path"], "properties": {"path": {"type": "string"}}},
  "Entry": {"type": "object", "required": ["content", "priority"], "properties": {
    "content": {"type": "string"},
    "priority": {"$ref": "#/$defs/Priority"},
    "kind": {"anyOf": [{"$ref": "#/$defs/Kind"}, {"type": "null"}]},
    "location": {"anyOf": [{"$ref": "#/$defs/Location"}, {"type": "null"}]},
    "note": {"type": ["string", "null"], "x-deserialize-default-on-error": true},
    "lines": {"type": "array", "items": {"type": "integer"}, "default": [], "x-deserialize-default-on-null": true},
    "env": {"type": "object", "additionalProperties": {"type": "string"}}}},
  "Content": {"discriminator": {"propertyName": "type"}, "oneOf": [
    {"type": "object", "required": ["type", "path"], "properties": {
      "type": {"type": "string", "const": "diff"}, "path": {"type": "string"}}},
    {"type": "object", "required": ["type"], "properties": {"type": {"type": "string"}},
      "not": {"anyOf": [{"type": "object", "required": ["type"], "properties": {"type": {"type": "string", "const": "diff"}}}]},
      "additionalProperties": true}]},
  "Options": {"type": "object", "properties": {"flag": {"type": "boolean"}}},
  "A": {"type": "object", "properties": {
    "bC": {"type": "array", "items": {"type": "string"}, "x-deserialize-default-on-error": true}}},
  "AB": {"type": "object", "properties": {
    "c": {"type": "array", "items": {"type": "integer"}, "x-deserialize-default-on-error": true}}},
  "Composite": {"type": "object", "required": ["k"], "properties": {"k": {"const": {"a": 1}}}},
  "Mapped": {"type": "object", "properties": {
    "_meta": {"type": ["object", "null"], "x-deserialize-default-on-error": true}},
    "additionalProperties": {"type": "string"}},
  "Picked": {"type": "string", "enum": ["a"], "oneOf": [{"type": "string", "const": "a"}, {"type": "string", "const": "b"}]},
  "Checked": {"type": "object", "properties": {
    "composite": {"anyOf": [{"$ref": "#/$defs/Composite"}, {"type": "null"}], "x-deserialize-default-on-error": true},
    "mapped": {"anyOf": [{"$ref": "#/$defs/Mapped"}, {"type": "null"}], "x-deserialize-default-on-error": true},
    "picked": {"anyOf": [{"$ref": "#/$defs/Picked"}, {"type": "null"}], "x-deserialize-default-on-error": true}}},
  "Loose": {"type": "object", "properties": {
    "either": {"anyOf": [
      {"type": "object", "required": ["a"], "properties": {"a": {"type": "string"}}},
      {"type": "object", "required": ["b"], "properties": {"b": {"type": "string"}}}],
      "x-deserialize-default-on-error": true},
    "text": {"anyOf": [{"type": "string"}], "x-deserialize-default-on-error": true}}},
  "Holder": {"type": "object", "properties": {
    "options": {"anyOf": [{"$ref": "#/$defs/Options"}, {"type": "null"}], "x-deserialize-default-on-error": true},
    "entry": {"anyOf": [{"$ref": "#/$defs/Entry"}, {"type": "null"}], "x-deserialize-default-on-error": true},
    "entries": {"type": "array", "items": {"$ref": "#/$defs/Entry"}, "x-deserialize-skip-invalid-items": true},
    "content": {"type": ["array", "null"], "items": {"$ref": "#/$defs/Content"},
      "x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true},
    "_meta": {"type": ["object", "null"], "additionalProperties": true, "x-deserialize-default-on-error": true}}}
}}`

const shapedTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestHolder_UnmarshalJSON_ChecksHintedValuesAgainstTheirSchema(t *testing.T) {
	const good = ` + "`" + `{"content":"c","priority":"high"}` + "`" + `
	tests := []struct {
		name, in, want string
	}{
		{"a good entry is kept", ` + "`" + `{"entry":` + "`" + ` + good + ` + "`" + `}` + "`" + `,
			` + "`" + `{"entry":{"content":"c","priority":"high","lines":[]}}` + "`" + `},
		{"an entry without a required member reads as absent", ` + "`" + `{"entry":{"content":"c"}}` + "`" + `,
			` + "`" + `{}` + "`" + `},
		{"an entry with a null required member reads as absent", ` + "`" + `{"entry":{"content":null,"priority":"high"}}` + "`" + `,
			` + "`" + `{}` + "`" + `},
		{"an entry with an unknown enum value reads as absent", ` + "`" + `{"entry":{"content":"c","priority":"urgent"}}` + "`" + `,
			` + "`" + `{}` + "`" + `},
		{"an enum with the member other takes every string", ` + "`" + `{"entry":{"content":"c","priority":"high","kind":"teleport"}}` + "`" + `,
			` + "`" + `{"entry":{"content":"c","priority":"high","kind":"other","lines":[]}}` + "`" + `},
		{"a nested object without a required member breaks the entry", ` + "`" + `{"entry":{"content":"c","priority":"high","location":{}}}` + "`" + `,
			` + "`" + `{}` + "`" + `},
		{"a null optional member that the schema allows to be null is kept", ` + "`" + `{"entry":{"content":"c","priority":"high","location":null}}` + "`" + `,
			` + "`" + `{"entry":{"content":"c","priority":"high","lines":[]}}` + "`" + `},
		{"a member with its own hint keeps the entry", ` + "`" + `{"entry":{"content":"c","priority":"high","note":7}}` + "`" + `,
			` + "`" + `{"entry":{"content":"c","priority":"high","lines":[]}}` + "`" + `},
		{"x-deserialize-default-on-null inside takes null", ` + "`" + `{"entry":{"content":"c","priority":"high","lines":null}}` + "`" + `,
			` + "`" + `{"entry":{"content":"c","priority":"high","lines":[]}}` + "`" + `},
		{"a null item of an array inside breaks the entry", ` + "`" + `{"entry":{"content":"c","priority":"high","lines":[1,null]}}` + "`" + `,
			` + "`" + `{}` + "`" + `},
		{"a null map value breaks the entry", ` + "`" + `{"entry":{"content":"c","priority":"high","env":{"A":null}}}` + "`" + `,
			` + "`" + `{}` + "`" + `},
		{"null where an object without required members rejects it", ` + "`" + `{"options":{"flag":null}}` + "`" + `,
			` + "`" + `{}` + "`" + `},
		{"an object without required members is kept", ` + "`" + `{"options":{"flag":true}}` + "`" + `,
			` + "`" + `{"options":{"flag":true}}` + "`" + `},
		{"bad items are dropped", ` + "`" + `{"entries":[{"content":"c"},` + "`" + ` + good + ` + "`" + `,{"content":"c","priority":"urgent"}]}` + "`" + `,
			` + "`" + `{"entries":[{"content":"c","priority":"high","lines":[]}]}` + "`" + `},
		{"a variant without a required member is dropped", ` + "`" + `{"content":[{"type":"diff"},{"type":"diff","path":"/p"}]}` + "`" + `,
			` + "`" + `{"content":[{"type":"diff","path":"/p"}]}` + "`" + `},
		{"the catch-all variant takes an unknown type", ` + "`" + `{"content":[{"type":"_x","k":1}]}` + "`" + `,
			` + "`" + `{"content":[{"type":"_x","k":1}]}` + "`" + `},
	}
	for _, tt := range tests {
		var got Holder
		if err := json.Unmarshal([]byte(tt.in), &got); err != nil {
			t.Errorf("%s: unmarshal %s: %v", tt.name, tt.in, err)
			continue
		}
		out, err := json.Marshal(got)
		if err != nil {
			t.Errorf("%s: marshal: %v", tt.name, err)
			continue
		}
		var gotJSON, wantJSON any
		if err := json.Unmarshal(out, &gotJSON); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(tt.want), &wantJSON); err != nil {
			t.Fatalf("%s: want is not JSON: %v", tt.name, err)
		}
		if !jsonEqual(gotJSON, wantJSON) {
			t.Errorf("%s: %s decoded to %s, want %s", tt.name, tt.in, out, tt.want)
		}
	}
}

func TestShapes_EachPropertyHasItsOwnShape(t *testing.T) {
	var a A
	if err := json.Unmarshal([]byte(` + "`" + `{"bC":["x"]}` + "`" + `), &a); err != nil || len(a.BC) != 1 {
		t.Errorf("A.bC = %v, %v; want [x], nil", a.BC, err)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"bC":["x",null]}` + "`" + `), &a); err != nil || a.BC != nil {
		t.Errorf("A.bC with a null item = %v, %v; want nil, nil", a.BC, err)
	}
	var ab AB
	if err := json.Unmarshal([]byte(` + "`" + `{"c":[1]}` + "`" + `), &ab); err != nil || len(ab.C) != 1 {
		t.Errorf("AB.c = %v, %v; want [1], nil", ab.C, err)
	}
}

func TestLoose_UnmarshalJSON_ChecksPropertiesOfTypeAny(t *testing.T) {
	tests := []struct{ in, want string }{
		{` + "`" + `{"either":{"a":"x"}}` + "`" + `, ` + "`" + `{"either":{"a":"x"}}` + "`" + `},
		{` + "`" + `{"either":{"b":"x"}}` + "`" + `, ` + "`" + `{"either":{"b":"x"}}` + "`" + `},
		{` + "`" + `{"either":{"c":"x"}}` + "`" + `, ` + "`" + `{}` + "`" + `},
		{` + "`" + `{"text":"x"}` + "`" + `, ` + "`" + `{"text":"x"}` + "`" + `},
		{` + "`" + `{"text":5}` + "`" + `, ` + "`" + `{}` + "`" + `},
	}
	for _, tt := range tests {
		var got Loose
		if err := json.Unmarshal([]byte(tt.in), &got); err != nil {
			t.Errorf("unmarshal %s: %v", tt.in, err)
			continue
		}
		out, _ := json.Marshal(got)
		if string(out) != tt.want {
			t.Errorf("%s decoded to %s, want %s", tt.in, out, tt.want)
		}
	}
}

func TestChecked_UnmarshalJSON_FollowsEachRuleOfTheSchema(t *testing.T) {
	tests := []struct{ in, want string }{
		{` + "`" + `{"composite":{"k":{"a":1}}}` + "`" + `, ` + "`" + `{"composite":{"k":{"a":1}}}` + "`" + `},
		{` + "`" + `{"composite":{}}` + "`" + `, ` + "`" + `{}` + "`" + `},
		{` + "`" + `{"mapped":{"_meta":{"x":1},"a":"b"}}` + "`" + `, ` + "`" + `{"mapped":{"_meta":{"x":1}}}` + "`" + `},
		{` + "`" + `{"mapped":{"a":null}}` + "`" + `, ` + "`" + `{}` + "`" + `},
		{` + "`" + `{"picked":"a"}` + "`" + `, ` + "`" + `{"picked":"a"}` + "`" + `},
		{` + "`" + `{"picked":"b"}` + "`" + `, ` + "`" + `{}` + "`" + `},
	}
	for _, tt := range tests {
		var got Checked
		if err := json.Unmarshal([]byte(tt.in), &got); err != nil {
			t.Errorf("unmarshal %s: %v", tt.in, err)
			continue
		}
		out, _ := json.Marshal(got)
		if string(out) != tt.want {
			t.Errorf("%s decoded to %s, want %s", tt.in, out, tt.want)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
`

func TestWriteTypesJen_HintedValuesFitTheirSchema(t *testing.T) {
	checkGenerated(t, shapedSchema, map[string]string{"shapes_test.go": shapedTest})
}

// TestShapeTable_SiteSeesRulesBehindACycleOfRefs checks properties of types B
// and A, which refer to each other, where only A has a rule of its own: B
// must still name its shape.
func TestShapeTable_SiteSeesRulesBehindACycleOfRefs(t *testing.T) {
	var schema load.Schema
	if err := json.Unmarshal([]byte(`{"$defs": {
  "A": {"type": "object", "properties": {"b": {"$ref": "#/$defs/B"}, "kind": {"type": "string", "enum": ["x"]}}},
  "B": {"type": "object", "properties": {"a": {"anyOf": [{"$ref": "#/$defs/A"}, {"type": "null"}]}}}}}`), &schema); err != nil {
		t.Fatal(err)
	}
	table := newShapeTable(&schema)
	for _, def := range []string{"B", "A"} {
		field := ir.Field{Prop: "p", Name: "P", Def: &load.Definition{Ref: "#/$defs/" + def}, Lenient: ir.Lenient{DefaultOnError: true}}
		if table.site("Holder", field, fieldType(field)) == nil {
			t.Errorf("a property of type %s names no shape, want shape%s", def, def)
		}
	}
}
