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
	files := map[string]string{"go.mod": "module example.com/acp\n\ngo 1.21\n"}
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
