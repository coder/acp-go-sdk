package emit

import "testing"

// presenceSchema has a required member of each kind that holds its Go zero
// value when the JSON object lacks it: a plain string, an object without
// rules, integers, a boolean, a nullable string, an array with
// x-deserialize-default-on-null, and a free value. Server is a union whose
// http variant needs url, as McpServer has it, and Scoped a union wrapper
// with an own required property, as the form mode of CreateElicitationRequest
// has requestedSchema.
const presenceSchema = `{"$defs": {
  "Inner": {"type": "object", "properties": {"note": {"type": "string"}}},
  "Version": {"type": "integer", "format": "uint16"},
  "Config": {"type": "object",
    "required": ["name", "inner", "count", "version", "flag", "maybe", "args", "free"],
    "properties": {
      "name": {"type": "string"},
      "inner": {"$ref": "#/$defs/Inner"},
      "count": {"type": "integer"},
      "version": {"$ref": "#/$defs/Version"},
      "flag": {"type": "boolean"},
      "maybe": {"type": ["string", "null"]},
      "args": {"type": "array", "items": {"type": "string"}, "x-deserialize-default-on-null": true},
      "free": {},
      "title": {"type": "string"}}},
  "Holder": {"type": "object", "required": ["config"], "properties": {
    "config": {"$ref": "#/$defs/Config"},
    "more": {"type": "array", "items": {"$ref": "#/$defs/Config"}}}},
  "Http": {"type": "object", "required": ["url"], "properties": {"url": {"type": "string"}}},
  "Stdio": {"type": "object", "required": ["command"], "properties": {"command": {"type": "string"}}},
  "Server": {"anyOf": [
    {"type": "object", "required": ["type"], "properties": {"type": {"type": "string", "const": "http"}},
      "allOf": [{"$ref": "#/$defs/Http"}]},
    {"title": "stdio", "allOf": [{"$ref": "#/$defs/Stdio"}]}]},
  "SessionScope": {"type": "object", "required": ["sessionId"], "properties": {"sessionId": {"type": "string"}}},
  "RequestScope": {"type": "object", "required": ["requestId"], "properties": {"requestId": {"type": "string"}}},
  "Scoped": {"type": "object", "required": ["message"], "properties": {"message": {"type": "string"}},
    "anyOf": [
      {"title": "Session", "allOf": [{"$ref": "#/$defs/SessionScope"}]},
      {"title": "Request", "allOf": [{"$ref": "#/$defs/RequestScope"}]}]}
}}`

const presenceTest = `package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const config = ` + "`" + `{"args":[],"count":0,"flag":false,"free":null,"inner":{},"maybe":null,"name":"","version":0}` + "`" + `

// without returns config without the member prop.
func without(prop string) string {
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(config), &members); err != nil {
		panic(err)
	}
	delete(members, prop)
	out, err := json.Marshal(members)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// with returns config with the member prop set to value.
func with(prop, value string) string {
	return strings.Replace(without(prop), "{", ` + "`" + `{"` + "`" + `+prop+` + "`" + `":` + "`" + `+value+",", 1)
}

// typeError describes err, a *json.UnmarshalTypeError, by the JSON value, the
// struct and the field that it names, and the Go type: "null Config.name string".
func typeError(err error) string {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		return fmt.Sprintf("not a type error: %v", err)
	}
	return typeErr.Value + " " + typeErr.Struct + "." + typeErr.Field + " " + typeErr.Type.String()
}

func TestConfig_UnmarshalJSON_RejectsAnAbsentRequiredMember(t *testing.T) {
	types := map[string]string{
		"args": "[]string", "count": "int", "flag": "bool", "free": "json.RawMessage",
		"inner": "acp.Inner", "maybe": "*string", "name": "string", "version": "acp.Version",
	}
	for prop, typ := range types {
		var c Config
		err := json.Unmarshal([]byte(without(prop)), &c)
		if got, want := typeError(err), "missing member Config."+prop+" "+typ; got != want {
			t.Errorf("Config without %s: error %s, want %s", prop, got, want)
		}
	}
}

func TestConfig_UnmarshalJSON_RejectsNullWhereTheSchemaDoesNotTakeIt(t *testing.T) {
	for _, prop := range []string{"count", "flag", "inner", "name", "version"} {
		var c Config
		err := json.Unmarshal([]byte(with(prop, "null")), &c)
		if got := typeError(err); !strings.HasPrefix(got, "null Config."+prop+" ") {
			t.Errorf("Config with a null %s: error %s, want a null type error at Config.%s", prop, got, prop)
		}
	}
}

func TestConfig_UnmarshalJSON_TakesPresentZeroValuesAndAllowedNulls(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"present zero values and a null of a nullable member", config, ""},
		{"null where default-on-null reads it as the default", with("args", "null"), config},
		{"a key in another case, which encoding/json reads", strings.Replace(config, ` + "`" + `"name"` + "`" + `, ` + "`" + `"Name"` + "`" + `, 1), config},
		{"values", ` + "`" + `{"args":["a"],"count":2,"flag":true,"free":{"k":1},"inner":{"note":"x"},"maybe":"m","name":"n","version":1}` + "`" + `, ""},
	}
	for _, tt := range tests {
		var c Config
		if err := json.Unmarshal([]byte(tt.in), &c); err != nil {
			t.Errorf("%s: unmarshal %s: %v", tt.name, tt.in, err)
			continue
		}
		want := tt.want
		if want == "" {
			want = tt.in
		}
		if out, err := json.Marshal(c); err != nil || string(out) != want {
			t.Errorf("%s: %s decodes and encodes to %s (error %v), want %s", tt.name, tt.in, out, err, want)
		}
	}
}

// encoding/json names the outer struct and the path to the member. A null
// where a Config goes has no members at all.
func TestHolder_UnmarshalJSON_NamesThePathOfANestedMember(t *testing.T) {
	tests := []struct{ in, want string }{
		{` + "`" + `{"config":` + "`" + ` + without("name") + "}", "missing member Holder.config.name string"},
		{` + "`" + `{"config":` + "`" + ` + config + ` + "`" + `,"more":[null]}` + "`" + `, "missing member Holder.more.args []string"},
		{` + "`" + `{}` + "`" + `, "missing member Holder.config acp.Config"},
	}
	for _, tt := range tests {
		var h Holder
		if got := typeError(json.Unmarshal([]byte(tt.in), &h)); got != tt.want {
			t.Errorf("%s: error %s, want %s", tt.in, got, tt.want)
		}
	}
}

// A decode into a reused value starts each required field at zero, so a value
// that the field holds from before cannot hide an absent member. Every other
// required member decodes to a value that is not zero, so only the reused
// count could hide the absent one. An optional field keeps what it held.
func TestConfig_UnmarshalJSON_ChecksRequiredMembersOfAReusedValue(t *testing.T) {
	const others = "\"args\":[\"a\"],\"flag\":true,\"free\":{\"k\":1},\"inner\":{\"note\":\"x\"},\"maybe\":\"m\",\"name\":\"n\",\"version\":1"
	tests := []struct{ in, want string }{
		{"{" + others + "}", "missing member Config.count int"},
		{"{\"count\":null," + others + "}", "null Config.count int"},
	}
	for _, tt := range tests {
		c := Config{Count: 5}
		if got := typeError(json.Unmarshal([]byte(tt.in), &c)); got != tt.want {
			t.Errorf("%s into a reused Config: error %s, want %s", tt.in, got, tt.want)
		}
	}
	c := Config{Title: "old"}
	if err := json.Unmarshal([]byte(config), &c); err != nil || c.Title != "old" {
		t.Errorf("reused Config decodes %s to title %q (error %v), want the title it held", config, c.Title, err)
	}
}

func TestServer_UnmarshalJSON_RejectsAVariantWithoutARequiredMember(t *testing.T) {
	var s Server
	if err := json.Unmarshal([]byte(` + "`" + `{"type":"http"}` + "`" + `), &s); err == nil {
		t.Errorf("an http server without url decodes to %+v, want an error", s)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"type":"http","url":""}` + "`" + `), &s); err != nil || s.Http == nil {
		t.Errorf("an http server with an empty url decodes to %+v (error %v), want the http variant", s, err)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"command":""}` + "`" + `), &s); err != nil || s.Stdio == nil {
		t.Errorf("a stdio server with an empty command decodes to %+v (error %v), want the stdio variant", s, err)
	}
}

func TestScoped_UnmarshalJSON_RejectsAnAbsentOwnProperty(t *testing.T) {
	var s Scoped
	const want = "missing member Scoped.message string"
	if got := typeError(json.Unmarshal([]byte(` + "`" + `{"sessionId":"s"}` + "`" + `), &s)); got != want {
		t.Errorf("error %s, want %s", got, want)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"message":"","sessionId":"s"}` + "`" + `), &s); err != nil || s.Session == nil {
		t.Errorf("a scope with an empty message decodes to %+v (error %v), want the session scope", s, err)
	}
}
`

func TestWriteTypesJen_RequiredMembersMustBePresent(t *testing.T) {
	checkGenerated(t, presenceSchema, map[string]string{"presence_test.go": presenceTest})
}
