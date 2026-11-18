package emit

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// objectUnionRequestSchema models CreateElicitationRequest: an object with its
// own required "message" property and a union of modes (oneOf in schema 0.13.5,
// anyOf from 1.18.0 on). The %s is the union keyword. emitUnion turns it into a
// wrapper of variant pointers and copies "message" into each inline variant.
const objectUnionRequestSchema = `{"$defs": {
  "FormMode": {"type": "object", "required": ["requestedSchema"], "properties": {
    "requestedSchema": {"type": "object"}}},
  "UrlMode": {"type": "object", "required": ["url"], "properties": {"url": {"type": "string"}}},
  "CreateElicitationRequest": {"type": "object", "required": ["message"],
    "properties": {"message": {"type": "string"}},
    "%s": [
      {"type": "object", "required": ["mode"], "properties": {"mode": {"type": "string", "const": "form"}},
        "allOf": [{"$ref": "#/$defs/FormMode"}]},
      {"type": "object", "required": ["mode"], "properties": {"mode": {"type": "string", "const": "url"}},
        "allOf": [{"$ref": "#/$defs/UrlMode"}]}
    ]}
}}`

// The generated dispatch decodes each request and calls Validate, which must
// reject a request that lacks the required "message".
const objectUnionRequestTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestCreateElicitationRequest_Validate(t *testing.T) {
	tests := []struct {
		payload string
		wantErr bool
	}{
		{payload: ` + "`" + `{"mode":"url","message":"Sign in","url":"https://example.com"}` + "`" + `},
		{payload: ` + "`" + `{"mode":"url","url":"https://example.com"}` + "`" + `, wantErr: true},
		{payload: ` + "`" + `{"mode":"form","requestedSchema":{}}` + "`" + `, wantErr: true},
	}
	for _, tt := range tests {
		var req CreateElicitationRequest
		if err := json.Unmarshal([]byte(tt.payload), &req); err != nil {
			t.Fatalf("unmarshal %s: %v", tt.payload, err)
		}
		if err := req.Validate(); (err != nil) != tt.wantErr {
			t.Errorf("Validate() on %s = %v, wantErr %v", tt.payload, err, tt.wantErr)
		}
	}
}
`

// The variants of RequestId are not objects, so its MarshalJSON returns each
// encoded variant as it is.
const scalarUnionSchema = `{"$defs": {
  "RequestId": {"anyOf": [
    {"title": "Null", "type": "null"},
    {"title": "Number", "type": "integer", "format": "int64"},
    {"title": "Str", "type": "string"}
  ]}
}}`

// scopedUnionSchema models the elicitation types: ElicitationFormMode and
// ElicitationUrlMode are objects with properties of their own plus an anyOf of
// two $ref scopes, and each mode of CreateElicitationRequest takes one of them
// in through allOf.
const scopedUnionSchema = `{"$defs": {
  "SessionScope": {"type": "object", "required": ["sessionId"], "properties": {
    "sessionId": {"type": "string"}, "toolCallId": {"type": ["string", "null"]}}},
  "RequestScope": {"type": "object", "required": ["requestId"], "properties": {"requestId": {"type": "string"}}},
  "FormMode": {"type": "object", "required": ["requestedSchema", "fields"],
    "properties": {
      "requestedSchema": {"type": "object"},
      "fields": {"type": "array", "items": {"type": "string"}},
      "_meta": {"type": ["object", "null"], "additionalProperties": true, "x-deserialize-default-on-error": true}},
    "anyOf": [
      {"title": "Session", "allOf": [{"$ref": "#/$defs/SessionScope"}]},
      {"title": "Request", "allOf": [{"$ref": "#/$defs/RequestScope"}]}]},
  "UrlMode": {"type": "object", "required": ["url"],
    "properties": {"url": {"type": "string"}},
    "anyOf": [
      {"title": "Session", "allOf": [{"$ref": "#/$defs/SessionScope"}]},
      {"title": "Request", "allOf": [{"$ref": "#/$defs/RequestScope"}]}]},
  "CreateElicitationRequest": {"type": "object", "required": ["message"],
    "properties": {"message": {"type": "string"}},
    "discriminator": {"propertyName": "mode"},
    "oneOf": [
      {"type": "object", "required": ["mode"], "properties": {"mode": {"type": "string", "const": "form"}},
        "allOf": [{"$ref": "#/$defs/FormMode"}]},
      {"type": "object", "required": ["mode"], "properties": {"mode": {"type": "string", "const": "url"}},
        "allOf": [{"$ref": "#/$defs/UrlMode"}]},
      {"title": "other", "type": "object", "required": ["mode"], "properties": {"mode": {"type": "string"}},
        "anyOf": [
          {"title": "Session", "allOf": [{"$ref": "#/$defs/SessionScope"}]},
          {"title": "Request", "allOf": [{"$ref": "#/$defs/RequestScope"}]}],
        "unevaluatedProperties": true,
        "not": {"anyOf": [
          {"type": "object", "required": ["mode"], "properties": {"mode": {"type": "string", "const": "form"}}},
          {"type": "object", "required": ["mode"], "properties": {"mode": {"type": "string", "const": "url"}}}]}}
    ]}
}}`

// openUnionSchema has open unions whose variants are a direct $ref, or
// come from an allOf.
const openUnionSchema = `{"$defs": {
  "SessionScope": {"type": "object", "required": ["sessionId"], "properties": {"sessionId": {"type": "string"}}},
  "RequestScope": {"type": "object", "required": ["requestId"], "properties": {"requestId": {"type": "string"}}},
  "Scope": {"anyOf": [{"$ref": "#/$defs/SessionScope"}, {"$ref": "#/$defs/RequestScope"}], "unevaluatedProperties": true},
  "TitledScope": {"anyOf": [
    {"title": "Session", "allOf": [{"$ref": "#/$defs/SessionScope"}]},
    {"title": "Request", "allOf": [{"$ref": "#/$defs/RequestScope"}]}], "unevaluatedProperties": true},
  "Mode": {"oneOf": [
    {"title": "other", "type": "object", "required": ["mode"], "properties": {"mode": {"type": "string"}},
      "allOf": [{"$ref": "#/$defs/TitledScope"}]}]},
  "Inner": {"anyOf": [
    {"title": "X", "type": "object", "required": ["x"], "properties": {"x": {"type": "string"}, "y": {"type": "string"}}},
    {"title": "Z", "type": "object", "required": ["z"], "properties": {"z": {"type": "string"}}}]},
  "Nested": {"anyOf": [{"$ref": "#/$defs/Inner"}], "unevaluatedProperties": true},
  "Plain": {"type": "object", "properties": {"a": {"type": "string"}}, "unevaluatedProperties": true}
}}`

const openUnionTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestOpenUnions_KeepUnnamedMembers(t *testing.T) {
	for _, tt := range []struct {
		into func() any
		in   string
	}{
		{func() any { return new(Scope) }, ` + "`" + `{"sessionId":"s","extra":1}` + "`" + `},
		{func() any { return new(Mode) }, ` + "`" + `{"mode":"m","requestId":"r","extra":{"k":[1]}}` + "`" + `},
		{func() any { return new(Nested) }, ` + "`" + `{"x":"1","y":"2","extra":1}` + "`" + `},
		{func() any { return new(Plain) }, ` + "`" + `{"a":"1","extra":[true]}` + "`" + `},
	} {
		v := tt.into()
		if err := json.Unmarshal([]byte(tt.in), v); err != nil {
			t.Fatalf("unmarshal %s: %v", tt.in, err)
		}
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", tt.in, err)
		}
		var a, b any
		json.Unmarshal(out, &a)
		json.Unmarshal([]byte(tt.in), &b)
		if ja, _ := json.Marshal(a); string(ja) != func() string { jb, _ := json.Marshal(b); return string(jb) }() {
			t.Errorf("round trip of %s gives %s", tt.in, out)
		}
	}
	var scope Scope
	if err := json.Unmarshal([]byte(` + "`" + `{"sessionId":"s","extra":1}` + "`" + `), &scope); err != nil {
		t.Fatal(err)
	}
	if len(scope.AdditionalProperties) != 1 {
		t.Errorf("AdditionalProperties = %s, want only extra", scope.AdditionalProperties)
	}
	var nested Nested
	if err := json.Unmarshal([]byte(` + "`" + `{"x":"1","y":"2","extra":1}` + "`" + `), &nested); err != nil {
		t.Fatal(err)
	}
	if len(nested.AdditionalProperties) != 1 {
		t.Errorf("Nested.AdditionalProperties = %s, want only extra", nested.AdditionalProperties)
	}
}
`

const scopedUnionTest = `package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func roundTrip(t *testing.T, v any, in string) string {
	t.Helper()
	if err := json.Unmarshal([]byte(in), v); err != nil {
		t.Fatalf("unmarshal %s: %v", in, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", in, err)
	}
	return string(out)
}

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

func TestScopedUnions_RoundTripOwnPropertiesAndScope(t *testing.T) {
	tests := []struct {
		into func() any
		in   string
	}{
		{func() any { return new(FormMode) }, ` + "`" + `{"requestedSchema":{"a":1},"fields":["f"],"_meta":{"k":"v"},"sessionId":"s","toolCallId":"t"}` + "`" + `},
		{func() any { return new(FormMode) }, ` + "`" + `{"requestedSchema":{},"fields":[],"requestId":"r"}` + "`" + `},
		{func() any { return new(UrlMode) }, ` + "`" + `{"url":"https://example.com","sessionId":"s"}` + "`" + `},
		{func() any { return new(CreateElicitationRequest) }, ` + "`" + `{"mode":"form","message":"m","requestedSchema":{"a":1},"fields":["f"],"sessionId":"s","toolCallId":"t"}` + "`" + `},
		{func() any { return new(CreateElicitationRequest) }, ` + "`" + `{"mode":"url","message":"m","url":"https://example.com","requestId":"r"}` + "`" + `},
		{func() any { return new(CreateElicitationRequest) }, ` + "`" + `{"mode":"_x","message":"m","sessionId":"s","extra":{"k":1},"n":12345678901234567890}` + "`" + `},
		{func() any { return new(CreateElicitationRequest) }, ` + "`" + `{"mode":"_x","message":"m","requestId":"r"}` + "`" + `},
	}
	for _, tt := range tests {
		if out := roundTrip(t, tt.into(), tt.in); !sameJSON(t, out, tt.in) {
			t.Errorf("round trip of %s gives %s", tt.in, out)
		}
	}
}

func TestCreateElicitationOther_KeepsTheMembersThatNoPartNames(t *testing.T) {
	var req CreateElicitationRequest
	roundTrip(t, &req, ` + "`" + `{"mode":"_x","message":"m","sessionId":"s","toolCallId":"t","extra":1}` + "`" + `)
	if req.Other == nil || len(req.Other.AdditionalProperties) != 1 || string(req.Other.AdditionalProperties["extra"]) != "1" {
		t.Fatalf("decoded %+v, want the other mode with only extra unnamed", req.Other)
	}
	req.Other.AdditionalProperties["message"] = json.RawMessage(` + "`" + `"x"` + "`" + `)
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if want := ` + "`" + `{"mode":"_x","message":"m","sessionId":"s","toolCallId":"t","extra":1}` + "`" + `; !sameJSON(t, string(out), want) {
		t.Errorf("encoded %s, want %s: a named member keeps its value", out, want)
	}
}

func TestFormMode_BadMetaReadsAsAbsentAndNilFieldsEncodesAsEmpty(t *testing.T) {
	var mode FormMode
	out := roundTrip(t, &mode, ` + "`" + `{"requestedSchema":{},"_meta":"bad","sessionId":"s"}` + "`" + `)
	if want := ` + "`" + `{"requestedSchema":{},"fields":[],"sessionId":"s"}` + "`" + `; !sameJSON(t, out, want) {
		t.Errorf("FormMode encodes to %s, want %s", out, want)
	}
	if mode.Session == nil || mode.Session.SessionId != "s" || mode.Request != nil {
		t.Errorf("FormMode scope = session %+v, request %+v, want the session scope", mode.Session, mode.Request)
	}
}

func TestFormMode_UnmarshalJSON_ReplacesTheScopeOfAReusedValue(t *testing.T) {
	var mode FormMode
	roundTrip(t, &mode, ` + "`" + `{"requestedSchema":{},"fields":[],"sessionId":"s"}` + "`" + `)
	in := ` + "`" + `{"requestedSchema":{},"fields":[],"requestId":"r"}` + "`" + `
	if out := roundTrip(t, &mode, in); !sameJSON(t, out, in) {
		t.Errorf("a FormMode decoded a second time encodes to %s, want %s", out, in)
	}
	if mode.Session != nil {
		t.Errorf("session scope = %+v after a request-scoped payload, want nil", mode.Session)
	}
}

func TestNewCreateElicitationRequest_EveryConstructorSetsAScope(t *testing.T) {
	tests := []struct {
		req       CreateElicitationRequest
		key, want string
	}{
		{NewCreateElicitationRequestFormSession(map[string]json.RawMessage{}, []string{}, "s"), "sessionId", "s"},
		{NewCreateElicitationRequestFormRequest(map[string]json.RawMessage{}, []string{}, "r"), "requestId", "r"},
		{NewCreateElicitationRequestUrlSession("https://example.com", "s"), "sessionId", "s"},
		{NewCreateElicitationRequestUrlRequest("https://example.com", "r"), "requestId", "r"},
	}
	for _, tt := range tests {
		out, err := json.Marshal(tt.req)
		if err != nil {
			t.Errorf("marshal the result of a constructor with %s %q: %v", tt.key, tt.want, err)
			continue
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("parse %s: %v", out, err)
		}
		if got[tt.key] != tt.want {
			t.Errorf("constructor result encodes to %s, want %s %q", out, tt.key, tt.want)
		}
	}
}

func TestCreateElicitationRequest_NeedsAScopeToDecode(t *testing.T) {
	in := ` + "`" + `{"mode":"form","message":"m","requestedSchema":{},"fields":[]}` + "`" + `
	var req CreateElicitationRequest
	if err := json.Unmarshal([]byte(in), &req); err == nil {
		t.Errorf("a form mode without a scope decodes to %+v, want an error", req.Form)
	}
}

func TestCreateElicitationRequest_NeedsAScopeToEncode(t *testing.T) {
	req := CreateElicitationRequest{Form: &CreateElicitationForm{Message: "m", RequestedSchema: map[string]json.RawMessage{}}}
	if out, err := json.Marshal(req); err == nil {
		t.Errorf("a form mode without a scope encodes to %s, want an error", out)
	}
	req.Form.Request = &RequestScope{RequestId: "r"}
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal with a request scope: %v", err)
	}
	if want := ` + "`" + `{"mode":"form","message":"m","requestedSchema":{},"fields":[],"requestId":"r"}` + "`" + `; !sameJSON(t, string(out), want) {
		t.Errorf("form mode encodes to %s, want %s", out, want)
	}
}
`

// JSON Schema applies "properties" to objects only, so a Label is a plain
// string or a Named object with an optional note.
const mixedUnionSchema = `{"$defs": {
  "Named": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
  "Label": {"properties": {"note": {"type": "string"}},
    "anyOf": [
      {"title": "Text", "type": "string"},
      {"title": "Named", "allOf": [{"$ref": "#/$defs/Named"}]}]}
}}`

const mixedUnionTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestLabel_RoundTripsAStringAndAnObjectWithOwnProperties(t *testing.T) {
	for _, in := range []string{` + "`" + `"plain"` + "`" + `, ` + "`" + `{"name":"n","note":"x"}` + "`" + `} {
		var label Label
		if err := json.Unmarshal([]byte(in), &label); err != nil {
			t.Errorf("unmarshal %s: %v", in, err)
			continue
		}
		out, err := json.Marshal(label)
		if err != nil {
			t.Errorf("marshal the label decoded from %s: %v", in, err)
			continue
		}
		if string(out) != in {
			t.Errorf("round trip of %s gives %s", in, out)
		}
	}
}
`

func TestWriteTypesJen_GeneratedPackagePassesVetAndTests(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		// extra holds hand-written files to build and test with the generated ones.
		extra map[string]string
	}{
		{
			name:   "object anyOf request",
			schema: fmt.Sprintf(objectUnionRequestSchema, "anyOf"),
			extra:  map[string]string{"validate_test.go": objectUnionRequestTest},
		},
		{
			name:   "object oneOf request",
			schema: fmt.Sprintf(objectUnionRequestSchema, "oneOf"),
			extra:  map[string]string{"validate_test.go": objectUnionRequestTest},
		},
		{name: "union of scalar variants", schema: scalarUnionSchema},
		{
			name:   "unions with own properties and a nested scope",
			schema: scopedUnionSchema,
			extra:  map[string]string{"scoped_test.go": scopedUnionTest},
		},
		{
			name:   "open unions with referenced variants",
			schema: openUnionSchema,
			extra:  map[string]string{"open_test.go": openUnionTest},
		},
		{
			name:   "union with own properties and a string alternative",
			schema: mixedUnionSchema,
			extra:  map[string]string{"mixed_test.go": mixedUnionTest},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkGenerated(t, tt.schema, tt.extra)
		})
	}
}

// checkGenerated writes types_gen.go and helpers_gen.go for schemaJSON, plus
// the extra files, into a scratch module, and runs go vet and go test there.
// go vet type-checks the package, so it fails when the generated code does not
// compile or has a vet finding such as unreachable code.
func checkGenerated(t *testing.T, schemaJSON string, extra map[string]string) {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("find the go tool: %v", err)
	}
	var schema load.Schema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	dir := t.TempDir()
	if err := WriteTypesJen(dir, &schema, &load.Meta{}); err != nil {
		t.Fatalf("WriteTypesJen: %v", err)
	}
	if err := WriteHelpersJen(dir, &schema, &load.Meta{}); err != nil {
		t.Fatalf("WriteHelpersJen: %v", err)
	}
	// The generated code needs the hand-written walker of the table shapes
	// and the types Meta and Patch of the SDK.
	files := map[string]string{"go.mod": "module example.com/acp\n\ngo 1.21\n"}
	for _, name := range []string{"shape.go", "meta.go", "patch.go"} {
		src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", name))
		if err != nil {
			t.Fatalf("read the SDK's %s: %v", name, err)
		}
		files[name] = string(src)
	}
	for name, src := range extra {
		files[name] = src
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, args := range [][]string{{"vet", "."}, {"test", "-count=1", "."}} {
		cmd := exec.Command(goTool, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s on the generated package: %v\n%s", args[0], err, out)
		}
	}
}
