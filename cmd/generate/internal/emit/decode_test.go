package emit

import "testing"

const defaultedStructSchema = `{"$defs": {
  "Paging": {"type": "object", "properties": {"limit": {"type": "integer", "default": 10}, "offset": {"type": "integer"}}},
  "Query": {"type": "object", "required": ["id"], "properties": {
    "id": {"type": "string"},
    "limit": {"type": "integer", "default": 10},
    "paging": {"allOf": [{"$ref": "#/$defs/Paging"}], "default": {"limit": 10}},
    "note": {"type": ["string", "null"], "default": "none"},
    "options": {"type": "object", "properties": {"depth": {"type": "integer", "default": 3}}}
  }},
  "Shape": {"oneOf": [
    {"type": "object", "required": ["kind"], "properties": {
      "kind": {"type": "string", "const": "circle"}, "radius": {"type": "number", "default": 1}}},
    {"type": "object", "required": ["kind"], "properties": {
      "kind": {"type": "string", "const": "square"}, "side": {"type": "number", "default": 2}}}
  ]}
}}`

const defaultedStructTest = `package acp

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestQuery_UnmarshalJSON_TypeErrorNamesStructAndProperty(t *testing.T) {
	tests := []struct{ name, in, wantField string }{
		{"property without a default", ` + "`" + `{"id":5}` + "`" + `, "id"},
		{"property with a default", ` + "`" + `{"id":"q","limit":"x"}` + "`" + `, "limit"},
		{"property inside one with a default", ` + "`" + `{"id":"q","paging":{"limit":"x"}}` + "`" + `, "paging.limit"},
	}
	for _, tt := range tests {
		var typeErr *json.UnmarshalTypeError
		if err := json.Unmarshal([]byte(tt.in), new(Query)); !errors.As(err, &typeErr) {
			t.Errorf("%s: unmarshal %s: error = %v, want a *json.UnmarshalTypeError", tt.name, tt.in, err)
		} else if typeErr.Struct != "Query" || typeErr.Field != tt.wantField {
			t.Errorf("%s: unmarshal %s: error names %s.%s, want Query.%s", tt.name, tt.in, typeErr.Struct, typeErr.Field, tt.wantField)
		}
	}
}

func TestQuery_UnmarshalJSON_ReusedValueReadsAnAbsentPropertyAsItsDefault(t *testing.T) {
	var query Query
	for _, in := range []string{` + "`" + `{"id":"q","paging":{"limit":5,"offset":7}}` + "`" + `, ` + "`" + `{"id":"q"}` + "`" + `} {
		if err := json.Unmarshal([]byte(in), &query); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
	}
	if query.Paging.Limit != 10 || query.Paging.Offset != 0 {
		t.Errorf("paging of a reused query = %+v, want the default: limit 10 and no offset", query.Paging)
	}
}

func TestQuery_UnmarshalJSON_NullablePropertyTakesItsDefaultOnlyWhenAbsent(t *testing.T) {
	var absent, null Query
	for in, into := range map[string]*Query{` + "`" + `{"id":"q"}` + "`" + `: &absent, ` + "`" + `{"id":"q","note":null}` + "`" + `: &null} {
		if err := json.Unmarshal([]byte(in), into); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
	}
	if absent.Note == nil || *absent.Note != "none" {
		t.Errorf("note of a query without one = %v, want the default \"none\"", absent.Note)
	}
	if null.Note != nil {
		t.Errorf("note of a query with a null note = %q, want nil", *null.Note)
	}
}

func TestQuery_UnmarshalJSON_DefaultedPropertyMatchesItsKeyInAnyCase(t *testing.T) {
	var query Query
	if err := json.Unmarshal([]byte(` + "`" + `{"id":"q","Limit":5}` + "`" + `), &query); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if query.Limit != 5 {
		t.Errorf("limit sent as \"Limit\" = %d, want 5", query.Limit)
	}
}

func TestUnmarshalJSON_InlineStructsApplyTheirDefaults(t *testing.T) {
	var query Query
	if err := json.Unmarshal([]byte(` + "`" + `{"id":"q","options":{}}` + "`" + `), &query); err != nil {
		t.Fatalf("unmarshal a query: %v", err)
	}
	if query.Options.Depth != 3 {
		t.Errorf("depth of an inline object property = %d, want the default 3", query.Options.Depth)
	}
	var shape Shape
	if err := json.Unmarshal([]byte(` + "`" + `{"kind":"circle"}` + "`" + `), &shape); err != nil {
		t.Fatalf("unmarshal a shape: %v", err)
	}
	if shape.Circle == nil || shape.Circle.Radius != 1 {
		t.Errorf("circle = %+v, want an inline variant with the default radius 1", shape.Circle)
	}
}
`

func TestWriteTypesJen_StructDecodesDefaultsAndNamesTypeErrors(t *testing.T) {
	checkGenerated(t, defaultedStructSchema, map[string]string{"decode_test.go": defaultedStructTest})
}
