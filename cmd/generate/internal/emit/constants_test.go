package emit

import (
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

func TestWriteConstantsJen_DeclaresSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	if err := WriteConstantsJen(dir, &load.Meta{Version: 1}, "1.32.0"); err != nil {
		t.Fatalf("WriteConstantsJen: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "constants_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	var got string
	ast.Inspect(parseGo(t, "constants_gen.go", string(src)), func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok && vs.Names[0].Name == "SchemaVersion" {
			if lit, ok := vs.Values[0].(*ast.BasicLit); ok {
				got = lit.Value
			}
		}
		return true
	})
	if got != `"1.32.0"` {
		t.Errorf("SchemaVersion = %s, want \"1.32.0\"", got)
	}
}
