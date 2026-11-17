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

// catchAllUnionSchema has the union shapes that the schema gains from 1.18.0
// on. Idle is IdleStateUpdate: its discriminator is a property of its own with
// x-deserialize-default-on-error, and it has a catch-all "other" variant and a
// "none" variant. Items is MultiSelectItems, whose catch-all comes before a
// variant without a const. Problem is McpError, a struct that keeps the
// members the schema does not name. Strict has a "none" variant and a
// discriminator without the hint. State is StateUpdate, whose idle variant
// holds Idle.
const catchAllUnionSchema = `{"$defs": {
  "Failure": {"type": "object", "properties": {
    "error": {"type": ["string", "null"], "x-deserialize-default-on-error": true}}},
  "Idle": {"type": "object",
    "properties": {
      "usage": {"type": ["integer", "null"], "x-deserialize-default-on-error": true},
      "stopReason": {"type": ["string", "null"], "x-deserialize-default-on-error": true}},
    "anyOf": [
      {"type": "object", "required": ["stopReason"], "properties": {"stopReason": {"type": "string", "const": "end_turn"}}},
      {"type": "object", "required": ["stopReason"], "properties": {"stopReason": {"type": "string", "const": "error"}},
        "allOf": [{"$ref": "#/$defs/Failure"}]},
      {"title": "other", "type": "object", "required": ["stopReason"], "properties": {"stopReason": {"type": "string"}},
        "additionalProperties": true},
      {"title": "none", "type": "object", "properties": {"stopReason": {"type": "null"}}}
    ]},
  "Titled": {"type": "object", "required": ["anyOf"], "properties": {
    "anyOf": {"type": "array", "items": {"type": "string"}}}},
  "Items": {"anyOf": [
    {"type": "object", "required": ["type"], "properties": {"type": {"type": "string", "const": "string"}}},
    {"title": "other", "type": "object", "required": ["type"], "properties": {"type": {"type": "string"}},
      "additionalProperties": true},
    {"title": "titled", "allOf": [{"$ref": "#/$defs/Titled"}]}
  ]},
  "Problem": {"type": "object", "required": ["code"], "properties": {"code": {"type": "integer"}},
    "additionalProperties": true},
  "Strict": {"anyOf": [
    {"type": "object", "required": ["stopReason"], "properties": {"stopReason": {"type": "string", "const": "done"}}},
    {"title": "none", "type": "object", "properties": {"stopReason": {"type": "null"}}}
  ]},
  "State": {"anyOf": [
    {"type": "object", "required": ["state"], "properties": {"state": {"type": "string", "const": "running"}}},
    {"type": "object", "required": ["state"], "properties": {"state": {"type": "string", "const": "idle"}},
      "allOf": [{"$ref": "#/$defs/Idle"}]},
    {"title": "other", "type": "object", "required": ["state"], "properties": {"state": {"type": "string"}},
      "additionalProperties": true}
  ]}
}}`

const catchAllUnionTest = `package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal([]byte(a), &va); err != nil {
		t.Fatalf("parse %s: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &vb); err != nil {
		t.Fatalf("parse %s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}

func TestCatchAllUnions_RoundTrip(t *testing.T) {
	tests := []struct {
		into func() any
		in   string
		// want is the encoding, or "" when it equals in.
		want string
	}{
		{func() any { return new(Idle) }, ` + "`" + `{"stopReason":"end_turn","usage":3}` + "`" + `, ""},
		{func() any { return new(Idle) }, ` + "`" + `{"stopReason":"error","error":"boom"}` + "`" + `, ""},
		{func() any { return new(Idle) }, ` + "`" + `{"stopReason":"_custom","usage":3,"error":"boom","detail":{"a":[1,2]}}` + "`" + `, ""},
		{func() any { return new(Idle) }, ` + "`" + `{"usage":3}` + "`" + `, ""},
		{func() any { return new(Idle) }, ` + "`" + `{"stopReason":null,"usage":3}` + "`" + `, ` + "`" + `{"usage":3}` + "`" + `},
		{func() any { return new(Idle) }, ` + "`" + `{"stopReason":12345,"usage":3}` + "`" + `, ` + "`" + `{"usage":3}` + "`" + `},
		{func() any { return new(State) }, ` + "`" + `{"state":"running"}` + "`" + `, ""},
		{func() any { return new(State) }, ` + "`" + `{"state":"idle","stopReason":"end_turn","usage":3}` + "`" + `, ""},
		{func() any { return new(State) }, ` + "`" + `{"state":"idle","stopReason":"_custom","usage":3,"detail":{"a":[1,2]}}` + "`" + `, ""},
		{func() any { return new(State) }, ` + "`" + `{"state":"idle","usage":3}` + "`" + `, ""},
		{func() any { return new(State) }, ` + "`" + `{"state":"idle","stopReason":null,"usage":3}` + "`" + `, ` + "`" + `{"state":"idle","usage":3}` + "`" + `},
		{func() any { return new(State) }, ` + "`" + `{"state":"idle","stopReason":12345,"usage":3}` + "`" + `, ` + "`" + `{"state":"idle","usage":3}` + "`" + `},
		{func() any { return new(State) }, ` + "`" + `{"state":"_later","detail":1}` + "`" + `, ""},
		{func() any { return new(Items) }, ` + "`" + `{"type":"string"}` + "`" + `, ""},
		{func() any { return new(Items) }, ` + "`" + `{"type":"future","limit":2}` + "`" + `, ""},
		{func() any { return new(Items) }, ` + "`" + `{"anyOf":["a","b"]}` + "`" + `, ""},
		{func() any { return new(Problem) }, ` + "`" + `{"code":1,"data":{"k":"v"},"retry":true}` + "`" + `, ""},
	}
	for _, tt := range tests {
		v := tt.into()
		if err := json.Unmarshal([]byte(tt.in), v); err != nil {
			t.Errorf("unmarshal %s: %v", tt.in, err)
			continue
		}
		out, err := json.Marshal(v)
		if err != nil {
			t.Errorf("marshal the value of %s: %v", tt.in, err)
			continue
		}
		want := tt.want
		if want == "" {
			want = tt.in
		}
		if !sameJSON(t, string(out), want) {
			t.Errorf("%s decodes and encodes to %s, want %s", tt.in, out, want)
		}
	}
}

func TestCatchAllUnions_SelectTheVariant(t *testing.T) {
	var custom, absent, null, malformed Idle
	for in, into := range map[string]*Idle{
		` + "`" + `{"stopReason":"_custom","detail":1}` + "`" + `: &custom, ` + "`" + `{}` + "`" + `: &absent,
		` + "`" + `{"stopReason":null}` + "`" + `: &null, ` + "`" + `{"stopReason":12345}` + "`" + `: &malformed,
	} {
		if err := json.Unmarshal([]byte(in), into); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
	}
	if custom.Other == nil || custom.Other.StopReason != "_custom" || custom.Other.AdditionalProperties["detail"] != float64(1) {
		t.Errorf("an unknown stop reason decodes to %+v, want the other variant with the reason and the detail member", custom.Other)
	}
	if absent.None == nil || null.None == nil || malformed.None == nil {
		t.Errorf("none variant of an absent, a null and a malformed stop reason = %v, %v, %v, want all set", absent.None, null.None, malformed.None)
	}
	var items Items
	for _, in := range []string{` + "`" + `{"type":null}` + "`" + `, ` + "`" + `{"type":5}` + "`" + `, ` + "`" + `{}` + "`" + `} {
		if err := json.Unmarshal([]byte(in), &items); err == nil {
			t.Errorf("unmarshal %s gives %+v, want an error", in, items)
		}
	}
	var strict Strict
	for _, in := range []string{` + "`" + `{}` + "`" + `, ` + "`" + `{"stopReason":null}` + "`" + `} {
		if err := json.Unmarshal([]byte(in), &strict); err != nil || strict.None == nil {
			t.Errorf("unmarshal %s gives %+v (error %v), want the none variant", in, strict, err)
		}
	}
	for _, in := range []string{` + "`" + `{"stopReason":5}` + "`" + `, ` + "`" + `{"stopReason":"later"}` + "`" + `} {
		if err := json.Unmarshal([]byte(in), &strict); err == nil {
			t.Errorf("unmarshal %s gives %+v, want an error", in, strict)
		}
	}
}

func TestProblem_AdditionalPropertiesDoNotReplaceANamedProperty(t *testing.T) {
	out, err := json.Marshal(Problem{Code: 1, AdditionalProperties: map[string]any{"code": 9, "retry": true}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := ` + "`" + `{"code":1,"retry":true}` + "`" + `; !sameJSON(t, string(out), want) {
		t.Errorf("problem encodes to %s, want %s", out, want)
	}
	var problem Problem
	if err := json.Unmarshal([]byte(` + "`" + `{"code":1}` + "`" + `), &problem); err != nil || problem.AdditionalProperties != nil {
		t.Errorf("a problem without other members has AdditionalProperties %v (error %v), want nil", problem.AdditionalProperties, err)
	}
}
`

func TestWriteTypesJen_UnionMatchesAVariantOnlyWithItsConstValue(t *testing.T) {
	checkGenerated(t, discriminatedUnionSchema, map[string]string{"discriminated_test.go": discriminatedUnionTest})
}

func TestWriteTypesJen_CatchAllAndNoneVariants(t *testing.T) {
	checkGenerated(t, catchAllUnionSchema, map[string]string{"catch_all_test.go": catchAllUnionTest})
}
