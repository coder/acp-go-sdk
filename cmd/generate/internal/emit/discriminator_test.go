package emit

import "testing"

// discriminatedUnionSchema has the union shapes of the released schemas that
// have a const: Outcome like RequestPermissionOutcome, Server like McpServer,
// whose stdio variant has no const, and Shape, whose const key is optional.
// Reply is AgentResponse, whose required id and result accept null, and
// Options is SessionConfigSelectOptions, two arrays told apart by the required
// keys of their items. Runner and Runners select Launch by required keys that
// have a hint, as McpServer selects its stdio variant from schema 1.27.0 and
// SessionConfigSelectOptions its groups from 1.17.0.
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
  ]},
  "Id": {"anyOf": [{"title": "null", "type": "null"}, {"title": "number", "type": "integer"}]},
  "Reply": {"anyOf": [
    {"title": "result", "type": "object", "required": ["id", "result"], "properties": {
      "id": {"$ref": "#/$defs/Id"}, "result": {}}},
    {"title": "error", "type": "object", "required": ["id", "error"], "properties": {
      "id": {"type": ["integer", "null"]}, "error": {"type": "string"}}}
  ]},
  "Option": {"type": "object", "required": ["value"], "properties": {"value": {"type": "string"}}},
  "Group": {"type": "object", "required": ["group"], "properties": {"group": {"type": "string"}}},
  "Options": {"anyOf": [
    {"title": "flat", "type": "array", "items": {"$ref": "#/$defs/Option"}},
    {"title": "grouped", "type": "array", "items": {"$ref": "#/$defs/Group"}}
  ]},
  "Launch": {"type": "object", "required": ["command", "args", "env"], "properties": {
    "command": {"type": "string"},
    "args": {"type": "array", "items": {"type": "string"}, "x-deserialize-default-on-null": true},
    "env": {"type": "array", "items": {"type": "string"},
      "x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true}}},
  "Runner": {"anyOf": [
    {"title": "remote", "type": "object", "required": ["url"], "properties": {"url": {"type": "string"}}},
    {"title": "local", "allOf": [{"$ref": "#/$defs/Launch"}]}
  ]},
  "Runners": {"anyOf": [
    {"title": "flat", "type": "array", "items": {"$ref": "#/$defs/Option"}},
    {"title": "local", "type": "array", "items": {"$ref": "#/$defs/Launch"}}
  ]},
  "Code": {"anyOf": [
    {"title": "low", "type": "integer", "const": 1},
    {"title": "high", "type": "integer", "const": 2},
    {"title": "auto", "type": "string", "const": "auto"},
    {"title": "other", "type": "integer"}
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
		{func() any { return new(Server) }, ` + "`" + `{"name":"n","command":null}` + "`" + `, ""},
		{func() any { return new(Server) }, ` + "`" + `{"name":null,"command":"c"}` + "`" + `, ""},
		{func() any { return new(Reply) }, ` + "`" + `{"id":null,"result":null}` + "`" + `, ` + "`" + `{"id":null,"result":null}` + "`" + `},
		{func() any { return new(Reply) }, ` + "`" + `{"id":7,"result":{"ok":true}}` + "`" + `, ` + "`" + `{"id":7,"result":{"ok":true}}` + "`" + `},
		{func() any { return new(Reply) }, ` + "`" + `{"id":null,"error":"boom"}` + "`" + `, ` + "`" + `{"error":"boom","id":null}` + "`" + `},
		{func() any { return new(Reply) }, ` + "`" + `{"id":null,"error":null}` + "`" + `, ""},
		{func() any { return new(Options) }, ` + "`" + `[{"value":"v"}]` + "`" + `, ` + "`" + `[{"value":"v"}]` + "`" + `},
		{func() any { return new(Options) }, ` + "`" + `[{"group":"g"}]` + "`" + `, ` + "`" + `[{"group":"g"}]` + "`" + `},
		{func() any { return new(Options) }, ` + "`" + `[{"value":null}]` + "`" + `, ""},
		{func() any { return new(Options) }, ` + "`" + `[{"value":null,"group":"g"}]` + "`" + `, ` + "`" + `[{"group":"g"}]` + "`" + `},
		{func() any { return new(Runner) }, ` + "`" + `{"command":"c","args":null,"env":[]}` + "`" + `, ` + "`" + `{"args":[],"command":"c","env":[]}` + "`" + `},
		{func() any { return new(Runner) }, ` + "`" + `{"command":"c","args":[],"env":null}` + "`" + `, ` + "`" + `{"args":[],"command":"c","env":[]}` + "`" + `},
		{func() any { return new(Runner) }, ` + "`" + `{"command":null,"args":[],"env":[]}` + "`" + `, ""},
		{func() any { return new(Runner) }, ` + "`" + `{"command":"c","env":[]}` + "`" + `, ""},
		{func() any { return new(Runners) }, ` + "`" + `[{"command":"c","args":null,"env":null}]` + "`" + `, ` + "`" + `[{"args":[],"command":"c","env":[]}]` + "`" + `},
		{func() any { return new(Runners) }, ` + "`" + `[{"command":null,"args":[],"env":[]}]` + "`" + `, ""},
		{func() any { return new(Id) }, ` + "`" + `null` + "`" + `, ` + "`" + `null` + "`" + `},
		{func() any { return new(Id) }, ` + "`" + `7` + "`" + `, ` + "`" + `7` + "`" + `},
		{func() any { return new(Id) }, ` + "`" + `{}` + "`" + `, ""},
		{func() any { return new(Id) }, ` + "`" + `{"a":1}` + "`" + `, ""},
		{func() any { return new(Options) }, ` + "`" + `null` + "`" + `, ""},
		{func() any { return new(Reply) }, ` + "`" + `null` + "`" + `, ""},
		{func() any { return new(Outcome) }, ` + "`" + `null` + "`" + `, ""},
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

func TestCode_UnmarshalJSON_SelectsTheVariantOfTheConst(t *testing.T) {
	tests := []struct {
		in      string
		variant string
		set     func(Code) bool
	}{
		{"1", "low", func(c Code) bool { return c.Low != nil }},
		{"2", "high", func(c Code) bool { return c.High != nil }},
		{` + "`" + `"auto"` + "`" + `, "auto", func(c Code) bool { return c.Auto != nil }},
		{"3", "other", func(c Code) bool { return c.Other != nil }},
	}
	for _, tt := range tests {
		var code Code
		if err := json.Unmarshal([]byte(tt.in), &code); err != nil || !tt.set(code) {
			t.Errorf("%s decodes to %+v (error %v), want the variant %s", tt.in, code, err, tt.variant)
			continue
		}
		if out, err := json.Marshal(code); err != nil || string(out) != tt.in {
			t.Errorf("%s decodes and encodes to %s (error %v), want it unchanged", tt.in, out, err)
		}
	}
	var code Code
	for _, in := range []string{` + "`" + `"manual"` + "`" + `, "1.5"} {
		if err := json.Unmarshal([]byte(in), &code); err == nil {
			t.Errorf("unmarshal %s gives %+v, want an error", in, code)
		}
	}
}

func TestUnion_UnmarshalJSON_SelectsAVariantByExactKeys(t *testing.T) {
	// The upstream Rust types read keys exactly too.
	tests := []struct {
		into any
		in   string
	}{
		{new(Outcome), ` + "`" + `{"Outcome":"selected","optionId":"o"}` + "`" + `},
		{new(Server), ` + "`" + `{"Name":"n","Command":"c"}` + "`" + `},
		{new(Options), ` + "`" + `[{"Value":"v"}]` + "`" + `},
	}
	for _, tt := range tests {
		if err := json.Unmarshal([]byte(tt.in), tt.into); err == nil {
			t.Errorf("unmarshal %s gives %+v, want an error for a key in another case", tt.in, tt.into)
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
// holds Idle. No released schema has the last two: Marked has a required
// property that is always null, and Bag names no property at all.
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
  "Bag": {"type": "object", "additionalProperties": true},
  "Marked": {"type": "object", "required": ["marker"], "properties": {
    "marker": {"type": "null"}, "note": {"type": "string"}}},
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
		{func() any { return new(Bag) }, ` + "`" + `{"a":1,"b":[true]}` + "`" + `, ""},
		{func() any { return new(Bag) }, ` + "`" + `{}` + "`" + `, ""},
		{func() any { return new(Marked) }, ` + "`" + `{"marker":null,"note":"n"}` + "`" + `, ""},
		{func() any { return new(Marked) }, ` + "`" + `{"note":"n"}` + "`" + `, ` + "`" + `{"marker":null,"note":"n"}` + "`" + `},
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
	if custom.Other == nil || custom.Other.StopReason != "_custom" || string(custom.Other.AdditionalProperties["detail"]) != "1" {
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
	for _, into := range []any{new(Idle), new(Strict), new(State)} {
		if err := json.Unmarshal([]byte("null"), into); err == nil {
			t.Errorf("unmarshal null gives %+v, want an error, because a none variant is an object", into)
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
	out, err := json.Marshal(Problem{Code: 1, AdditionalProperties: map[string]json.RawMessage{"code": []byte("9"), "retry": []byte("true")}})
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

func TestAdditionalProperties_NumbersKeepEveryDigit(t *testing.T) {
	tests := []struct {
		into any
		in   string
	}{
		{new(Problem), ` + "`" + `{"big":9007199254740993,"code":1,"huge":1e400}` + "`" + `},
		{new(Idle), ` + "`" + `{"big":9007199254740993,"huge":1e400,"stopReason":"_custom"}` + "`" + `},
		{new(State), ` + "`" + `{"big":9007199254740993,"state":"_later"}` + "`" + `},
	}
	for _, tt := range tests {
		if err := json.Unmarshal([]byte(tt.in), tt.into); err != nil {
			t.Errorf("unmarshal %s: %v", tt.in, err)
			continue
		}
		if out, err := json.Marshal(tt.into); err != nil || string(out) != tt.in {
			t.Errorf("%s decodes and encodes to %s (error %v), want it unchanged", tt.in, out, err)
		}
	}
}
`

func TestWriteTypesJen_UnionMatchesAVariantOnlyWithItsConstValue(t *testing.T) {
	checkGenerated(t, discriminatedUnionSchema, map[string]string{"discriminated_test.go": discriminatedUnionTest})
}

func TestWriteTypesJen_CatchAllAndNoneVariants(t *testing.T) {
	checkGenerated(t, catchAllUnionSchema, map[string]string{"catch_all_test.go": catchAllUnionTest})
}
