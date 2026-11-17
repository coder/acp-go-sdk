package emit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// The variants of RequestId are not objects, so its MarshalJSON returns each
// encoded variant as it is.
const scalarUnionSchema = `{"$defs": {
  "RequestId": {"anyOf": [
    {"title": "Null", "type": "null"},
    {"title": "Number", "type": "integer", "format": "int64"},
    {"title": "Str", "type": "string"}
  ]}
}}`

func TestWriteTypesJen_GeneratedPackagePassesVet(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		// extra holds hand-written files to compile with the generated ones.
		extra map[string]string
	}{
		{name: "union of scalar variants", schema: scalarUnionSchema},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vetGenerated(t, tt.schema, tt.extra)
		})
	}
}

// vetGenerated writes types_gen.go and helpers_gen.go for schemaJSON, plus the
// extra files, into a scratch module and runs go vet there. go vet type-checks
// the package, so it fails when the generated code does not compile or has a
// vet finding such as unreachable code.
func vetGenerated(t *testing.T, schemaJSON string, extra map[string]string) {
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
	cmd := exec.Command(goTool, "vet", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go vet on the generated package: %v\n%s", err, out)
	}
}
