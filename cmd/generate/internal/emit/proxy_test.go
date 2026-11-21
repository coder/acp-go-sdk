package emit

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

const proxySchemaJSON = `{"$defs": {
  "InitializeRequest": {"type": "object", "x-side": "agent", "x-method": "initialize"},
  "InitializeResponse": {"type": "object", "x-side": "agent", "x-method": "initialize"},
  "PromptRequest": {"type": "object", "x-side": "agent", "x-method": "session/prompt"},
  "PromptResponse": {"type": "object", "x-side": "agent", "x-method": "session/prompt"},
  "SessionNotification": {"type": "object", "x-side": "client", "x-method": "session/update"},
  "UnstableInitializeProxyRequest": {"x-side": "proxy", "x-method": "proxy/initialize", "allOf": [{"$ref": "#/$defs/InitializeRequest"}]},
  "UnstableInitializeProxyResponse": {"x-side": "proxy", "x-method": "proxy/initialize", "allOf": [{"$ref": "#/$defs/InitializeResponse"}]},
  "UnstableProxySuccessorRequest": {"type": "object", "x-side": "proxy", "x-method": "proxy/successor"},
  "UnstableProxySuccessorResponse": {"x-side": "proxy", "x-method": "proxy/successor"},
  "UnstableProxySuccessorNotification": {"type": "object", "x-side": "proxy", "x-method": "proxy/successor"}
}}`

func proxyMeta() *load.Meta {
	return &load.Meta{
		AgentMethods:  map[string]string{"initialize": "initialize", "session_prompt": "session/prompt"},
		ClientMethods: map[string]string{"session_update": "session/update"},
		ProxyMethods:  map[string]string{"proxy_initialize": "proxy/initialize", "proxy_successor": "proxy/successor"},
	}
}

// proxyGenDecls returns, from proxy_gen.go, the fields of each struct, the
// methods of each receiver type, and the case labels of each switch per
// function.
func proxyGenDecls(t *testing.T, src string) (fields, methods, cases map[string][]string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "proxy_gen.go", src, 0)
	if err != nil {
		t.Fatalf("parse proxy_gen.go: %v", err)
	}
	fields, methods, cases = map[string][]string{}, map[string][]string{}, map[string][]string{}
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			spec, ok := decl.Specs[0].(*ast.TypeSpec)
			if !ok {
				continue
			}
			for _, f := range spec.Type.(*ast.StructType).Fields.List {
				fields[spec.Name.Name] = append(fields[spec.Name.Name], f.Names[0].Name)
			}
		case *ast.FuncDecl:
			recv := decl.Recv.List[0].Type.(*ast.StarExpr).X.(*ast.Ident).Name
			methods[recv] = append(methods[recv], decl.Name.Name)
			ast.Inspect(decl.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CaseClause); ok {
					for _, e := range c.List {
						cases[decl.Name.Name] = append(cases[decl.Name.Name], e.(*ast.Ident).Name)
					}
				}
				return true
			})
		}
	}
	return fields, methods, cases
}

func TestWriteProxyJen_DeclaresTheProxyRole(t *testing.T) {
	var schema load.Schema
	if err := json.Unmarshal([]byte(proxySchemaJSON), &schema); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteProxyJen(dir, &schema, proxyMeta()); err != nil {
		t.Fatalf("WriteProxyJen: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "proxy_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	fields, methods, cases := proxyGenDecls(t, string(src))
	for _, tt := range []struct {
		what      string
		got, want []string
	}{
		// initialize is no handler: proxy/initialize stands in for it.
		{"UnstableProxyFromClient fields", fields["UnstableProxyFromClient"], []string{"UnstableInitializeProxy", "Prompt"}},
		{"UnstableProxyFromSuccessor fields", fields["UnstableProxyFromSuccessor"], []string{"SessionUpdate"}},
		{"handle cases", cases["handle"], []string{"ProxyMethodProxyInitialize", "ProxyMethodProxySuccessor", "AgentMethodInitialize", "AgentMethodSessionPrompt"}},
		{"handleFromSuccessor cases", cases["handleFromSuccessor"], []string{"ClientMethodSessionUpdate"}},
		// A proxy initializes its successor with initialize.
		{"UnstableProxySuccessor methods", methods["UnstableProxySuccessor"], []string{"Initialize", "Prompt"}},
		{"UnstableProxyClient methods", methods["UnstableProxyClient"], []string{"SessionUpdate"}},
	} {
		if !slices.Equal(tt.got, tt.want) {
			t.Errorf("%s = %v, want %v", tt.what, tt.got, tt.want)
		}
	}
	if want := `"proxies must be initialized with proxy/initialize"`; !strings.Contains(string(src), want) {
		t.Errorf("proxy_gen.go does not reject initialize with %s", want)
	}
}

// Without proxy methods, as in the stable schema or an old release,
// WriteProxyJen writes nothing and removes a proxy_gen.go left from a run
// that had them.
func TestWriteProxyJen_WithoutProxyMethodsRemovesProxyGen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy_gen.go")
	if err := os.WriteFile(path, []byte("package acp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := WriteProxyJen(dir, &load.Schema{}, &load.Meta{}); err != nil {
			t.Fatalf("WriteProxyJen: %v", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("proxy_gen.go is still there: %v", err)
		}
	}
}
