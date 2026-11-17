package emit

import "testing"

// discriminatedUnionSchema has the union shapes of the released schemas that
// have a const: Outcome like RequestPermissionOutcome, Server like McpServer,
// whose stdio variant has no const, and Shape, whose const key is optional.
const discriminatedUnionSchema = `{"$defs": {
  "Stdio": {"type": "object", "required": ["name", "command"], "properties": {
    "name": {"type": "string"}, "command": {"type": "string"}}},
  "Outcome": {"discriminator": {"propertyName": "outcome"}, "oneOf": [
    {"type": "object", "required": ["outcome"], "properties": {"outcome": {"type": "string", "const": "cancelled"}}},
    {"type": "object", "required": ["outcome", "optionId"], "properties": {
      "outcome": {"type": "string", "const": "selected"}, "optionId": {"type": "string"}}}
  ]},
  "Server": {"anyOf": [
    {"type": "object", "required": ["type", "name", "url"], "properties": {
      "type": {"type": "string", "const": "http"}, "name": {"type": "string"}, "url": {"type": "string"}}},
    {"type": "object", "required": ["type", "name", "url"], "properties": {
      "type": {"type": "string", "const": "sse"}, "name": {"type": "string"}, "url": {"type": "string"}}},
    {"title": "stdio", "allOf": [{"$ref": "#/$defs/Stdio"}]}
  ]},
  "Shape": {"anyOf": [
    {"title": "circle", "type": "object", "required": ["radius"], "properties": {
      "kind": {"type": "string", "const": "circle"}, "radius": {"type": "number"}}},
    {"title": "square", "type": "object", "required": ["side"], "properties": {
      "kind": {"type": "string", "const": "square"}, "side": {"type": "number"}}}
  ]}
}}`

const discriminatedUnionTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestUnion_UnmarshalJSON_MatchesAVariantOnlyWithItsConstValue(t *testing.T) {
	tests := []struct {
		into func() any
		in   string
		want string
	}{
		{func() any { return new(Outcome) }, ` + "`" + `{"outcome":"cancelled"}` + "`" + `, ` + "`" + `{"outcome":"cancelled"}` + "`" + `},
		{func() any { return new(Outcome) }, ` + "`" + `{"outcome":"bogus"}` + "`" + `, ""},
		{func() any { return new(Outcome) }, ` + "`" + `{"outcome":"bogus","optionId":"o"}` + "`" + `, ""},
		{func() any { return new(Outcome) }, ` + "`" + `{"optionId":"o"}` + "`" + `, ""},
		{func() any { return new(Server) }, ` + "`" + `{"type":"sse","name":"n","url":"u"}` + "`" + `, ` + "`" + `{"name":"n","type":"sse","url":"u"}` + "`" + `},
		{func() any { return new(Server) }, ` + "`" + `{"type":"bogus","name":"n","url":"u"}` + "`" + `, ""},
		{func() any { return new(Server) }, ` + "`" + `{"name":"n","url":"u"}` + "`" + `, ""},
		{func() any { return new(Server) }, ` + "`" + `{"name":"n","command":"c"}` + "`" + `, ` + "`" + `{"command":"c","name":"n"}` + "`" + `},
		{func() any { return new(Server) }, ` + "`" + `{"type":"bogus","name":"n","command":"c"}` + "`" + `, ` + "`" + `{"command":"c","name":"n"}` + "`" + `},
		{func() any { return new(Shape) }, ` + "`" + `{"kind":"square","side":2}` + "`" + `, ` + "`" + `{"kind":"square","side":2}` + "`" + `},
		{func() any { return new(Shape) }, ` + "`" + `{"side":2}` + "`" + `, ` + "`" + `{"kind":"square","side":2}` + "`" + `},
		{func() any { return new(Shape) }, ` + "`" + `{"kind":"bogus","side":2}` + "`" + `, ""},
		{func() any { return new(Shape) }, ` + "`" + `{"kind":7,"side":2}` + "`" + `, ""},
	}
	for _, tt := range tests {
		v := tt.into()
		err := json.Unmarshal([]byte(tt.in), v)
		if tt.want == "" {
			if err == nil {
				t.Errorf("unmarshal %s gives %+v, want an error", tt.in, v)
			}
			continue
		}
		if err != nil {
			t.Errorf("unmarshal %s: %v", tt.in, err)
			continue
		}
		if out, err := json.Marshal(v); err != nil || string(out) != tt.want {
			t.Errorf("%s decodes and encodes to %s (error %v), want %s", tt.in, out, err, tt.want)
		}
	}
}
`

func TestWriteTypesJen_UnionMatchesAVariantOnlyWithItsConstValue(t *testing.T) {
	checkGenerated(t, discriminatedUnionSchema, map[string]string{"discriminated_test.go": discriminatedUnionTest})
}
