package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// interfaceMethods parses the types_gen.go that WriteTypesJen writes for
// schema and meta, and returns the method names of each interface.
func interfaceMethods(t *testing.T, schema *load.Schema, meta *load.Meta) map[string][]string {
	t.Helper()
	dir := t.TempDir()
	if err := WriteTypesJen(dir, schema, meta); err != nil {
		t.Fatalf("WriteTypesJen: %v", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, "types_gen.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse types_gen.go: %v", err)
	}
	ifaces := map[string][]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		if it, ok := spec.Type.(*ast.InterfaceType); ok {
			names := []string{}
			for _, m := range it.Methods.List {
				for _, name := range m.Names {
					names = append(names, name.Name)
				}
			}
			ifaces[spec.Name.Name] = names
		}
		return false
	})
	return ifaces
}

// forkSchema is an agent with session/new and session/fork. The prefix names
// the fork types: "Unstable" while the method is unstable, "" once it is
// stable.
func forkSchema(prefix string) (*load.Schema, *load.Meta) {
	def := func(method string) *load.Definition {
		return &load.Definition{Type: "object", XSide: "agent", XMethod: method}
	}
	return &load.Schema{Defs: map[string]*load.Definition{
			"NewSessionRequest":            def("session/new"),
			"NewSessionResponse":           def("session/new"),
			prefix + "ForkSessionRequest":  def("session/fork"),
			prefix + "ForkSessionResponse": def("session/fork"),
			"SessionNotification":          {Type: "object", XSide: "client", XMethod: "session/update"},
		}}, &load.Meta{
			AgentMethods:  map[string]string{"session_new": "session/new", "session_fork": "session/fork"},
			ClientMethods: map[string]string{"session_update": "session/update"},
		}
}

// A schema release that makes a method stable must not add it to Agent, or
// every agent stops compiling. The method moves from one optional interface
// to another, which breaks only the agents that implement it.
func TestWriteTypesJen_StabilizedMethodStaysOutOfAgent(t *testing.T) {
	schema, meta := forkSchema("Unstable")
	before := interfaceMethods(t, schema, meta)
	schema, meta = forkSchema("")
	after := interfaceMethods(t, schema, meta)

	for _, ifaces := range []map[string][]string{before, after} {
		if got := ifaces["Agent"]; !reflect.DeepEqual(got, []string{"NewSession"}) {
			t.Errorf("Agent has %v, want [NewSession]", got)
		}
		if got := ifaces["Client"]; !reflect.DeepEqual(got, []string{"SessionUpdate"}) {
			t.Errorf("Client has %v, want [SessionUpdate]", got)
		}
	}
	if got := before["AgentUnstableForkSession"]; !reflect.DeepEqual(got, []string{"UnstableForkSession"}) {
		t.Errorf("before: AgentUnstableForkSession has %v, want [UnstableForkSession]", got)
	}
	if got := after["AgentForkSession"]; !reflect.DeepEqual(got, []string{"ForkSession"}) {
		t.Errorf("after: AgentForkSession has %v, want [ForkSession]", got)
	}
	for name := range after {
		if strings.HasPrefix(name, "AgentUnstable") {
			t.Errorf("after: %s is still declared", name)
		}
	}
}

// An optional interface whose name a schema type already has is a generator
// bug: the generated package would not compile.
func TestWriteTypesJen_PanicsWhenAnOptionalInterfaceNameIsTaken(t *testing.T) {
	schema, meta := forkSchema("")
	schema.Defs["AgentForkSession"] = &load.Definition{Type: "string"}
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "AgentForkSession") {
			t.Fatalf("recover() = %v, want a panic that names AgentForkSession", r)
		}
	}()
	_ = WriteTypesJen(t.TempDir(), schema, meta)
}
