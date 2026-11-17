package emit

import (
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

func TestWriteConstantsJen_DeclaresConstants(t *testing.T) {
	meta := &load.Meta{
		Version:         1,
		AgentMethods:    map[string]string{"session_prompt": "session/prompt"},
		ClientMethods:   map[string]string{"session_update": "session/update"},
		ProtocolMethods: map[string]string{"cancel_request": "$/cancel_request"},
	}
	dir := t.TempDir()
	if err := WriteConstantsJen(dir, meta, "1.32.0"); err != nil {
		t.Fatalf("WriteConstantsJen: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "constants_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	ast.Inspect(parseGo(t, "constants_gen.go", string(src)), func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok {
			if lit, ok := vs.Values[0].(*ast.BasicLit); ok {
				got[vs.Names[0].Name] = lit.Value
			}
		}
		return true
	})
	want := map[string]string{
		"ProtocolVersionNumber":       `1`,
		"SchemaVersion":               `"1.32.0"`,
		"AgentMethodSessionPrompt":    `"session/prompt"`,
		"ClientMethodSessionUpdate":   `"session/update"`,
		"ProtocolMethodCancelRequest": `"$/cancel_request"`,
	}
	for name, lit := range want {
		if got[name] != lit {
			t.Errorf("%s = %q, want %s", name, got[name], lit)
		}
	}
}
