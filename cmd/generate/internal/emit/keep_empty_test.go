package emit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// keepEmptySchema has optional lists and maps that allow null, as Rust's
// Option<Vec<T>> and Option<BTreeMap<..>> write them, next to optional ones
// that do not, as Rust's Vec with skip_serializing_if = "Vec::is_empty"
// writes them. Filter has such a list in an inline variant.
const keepEmptySchema = `{"$defs": {
  "Kind": {"type": "string", "enum": ["read", "edit"]},
  "Session": {"type": "object", "required": ["id"], "properties": {
    "id": {"type": "string"},
    "tags": {"type": ["array", "null"], "items": {"type": "string"}},
    "labels": {"type": ["object", "null"], "additionalProperties": {"type": "string"}},
    "kinds": {"type": ["array", "null"], "items": {"$ref": "#/$defs/Kind"},
      "x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true},
    "names": {"type": "array", "items": {"type": "string"}},
    "env": {"type": "object", "additionalProperties": {"type": "string"}}
  }},
  "Filter": {"discriminator": {"propertyName": "kind"}, "oneOf": [
    {"type": "object", "required": ["kind"], "properties": {
      "kind": {"type": "string", "const": "tags"}, "tags": {"type": ["array", "null"], "items": {"type": "string"}}}},
    {"type": "object", "required": ["kind"], "properties": {"kind": {"type": "string", "const": "none"}}}
  ]}
}}`

const keepEmptyTest = `package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("output %s is not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want %s is not JSON: %v", want, err)
	}
	return reflect.DeepEqual(g, w)
}

func TestSession_MarshalJSON_KeepsAnEmptyValueOnlyWhereNullIsAllowed(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"empty values", Session{Id: "s", Tags: []string{}, Labels: map[string]string{}, Kinds: []Kind{}, Names: []string{}, Env: map[string]string{}},
			` + "`" + `{"id":"s","tags":[],"labels":{},"kinds":[]}` + "`" + `},
		{"nil values", Session{Id: "s"}, ` + "`" + `{"id":"s"}` + "`" + `},
	}
	for _, tt := range tests {
		got, err := json.Marshal(tt.value)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tt.name, err)
		}
		if !sameJSON(t, got, tt.want) {
			t.Errorf("%s: encoded as %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestSession_RoundTrip_KeepsAPresentEmptyValue(t *testing.T) {
	tests := []struct {
		name, in, want string
		value          func() any
	}{
		{"empty values", ` + "`" + `{"id":"s","tags":[],"labels":{},"kinds":[],"names":[],"env":{}}` + "`" + `,
			` + "`" + `{"id":"s","tags":[],"labels":{},"kinds":[]}` + "`" + `, func() any { return new(Session) }},
		{"null reads as absent", ` + "`" + `{"id":"s","tags":null,"labels":null,"kinds":null}` + "`" + `,
			` + "`" + `{"id":"s"}` + "`" + `, func() any { return new(Session) }},
		{"a list whose items are all dropped stays empty", ` + "`" + `{"id":"s","kinds":["delete",5]}` + "`" + `,
			` + "`" + `{"id":"s","kinds":[]}` + "`" + `, func() any { return new(Session) }},
		{"a value of the wrong type reads as absent", ` + "`" + `{"id":"s","kinds":"x"}` + "`" + `,
			` + "`" + `{"id":"s"}` + "`" + `, func() any { return new(Session) }},
		{"an inline variant keeps an empty list", ` + "`" + `{"kind":"tags","tags":[]}` + "`" + `,
			` + "`" + `{"kind":"tags","tags":[]}` + "`" + `, func() any { return new(Filter) }},
		{"an inline variant leaves null out", ` + "`" + `{"kind":"tags","tags":null}` + "`" + `,
			` + "`" + `{"kind":"tags"}` + "`" + `, func() any { return new(Filter) }},
	}
	for _, tt := range tests {
		v := tt.value()
		if err := json.Unmarshal([]byte(tt.in), v); err != nil {
			t.Errorf("%s: unmarshal %s: %v", tt.name, tt.in, err)
			continue
		}
		got, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tt.name, err)
		}
		if !sameJSON(t, got, tt.want) {
			t.Errorf("%s: %s encoded as %s, want %s", tt.name, tt.in, got, tt.want)
		}
	}
}
`

func TestWriteTypesJen_OptionalNullableListOrMapKeepsAnEmptyValue(t *testing.T) {
	checkGenerated(t, keepEmptySchema, map[string]string{"keep_empty_test.go": keepEmptyTest})
}

// The MarshalJSON of a union encodes the union's own properties as they are
// declared, so it would leave out an empty list that has to stay.
func TestWriteTypesJen_PanicsWhenAUnionHasAnOwnListThatKeepsAnEmptyValue(t *testing.T) {
	var schema load.Schema
	if err := json.Unmarshal([]byte(`{"$defs": {
  "SessionScope": {"type": "object", "required": ["sessionId"], "properties": {"sessionId": {"type": "string"}}},
  "RequestScope": {"type": "object", "required": ["requestId"], "properties": {"requestId": {"type": "string"}}},
  "Mode": {"type": "object", "properties": {"tags": {"type": ["array", "null"], "items": {"type": "string"}}},
    "anyOf": [
      {"title": "Session", "allOf": [{"$ref": "#/$defs/SessionScope"}]},
      {"title": "Request", "allOf": [{"$ref": "#/$defs/RequestScope"}]}]}
}}`), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), `"Mode"`) {
			t.Fatalf("recover() = %v, want a panic that names Mode", r)
		}
	}()
	_ = WriteTypesJen(t.TempDir(), &schema, &load.Meta{})
}
