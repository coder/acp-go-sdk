// Command methodcheck checks that the SDK at -sdk wires every method in its
// schema/meta.json and schema/meta.unstable.json: each wire method needs a
// generated string constant, and the dispatcher on its side needs a case for
// it. Agent methods go to (*AgentSideConnection).handle, client methods to
// (*ClientSideConnection).handle, and a method listed for both sides needs
// both. Protocol methods ($/...) have no generated dispatcher, so they need a
// constant and a receive path in hand-written code: a function that decodes
// a message with json.Unmarshal into a variable x and compares x.Method with
// the wire string or its constant (==, != or a case of switch x.Method).
// Sending the method, or comparing any other value with it, does not count.
//
// It parses source only, so it works on code that does not build. It prints
// one MISSING line per gap and exits 1 when there is any.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type meta struct {
	AgentMethods    map[string]string `json:"agentMethods"`
	ClientMethods   map[string]string `json:"clientMethods"`
	ProtocolMethods map[string]string `json:"protocolMethods"`
}

// pkg is what the check needs from the SDK package source.
type pkg struct {
	consts    map[string]string          // constant name -> string value
	generated map[string]bool            // string values of constants in *_gen.go files
	cases     map[string]map[string]bool // dispatcher receiver -> wire methods with a case
	inbound   map[string]bool            // wire methods that a hand-written receive path compares a decoded message's Method with
}

func main() {
	sdk := flag.String("sdk", ".", "SDK checkout root")
	flag.Parse()
	missing, checked, err := check(*sdk)
	if err != nil {
		fmt.Fprintln(os.Stderr, "methodcheck:", err)
		os.Exit(2)
	}
	for _, line := range missing {
		fmt.Println(line)
	}
	fmt.Printf("methods: %d checked, %d missing\n", checked, len(missing))
	if len(missing) > 0 {
		os.Exit(1)
	}
}

// check returns one MISSING line per gap and the number of methods checked.
func check(sdk string) (missing []string, checked int, err error) {
	sides := map[string]map[string]bool{"agent": {}, "client": {}, "protocol": {}}
	for _, name := range []string{"meta.json", "meta.unstable.json"} {
		b, err := os.ReadFile(filepath.Join(sdk, "schema", name))
		if err != nil {
			return nil, 0, err
		}
		var m meta
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, 0, fmt.Errorf("%s: %w", name, err)
		}
		for side, methods := range map[string]map[string]string{"agent": m.AgentMethods, "client": m.ClientMethods, "protocol": m.ProtocolMethods} {
			for _, wire := range methods {
				sides[side][wire] = true
			}
		}
	}

	p, err := parsePackage(sdk)
	if err != nil {
		return nil, 0, err
	}
	dispatcher := map[string]string{"agent": "AgentSideConnection", "client": "ClientSideConnection"}
	for _, side := range []string{"agent", "client", "protocol"} {
		for _, wire := range sorted(sides[side]) {
			checked++
			if !p.generated[wire] {
				missing = append(missing, fmt.Sprintf("MISSING constant %s %s", side, wire))
			}
			if recv, ok := dispatcher[side]; ok && !p.cases[recv][wire] {
				missing = append(missing, fmt.Sprintf("MISSING case %s %s in (*%s).handle", side, wire, recv))
			}
			if side == "protocol" && !p.inbound[wire] {
				missing = append(missing, fmt.Sprintf("MISSING handler protocol %s in hand-written code", wire))
			}
		}
	}
	return missing, checked, nil
}

func parsePackage(dir string) (*pkg, error) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, err
		}
		files[filepath.Base(path)] = f
	}

	p := &pkg{consts: map[string]string{}, generated: map[string]bool{}, cases: map[string]map[string]bool{}, inbound: map[string]bool{}}
	for name, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if i < len(vs.Values) {
						if s, ok := stringLit(vs.Values[i]); ok {
							p.consts[id.Name] = s
							p.generated[s] = p.generated[s] || strings.HasSuffix(name, "_gen.go")
						}
					}
				}
			}
		}
	}
	value := func(e ast.Expr) (string, bool) {
		if s, ok := stringLit(e); ok {
			return s, true
		}
		if id, ok := e.(*ast.Ident); ok {
			s, ok := p.consts[id.Name]
			return s, ok
		}
		return "", false
	}

	for name, f := range files {
		generated := strings.HasSuffix(name, "_gen.go")
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "handle" && fd.Recv != nil && fd.Body != nil {
				recv := receiverType(fd.Recv.List[0].Type)
				if p.cases[recv] == nil {
					p.cases[recv] = map[string]bool{}
				}
				// Only cases of a switch on the handler's method parameter count.
				param := methodParam(fd.Type)
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					sw, ok := n.(*ast.SwitchStmt)
					if !ok {
						return true
					}
					if tag, ok := sw.Tag.(*ast.Ident); !ok || tag.Obj == nil || tag.Obj != param {
						return true
					}
					for _, stmt := range sw.Body.List {
						for _, e := range stmt.(*ast.CaseClause).List {
							if s, ok := value(e); ok {
								p.cases[recv][s] = true
							}
						}
					}
					return true
				})
			}
			if generated {
				continue
			}
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			decoded := decodedVars(fd.Body)
			// isMethod reports whether e is x.Method of a message x that an
			// earlier json.Unmarshal filled.
			isMethod := func(e ast.Expr) bool {
				sel, ok := e.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Method" {
					return false
				}
				x, ok := sel.X.(*ast.Ident)
				at, decodedX := decoded[x.Obj]
				return ok && x.Obj != nil && decodedX && at < sel.Pos()
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				var operands []ast.Expr
				switch n := n.(type) {
				case *ast.BinaryExpr:
					if n.Op != token.EQL && n.Op != token.NEQ {
						break
					}
					if isMethod(n.X) {
						operands = append(operands, n.Y)
					}
					if isMethod(n.Y) {
						operands = append(operands, n.X)
					}
				case *ast.SwitchStmt:
					if n.Tag != nil && isMethod(n.Tag) {
						for _, stmt := range n.Body.List {
							operands = append(operands, stmt.(*ast.CaseClause).List...)
						}
					}
				}
				for _, e := range operands {
					if s, ok := value(e); ok {
						p.inbound[s] = true
					}
				}
				return true
			})
		}
	}
	return p, nil
}

// decodedVars returns the variables that body fills with json.Unmarshal(b, &x),
// each with the position of that call. Variables are parser objects, so a
// shadowing variable of the same name is a different one.
func decodedVars(body *ast.BlockStmt) map[*ast.Object]token.Pos {
	vars := map[*ast.Object]token.Pos{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		fn, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || fn.Sel.Name != "Unmarshal" {
			return true
		}
		if pkg, ok := fn.X.(*ast.Ident); !ok || pkg.Name != "json" {
			return true
		}
		if addr, ok := call.Args[1].(*ast.UnaryExpr); ok && addr.Op == token.AND {
			if x, ok := addr.X.(*ast.Ident); ok && x.Obj != nil {
				vars[x.Obj] = call.Pos()
			}
		}
		return true
	})
	return vars
}

// methodParam is the parser object of the handler's first string
// parameter, the incoming method name, so a shadowing variable of the same
// name does not match it.
func methodParam(ft *ast.FuncType) *ast.Object {
	for _, field := range ft.Params.List {
		if id, ok := field.Type.(*ast.Ident); ok && id.Name == "string" && len(field.Names) > 0 {
			return field.Names[0].Obj
		}
	}
	return nil
}

func receiverType(e ast.Expr) string {
	if st, ok := e.(*ast.StarExpr); ok {
		e = st.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func stringLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	return s, err == nil
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
