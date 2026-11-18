package emit

import "testing"

const hintedStructSchema = `{"$defs": {
  "Kind": {"type": "string", "enum": ["read", "edit"]},
  "Entry": {"type": "object", "required": ["content"], "properties": {"content": {"type": "string"}}},
  "Update": {"type": "object", "required": ["id"], "properties": {
    "id": {"type": "string"},
    "strict": {"type": ["integer", "null"]},
    "plainKind": {"anyOf": [{"$ref": "#/$defs/Kind"}, {"type": "null"}]},
    "_meta": {"type": ["object", "null"], "additionalProperties": true, "x-deserialize-default-on-error": true},
    "title": {"type": ["string", "null"], "x-deserialize-default-on-error": true},
    "entry": {"anyOf": [{"$ref": "#/$defs/Entry"}, {"type": "null"}], "x-deserialize-default-on-error": true},
    "kind": {"anyOf": [{"$ref": "#/$defs/Kind"}, {"type": "null"}], "x-deserialize-default-on-error": true},
    "mode": {"allOf": [{"$ref": "#/$defs/Kind"}], "default": "edit", "x-deserialize-default-on-error": true},
    "kinds": {"type": ["array", "null"], "items": {"$ref": "#/$defs/Kind"},
      "x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true},
    "entries": {"type": ["array", "null"], "items": {"$ref": "#/$defs/Entry"},
      "x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true},
    "tags": {"type": "array", "items": {"type": "string"}, "x-deserialize-skip-invalid-items": true},
    "methods": {"type": "array", "items": {"type": "string"}, "default": [],
      "x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true},
    "modes": {"type": "array", "items": {"$ref": "#/$defs/Kind"}, "default": [],
      "x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true},
    "enabled": {"type": "boolean", "default": false, "x-deserialize-default-on-error": true}
  }}
}}`

const hintedStructTest = `package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestUpdate_UnmarshalJSON_HonorsHints(t *testing.T) {
	const defaults = ` + "`" + `"id":"u","mode":"edit","methods":[],"modes":[]` + "`" + `
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"valid values are kept",
			` + "`" + `{"id":"u","_meta":{"k":1},"title":"t","entry":{"content":"c"},"kind":"read","mode":"read","kinds":["edit"],"entries":[{"content":"c"}],"tags":["a"],"methods":["m"],"modes":["read"],"enabled":true}` + "`" + `,
			` + "`" + `{"id":"u","_meta":{"k":1},"title":"t","entry":{"content":"c"},"kind":"read","mode":"read","kinds":["edit"],"entries":[{"content":"c"}],"tags":["a"],"methods":["m"],"modes":["read"],"enabled":true}` + "`" + `},
		{"a value of the wrong shape reads as absent",
			` + "`" + `{"id":"u","_meta":"x","title":5,"entry":"x","kind":7,"kinds":"x","entries":{"a":1}}` + "`" + `,
			` + "`" + `{` + "`" + ` + defaults + ` + "`" + `}` + "`" + `},
		{"a value of the wrong shape reads as the schema default",
			` + "`" + `{"id":"u","mode":7,"methods":"x","modes":{"a":1},"enabled":"yes"}` + "`" + `,
			` + "`" + `{` + "`" + ` + defaults + ` + "`" + `}` + "`" + `},
		{"an unknown value of a closed enum reads as absent or as the default",
			` + "`" + `{"id":"u","kind":"delete","mode":"delete"}` + "`" + `,
			` + "`" + `{` + "`" + ` + defaults + ` + "`" + `}` + "`" + `},
		{"items of the wrong shape are dropped",
			` + "`" + `{"id":"u","kinds":["read","delete",5],"entries":[{"content":"c"},"x",null,{"content":7}],"tags":["a",5,null,"b"],"methods":["m",5],"modes":["delete","edit"]}` + "`" + `,
			` + "`" + `{"id":"u","mode":"edit","kinds":["read"],"entries":[{"content":"c"}],"tags":["a","b"],"methods":["m"],"modes":["edit"]}` + "`" + `},
		{"an unknown enum value without a hint is kept",
			` + "`" + `{"id":"u","plainKind":"delete"}` + "`" + `,
			` + "`" + `{"id":"u","plainKind":"delete",` + "`" + ` + defaults[len(` + "`" + `"id":"u",` + "`" + `):] + ` + "`" + `}` + "`" + `},
	}
	for _, tt := range tests {
		var got Update
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
		if !reflect.DeepEqual(gotJSON, wantJSON) {
			t.Errorf("%s: %s decoded to %s, want %s", tt.name, tt.in, out, tt.want)
		}
	}
}

func TestUpdate_UnmarshalJSON_StaysStrictWithoutDefaultOnError(t *testing.T) {
	tests := []struct{ name, in string }{
		{"required property", ` + "`" + `{"id":5}` + "`" + `},
		{"optional property", ` + "`" + `{"id":"u","strict":"x"}` + "`" + `},
		{"closed enum", ` + "`" + `{"id":"u","plainKind":5}` + "`" + `},
		{"skip-invalid-items alone, value is no array", ` + "`" + `{"id":"u","tags":"x"}` + "`" + `},
	}
	for _, tt := range tests {
		var got Update
		if err := json.Unmarshal([]byte(tt.in), &got); err == nil {
			t.Errorf("%s: unmarshal %s: no error, want one", tt.name, tt.in)
		}
	}
}
`

func TestWriteTypesJen_StructHonorsDeserializeHints(t *testing.T) {
	checkGenerated(t, hintedStructSchema, map[string]string{"hints_test.go": hintedStructTest})
}

// requiredArraySchema models NewSessionRequest from schema 1.27.0 on: its
// required "mcpServers" array has x-deserialize-default-on-null.
const requiredArraySchema = `{"$defs": {
  "NewSessionRequest": {"type": "object", "required": ["cwd", "mcpServers", "roots", "suggestions"], "properties": {
    "cwd": {"type": "string"},
    "mcpServers": {"type": "array", "items": {"type": "string"}, "x-deserialize-default-on-null": true},
    "roots": {"type": "array", "items": {"type": "string"}},
    "suggestions": {"type": "array", "items": {"type": "string"}, "x-deserialize-skip-invalid-items": true},
    "tags": {"type": "array", "items": {"type": "string"}}
  }}
}}`

const requiredArrayTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestNewSessionRequest_MarshalJSON_WritesNilRequiredArraysAsEmpty(t *testing.T) {
	got, err := json.Marshal(NewSessionRequest{Cwd: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	if want := ` + "`" + `{"cwd":"/work","mcpServers":[],"roots":[],"suggestions":[]}` + "`" + `; string(got) != want {
		t.Errorf("zero request encodes to %s, want %s", got, want)
	}
}

func TestNewSessionRequest_UnmarshalJSON_ReadsNullOnlyWhereHinted(t *testing.T) {
	tests := []struct {
		name         string
		payload      string
		wantDecodeOK bool
		wantValid    bool
	}{
		{"all arrays present", ` + "`" + `{"cwd":"/w","mcpServers":["a"],"roots":[],"suggestions":["s",5]}` + "`" + `, true, true},
		{"null with default-on-null reads as empty", ` + "`" + `{"cwd":"/w","mcpServers":null,"roots":[],"suggestions":[]}` + "`" + `, true, true},
		{"absent with default-on-null stays missing", ` + "`" + `{"cwd":"/w","roots":[],"suggestions":[]}` + "`" + `, true, false},
		{"default-on-null does not tolerate another type", ` + "`" + `{"cwd":"/w","mcpServers":"x","roots":[],"suggestions":[]}` + "`" + `, false, false},
		{"null without a hint stays missing", ` + "`" + `{"cwd":"/w","mcpServers":[],"roots":null,"suggestions":[]}` + "`" + `, true, false},
		{"null with skip-invalid-items alone stays missing", ` + "`" + `{"cwd":"/w","mcpServers":[],"roots":[],"suggestions":null}` + "`" + `, true, false},
	}
	for _, tt := range tests {
		var req NewSessionRequest
		err := json.Unmarshal([]byte(tt.payload), &req)
		if (err == nil) != tt.wantDecodeOK {
			t.Errorf("%s: unmarshal %s: error = %v, want ok = %v", tt.name, tt.payload, err, tt.wantDecodeOK)
			continue
		}
		if err != nil {
			continue
		}
		if err := req.Validate(); (err == nil) != tt.wantValid {
			t.Errorf("%s: Validate() on %s = %v, want valid = %v", tt.name, tt.payload, err, tt.wantValid)
		}
	}
}
`

func TestWriteTypesJen_RequiredArrayIsNeverNull(t *testing.T) {
	checkGenerated(t, requiredArraySchema, map[string]string{"required_array_test.go": requiredArrayTest})
}

// otherEnumSchema models ToolKind: a closed enum whose member "other" stands
// for every value that the enum does not list. Level writes the same enum
// with the "enum" keyword.
const otherEnumSchema = `{"$defs": {
  "Kind": {"oneOf": [{"const": "read", "type": "string"}, {"const": "other", "type": "string"}]},
  "Level": {"type": "string", "enum": ["low", "other"]},
  "Call": {"type": "object", "required": ["id"], "properties": {
    "id": {"type": "string"},
    "kind": {"allOf": [{"$ref": "#/$defs/Kind"}]},
    "level": {"anyOf": [{"$ref": "#/$defs/Level"}, {"type": "null"}], "x-deserialize-default-on-error": true},
    "kinds": {"type": "array", "items": {"$ref": "#/$defs/Kind"}, "x-deserialize-skip-invalid-items": true}
  }}
}}`

const otherEnumTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestCall_UnmarshalJSON_ReadsAnUnknownEnumValueAsOther(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"listed values are kept",
			` + "`" + `{"id":"c","kind":"read","kinds":["read","other"],"level":"low"}` + "`" + `,
			` + "`" + `{"id":"c","kind":"read","kinds":["read","other"],"level":"low"}` + "`" + `},
		{"a string that the enum does not list reads as other",
			` + "`" + `{"id":"c","kind":"teleport","level":"","kinds":["READ","read"]}` + "`" + `,
			` + "`" + `{"id":"c","kind":"other","kinds":["other","read"],"level":"other"}` + "`" + `},
		{"a value that is no string stays a bad value where a hint allows one",
			` + "`" + `{"id":"c","level":7,"kinds":[7,"read"]}` + "`" + `,
			` + "`" + `{"id":"c","kinds":["read"]}` + "`" + `},
		{"null leaves the property unset",
			` + "`" + `{"id":"c","kind":null,"level":null}` + "`" + `,
			` + "`" + `{"id":"c"}` + "`" + `},
	}
	for _, tt := range tests {
		var call Call
		if err := json.Unmarshal([]byte(tt.in), &call); err != nil {
			t.Errorf("%s: unmarshal %s: %v", tt.name, tt.in, err)
			continue
		}
		if out, err := json.Marshal(call); err != nil || string(out) != tt.want {
			t.Errorf("%s: %s decodes and encodes to %s (error %v), want %s", tt.name, tt.in, out, err, tt.want)
		}
	}

	var call Call
	if err := json.Unmarshal([]byte(` + "`" + `{"id":"c","kind":7}` + "`" + `), &call); err == nil {
		t.Errorf("a number as kind, which has no hint, decodes to %+v, want an error", call)
	}
}
`

func TestWriteTypesJen_EnumWithOtherReadsUnknownValuesAsOther(t *testing.T) {
	checkGenerated(t, otherEnumSchema, map[string]string{"other_enum_test.go": otherEnumTest})
}
