package emit

import "testing"

// zeroUnionSchema has one union for each way that emitUnion writes
// MarshalJSON: a oneOf of objects, an anyOf of scalars with a null variant,
// an open anyOf of $ref variants, and a union with an own property.
const zeroUnionSchema = `{"$defs": {
  "Named": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
  "Shape": {"discriminator": {"propertyName": "kind"}, "oneOf": [
    {"type": "object", "required": ["kind", "radius"], "properties": {
      "kind": {"type": "string", "const": "circle"}, "radius": {"type": "number"}}},
    {"type": "object", "required": ["kind", "side"], "properties": {
      "kind": {"type": "string", "const": "square"}, "side": {"type": "number"}}}]},
  "Id": {"anyOf": [
    {"title": "Null", "type": "null"},
    {"title": "Number", "type": "integer", "format": "int64"},
    {"title": "Str", "type": "string"}]},
  "Scope": {"anyOf": [{"$ref": "#/$defs/Named"}], "unevaluatedProperties": true},
  "Label": {"properties": {"note": {"type": "string"}}, "anyOf": [
    {"title": "Text", "type": "string"},
    {"title": "Named", "allOf": [{"$ref": "#/$defs/Named"}]}]}
}}`

// A union with no variant set has no JSON form. MarshalJSON and Validate
// both reject it, and the error names the union.
const zeroUnionTest = `package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUnions_ZeroValueFailsToMarshalWithTheUnionName(t *testing.T) {
	for _, tt := range []struct {
		name     string
		zero     interface{ Validate() error }
		set      any
		wantJSON string
	}{
		{"Shape", &Shape{}, Shape{Circle: &ShapeCircle{Radius: 1}}, ` + "`" + `{"kind":"circle","radius":1}` + "`" + `},
		{"Id", &Id{}, Id{Null: &IdNull{}}, "null"},
		{"Scope", &Scope{}, Scope{Named: &Named{Name: "n"}}, ` + "`" + `{"name":"n"}` + "`" + `},
		{"Label", &Label{}, Label{Text: new(LabelText)}, ` + "`" + `""` + "`" + `},
	} {
		out, err := json.Marshal(tt.zero)
		if err == nil || !strings.Contains(err.Error(), tt.name+" has no variant set") {
			t.Errorf("json.Marshal(%s{}) = %q, %v; want the error %q", tt.name, out, err, tt.name+" has no variant set")
		}
		if err := tt.zero.Validate(); err == nil {
			t.Errorf("%s{}.Validate() = nil, want an error as from MarshalJSON", tt.name)
		}
		if out, err := json.Marshal(tt.set); err != nil || string(out) != tt.wantJSON {
			t.Errorf("json.Marshal of a set %s = %s, %v; want %s", tt.name, out, err, tt.wantJSON)
		}
	}
}
`

func TestWriteTypesJen_ZeroValueUnionFailsToMarshal(t *testing.T) {
	checkGenerated(t, zeroUnionSchema, map[string]string{"zero_test.go": zeroUnionTest})
}
