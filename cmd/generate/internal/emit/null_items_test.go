package emit

import "testing"

// nullItemsSchema has arrays and maps of scalars, as AuthMethodTerminal has
// args and env and NewSessionRequest has additionalDirectories, next to fields
// whose schema or hints let null through. wrapped takes its item type through
// an allOf, as a property does. Change has a patch field, as
// SubagentUpdate has, Target an own property of a union wrapper, and Value an
// array variant, as ElicitationContentValue has.
const nullItemsSchema = `{"$defs": {
  "Kind": {"type": "string", "enum": ["read", "edit"]},
  "Named": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
  "Config": {"type": "object", "required": ["args"], "properties": {
    "args": {"type": "array", "items": {"type": "string"}, "x-deserialize-default-on-null": true},
    "dirs": {"type": ["array", "null"], "items": {"type": "string"}},
    "env": {"type": ["object", "null"], "additionalProperties": {"type": "string"}},
    "counts": {"type": "array", "items": {"type": "integer"}},
    "kinds": {"type": "array", "items": {"$ref": "#/$defs/Kind"}},
    "wrapped": {"type": "array", "items": {"allOf": [{"$ref": "#/$defs/Kind"}]}},
    "optional": {"type": "array", "items": {"type": ["string", "null"]}},
    "tags": {"type": "array", "items": {"type": "string"}, "x-deserialize-skip-invalid-items": true},
    "labels": {"type": "object", "additionalProperties": {"type": "string"}, "x-deserialize-default-on-error": true}
  }},
  "Change": {"type": "object", "properties": {
    "dirs": {"type": ["array", "null"], "items": {"type": "string"}, "description": "Omitted means unchanged. ` + "`null`" + ` clears it."}
  }},
  "Value": {"anyOf": [
    {"title": "Text", "type": "string"},
    {"title": "TextArray", "type": "array", "items": {"type": "string"}}]},
  "Target": {"properties": {"hosts": {"type": "array", "items": {"type": "string"}}},
    "anyOf": [
      {"title": "Text", "type": "string"},
      {"title": "Named", "allOf": [{"$ref": "#/$defs/Named"}]}]}
}}`

const nullItemsTest = `package acp

import (
	"encoding/json"
	"errors"
	"testing"
)

// nullError returns the error of decoding in into v unless it is a null type
// error at structName.prop.
func nullError(in string, v any, structName, prop string) error {
	err := json.Unmarshal([]byte(in), v)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Value == "null" && typeErr.Struct == structName && typeErr.Field == prop {
		return nil
	}
	if err == nil {
		return errors.New("no error")
	}
	return err
}

func TestConfig_UnmarshalJSON_RejectsNullItemsAndValues(t *testing.T) {
	tests := []struct{ name, in, prop string }{
		{"string item", ` + "`" + `{"args":["a",null]}` + "`" + `, "args"},
		{"string item of a nullable array", ` + "`" + `{"args":[],"dirs":[null]}` + "`" + `, "dirs"},
		{"string map value", ` + "`" + `{"args":[],"env":{"A":"1","B":null}}` + "`" + `, "env"},
		{"integer item", ` + "`" + `{"args":[],"counts":[1,null]}` + "`" + `, "counts"},
		{"enum item", ` + "`" + `{"args":[],"kinds":["read",null]}` + "`" + `, "kinds"},
		{"enum item through allOf", ` + "`" + `{"args":[],"wrapped":["read",null]}` + "`" + `, "wrapped"},
	}
	for _, tt := range tests {
		var c Config
		if err := nullError(tt.in, &c, "Config", tt.prop); err != nil {
			t.Errorf("%s: %s decodes with error %v, want a null type error at Config.%s", tt.name, tt.in, err, tt.prop)
		}
	}

	var c Config
	const want = "json: cannot unmarshal null into Go struct field Config.env of type string"
	if err := json.Unmarshal([]byte(` + "`" + `{"args":[],"env":{"A":null}}` + "`" + `), &c); err == nil || err.Error() != want {
		t.Errorf("error = %v, want %s", err, want)
	}
}

func TestConfig_UnmarshalJSON_ReadsNullWhereTheSchemaOrAHintAllowsIt(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"valid items and values are kept",
			` + "`" + `{"args":["a",""],"dirs":["d"],"env":{"A":""},"counts":[0],"kinds":["read"]}` + "`" + `,
			` + "`" + `{"args":["a",""],"counts":[0],"dirs":["d"],"env":{"A":""},"kinds":["read"]}` + "`" + `},
		{"a whole null with default-on-null reads as the default",
			` + "`" + `{"args":null}` + "`" + `,
			` + "`" + `{"args":[]}` + "`" + `},
		{"a whole null of a nullable field reads as absent",
			` + "`" + `{"args":[],"dirs":null,"env":null}` + "`" + `,
			` + "`" + `{"args":[]}` + "`" + `},
		{"an item schema that takes null reads it as the zero value",
			` + "`" + `{"args":[],"optional":["a",null]}` + "`" + `,
			` + "`" + `{"args":[],"optional":["a",""]}` + "`" + `},
		{"skip-invalid-items drops a null item",
			` + "`" + `{"args":[],"tags":["a",null,"b"]}` + "`" + `,
			` + "`" + `{"args":[],"tags":["a","b"]}` + "`" + `},
		{"default-on-error reads a map with a null value as absent",
			` + "`" + `{"args":[],"labels":{"a":null}}` + "`" + `,
			` + "`" + `{"args":[]}` + "`" + `},
	}
	for _, tt := range tests {
		var c Config
		if err := json.Unmarshal([]byte(tt.in), &c); err != nil {
			t.Errorf("%s: unmarshal %s: %v", tt.name, tt.in, err)
			continue
		}
		if out, err := json.Marshal(c); err != nil || string(out) != tt.want {
			t.Errorf("%s: %s decodes and encodes to %s (error %v), want %s", tt.name, tt.in, out, err, tt.want)
		}
	}
}

func TestChange_UnmarshalJSON_RejectsANullItemOfAPatch(t *testing.T) {
	var c Change
	if err := nullError(` + "`" + `{"dirs":["a",null]}` + "`" + `, &c, "Change", "dirs"); err != nil {
		t.Errorf("a null item of a patch decodes with error %v, want a null type error at Change.dirs", err)
	}
	for in, want := range map[string]string{` + "`" + `{}` + "`" + `: "unset", ` + "`" + `{"dirs":null}` + "`" + `: "null", ` + "`" + `{"dirs":["a"]}` + "`" + `: "[a]"} {
		var c Change
		if err := json.Unmarshal([]byte(in), &c); err != nil {
			t.Errorf("unmarshal %s: %v", in, err)
			continue
		}
		got := "unset"
		if dirs, ok := c.Dirs.Value(); ok && len(dirs) == 1 {
			got = "[" + dirs[0] + "]"
		} else if c.Dirs.IsNull() {
			got = "null"
		}
		if got != want {
			t.Errorf("%s decodes dirs as %s, want %s", in, got, want)
		}
	}
}

// A null item fails the array variant, so no variant matches.
func TestValue_UnmarshalJSON_RejectsANullItemOfAnArrayVariant(t *testing.T) {
	var v Value
	if err := json.Unmarshal([]byte(` + "`" + `["a",null]` + "`" + `), &v); err == nil {
		t.Errorf("an array variant with a null item decodes to %+v, want an error", v)
	}
	if err := json.Unmarshal([]byte(` + "`" + `["a",""]` + "`" + `), &v); err != nil || v.TextArray == nil || len(*v.TextArray) != 2 {
		t.Errorf("an array variant decodes to %+v (error %v), want two items", v, err)
	}
}

func TestTarget_UnmarshalJSON_RejectsANullItemOfAnOwnProperty(t *testing.T) {
	var target Target
	if err := nullError(` + "`" + `{"name":"n","hosts":["h",null]}` + "`" + `, &target, "Target", "hosts"); err != nil {
		t.Errorf("a null item of an own property decodes with error %v, want a null type error at Target.hosts", err)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"name":"n","hosts":["h"]}` + "`" + `), &target); err != nil || len(target.Hosts) != 1 || target.Named == nil {
		t.Errorf("a valid target decodes to %+v (error %v)", target, err)
	}
}
`

func TestWriteTypesJen_ScalarItemsAndValuesMustNotBeNull(t *testing.T) {
	checkGenerated(t, nullItemsSchema, map[string]string{"null_items_test.go": nullItemsTest})
}
