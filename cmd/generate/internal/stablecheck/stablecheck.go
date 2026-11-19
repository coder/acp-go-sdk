// Package stablecheck checks that merging the unstable schema keeps every
// stable Go type: the generator writes the acp package twice, from the stable
// schema alone and from the merged one, and compares what the first declares.
package stablecheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/exp/apidiff"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/emit"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// Run is the input of one generator run.
type Run struct {
	Meta   *load.Meta
	Schema *load.Schema
}

// Compare writes the generated files of stable, the stable schema alone, and
// of merged, the stable and unstable schemas merged, and type-checks each
// set with the hand-written files of pkgDir. It returns an error that lists
// each change to what the stable files declare:
//   - an API change other than an addition (apidiff), such as a field that
//     is removed or changes its type, or a named type whose integer type
//     widens, which apidiff calls compatible;
//   - a function or method whose body changes beyond the statements, switch
//     cases, and local struct fields that only refer to what merged adds: its
//     new fields of the receiver type and its new package-level names;
//   - in an UnmarshalJSON method, such an added statement that can return
//     with a nil error before code for a stable field, which can then decode
//     a stable value as the new one. A case of a switch whose stable cases
//     stay is exempt: the cases compare distinct constants.
//
// A JSON literal passed with a pointer, such as a default, may gain members
// for the fields that merged adds to the pointed-to type.
func Compare(pkgDir, schemaVersion string, stable, merged Run) error {
	dir, err := os.MkdirTemp("", "stablecheck")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "source", nil)
	var sides [2]*side
	for i, run := range []Run{stable, merged} {
		out := filepath.Join(dir, strconv.Itoa(i))
		if err := os.Mkdir(out, 0o755); err != nil {
			return err
		}
		if err := emit.WriteFiles(out, run.Schema, run.Meta, schemaVersion); err != nil {
			return err
		}
		if sides[i], err = typecheck(fset, imp, pkgDir, out); err != nil {
			return err
		}
	}
	old, new := sides[0], sides[1]
	var changes []string
	for _, c := range apidiff.Changes(old.pkg, new.pkg).Changes {
		if !strings.HasSuffix(c.Message, ": added") && old.declared[subject(c.Message)] {
			changes = append(changes, c.Message)
		}
	}
	changes = append(changes, bodyChanges(old, new)...)
	if len(changes) > 0 {
		return fmt.Errorf("cannot merge unstable schema: the merged schema changes stable Go types:\n\t%s", strings.Join(changes, "\n\t"))
	}
	return nil
}

// side is the acp package with the generated files of one run.
type side struct {
	fset *token.FileSet
	pkg  *types.Package
	info *types.Info
	// funcs holds the functions and methods of the generated files, by
	// name: "T.M" for the method M of T.
	funcs map[string]*ast.FuncDecl
	// declared holds the package-level names that the generated files
	// declare.
	declared map[string]bool
}

// typecheck type-checks the package of the hand-written files in pkgDir and
// the generated files in genDir. Type errors do not stop it: the
// hand-written code may refer to what only the merged files declare.
func typecheck(fset *token.FileSet, imp types.Importer, pkgDir, genDir string) (*side, error) {
	s := &side{fset: fset, funcs: map[string]*ast.FuncDecl{}, declared: map[string]bool{}}
	hand, err := filepath.Glob(filepath.Join(pkgDir, "*.go"))
	if err != nil {
		return nil, err
	}
	generated, err := filepath.Glob(filepath.Join(genDir, "*_gen.go"))
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, path := range append(hand, generated...) {
		if strings.HasSuffix(path, "_test.go") || filepath.Dir(path) == filepath.Clean(pkgDir) && strings.HasSuffix(path, "_gen.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
		if filepath.Dir(path) == filepath.Clean(genDir) {
			s.index(f)
		}
	}
	s.info = &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{Importer: imp, Error: func(error) {}}
	s.pkg, _ = conf.Check("acp", fset, files, s.info)
	return s, nil
}

func (s *side) index(f *ast.File) {
	for _, decl := range f.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Recv == nil {
				s.declared[decl.Name.Name] = true
			}
			s.funcs[funcName(decl)] = decl
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					s.declared[spec.Name.Name] = true
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						s.declared[name.Name] = true
					}
				}
			}
		}
	}
}

// funcName names a function, or a method M of T or *T as "T.M".
func funcName(f *ast.FuncDecl) string {
	if f.Recv == nil || len(f.Recv.List) == 0 {
		return f.Name.Name
	}
	t := f.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + f.Name.Name
	}
	return f.Name.Name
}

// subject returns the package-level name that an apidiff message is about:
// T for "T.F: removed" and "(*T).M: removed".
func subject(msg string) string {
	s := strings.TrimLeft(msg, "(*")
	if i := strings.IndexAny(s, ".):, "); i >= 0 {
		return s[:i]
	}
	return s
}

// fields returns the field names of the struct type that side declares
// under name.
func (s *side) fields(name string) map[string]bool {
	out := map[string]bool{}
	if obj, ok := s.pkg.Scope().Lookup(name).(*types.TypeName); ok {
		if st, ok := obj.Type().Underlying().(*types.Struct); ok {
			for i := 0; i < st.NumFields(); i++ {
				out[st.Field(i).Name()] = true
			}
		}
	}
	return out
}

// bodyChanges compares the body of each function and method of old's
// generated files that is not a method of a hand-written type with its
// version in new (see Compare).
func bodyChanges(old, new *side) []string {
	names := make([]string, 0, len(old.funcs))
	for name := range old.funcs {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		recv, _, isMethod := strings.Cut(name, ".")
		if isMethod && !old.declared[recv] {
			continue
		}
		of, nf := old.funcs[name], new.funcs[name]
		if nf == nil {
			if !ast.IsExported(of.Name.Name) {
				out = append(out, name+": removed")
			}
			continue
		}
		p := projection{stable: map[string]bool{}, added: map[string]bool{}, addedFields: map[string]bool{}}
		if isMethod {
			p.stable = old.fields(recv)
			for field := range new.fields(recv) {
				p.added[field] = !p.stable[field]
				p.addedFields[field] = !p.stable[field]
			}
		}
		for _, n := range new.pkg.Scope().Names() {
			if old.pkg.Scope().Lookup(n) == nil {
				p.added[n] = true
			}
		}
		body := p.block(nf.Body)
		if strings.HasSuffix(name, ".UnmarshalJSON") {
			out = append(out, p.earlyReturns(name, body)...)
		}
		old.canonicalJSON(of.Body, nil)
		new.canonicalJSON(body, old.pkg)
		if s, m, ok := firstDifference(old.text(of.Body), new.text(body)); ok {
			out = append(out, fmt.Sprintf("%s: changes; stable has %q where merged has %q", name, s, m))
		}
	}
	return out
}

// projection removes from a body of the merged files the statements, switch
// cases, and local struct fields that refer to names in added and to none in
// stable. It records those that can return with a nil error.
type projection struct {
	stable, added map[string]bool
	// addedFields holds the fields of the receiver type in added.
	addedFields map[string]bool
	early       []ast.Node
}

// refers reports whether n names something in added, and something in
// stable.
func (p *projection) refers(n ast.Node) (added, stable bool) {
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			added = added || p.added[id.Name]
			stable = stable || p.stable[id.Name]
		}
		return true
	})
	return added, stable
}

func (p *projection) block(b *ast.BlockStmt) *ast.BlockStmt {
	p.walk(b)
	return b
}

func (p *projection) walk(n ast.Node) {
	switch n := n.(type) {
	case *ast.BlockStmt:
		n.List = filter(p, n.List, false)
	case *ast.IfStmt:
		p.walk(n.Body)
		if n.Else != nil {
			p.walk(n.Else)
		}
	case *ast.ForStmt:
		p.walk(n.Body)
	case *ast.RangeStmt:
		p.walk(n.Body)
	case *ast.LabeledStmt:
		p.walk(n.Stmt)
	case *ast.SwitchStmt:
		n.Body.List = filter(p, n.Body.List, true)
	case *ast.TypeSwitchStmt:
		n.Body.List = filter(p, n.Body.List, true)
	case *ast.CaseClause:
		n.Body = filter(p, n.Body, false)
	case *ast.DeclStmt:
		for _, spec := range n.Decl.(*ast.GenDecl).Specs {
			switch spec := spec.(type) {
			case *ast.TypeSpec:
				p.walk(spec.Type)
			case *ast.ValueSpec:
				p.walk(spec.Type)
			}
		}
	case *ast.StructType:
		n.Fields.List = filter(p, n.Fields.List, false)
	}
}

// filter keeps the elements of list that do not refer only to added names,
// and walks into those that refer to both. cases says that list holds the
// cases of a switch.
func filter[T ast.Node](p *projection, list []T, cases bool) []T {
	var out []T
	for _, n := range list {
		added, stable := p.refers(n)
		switch {
		case !added:
			out = append(out, n)
		case stable:
			p.walk(n)
			out = append(out, n)
		default:
			if clause, ok := ast.Node(n).(*ast.CaseClause); (!cases || !ok || clause.List == nil) && returnsNil(n) {
				p.early = append(p.early, n)
			}
		}
	}
	return out
}

// returnsNil reports whether n has a return statement whose last result is
// nil, outside function literals.
func returnsNil(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(n.Results) > 0 {
				if id, ok := n.Results[len(n.Results)-1].(*ast.Ident); ok && id.Name == "nil" {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// earlyReturns names each removed statement that can end the method named
// name with a nil error before body, the projected body, refers to a stable
// field.
func (p *projection) earlyReturns(name string, body *ast.BlockStmt) []string {
	var out []string
	for _, n := range p.early {
		var later string
		ast.Inspect(body, func(m ast.Node) bool {
			if id, ok := m.(*ast.Ident); ok && later == "" && p.stable[id.Name] && id.Pos() > n.Pos() {
				later = id.Name
			}
			return later == ""
		})
		if later != "" {
			out = append(out, fmt.Sprintf("%s: tries the new %s before the stable %s, so a stable value can decode as the new one", name, p.firstAdded(n), later))
		}
	}
	return out
}

// firstAdded names the first new field of the receiver type that n refers
// to, else the first new name.
func (p *projection) firstAdded(n ast.Node) string {
	field, name := "", ""
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && p.added[id.Name] {
			if field == "" && p.addedFields[id.Name] {
				field = id.Name
			}
			if name == "" {
				name = id.Name
			}
		}
		return field == ""
	})
	if field != "" {
		return field
	}
	return name
}

// canonicalJSON rewrites each string literal in body that holds a JSON
// object or array and is passed to a call with a pointer, such as a default,
// in one form. With old set, it first removes the members for the fields of
// the pointed-to type that old's version of the type does not have.
func (s *side) canonicalJSON(body *ast.BlockStmt, old *types.Package) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var target types.Type
		for _, arg := range call.Args {
			if ptr, ok := s.info.TypeOf(arg).(*types.Pointer); ok && target == nil {
				target = ptr.Elem()
			}
		}
		for _, arg := range call.Args {
			lit, ok := arg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[") {
				continue
			}
			var v any
			if json.Unmarshal([]byte(text), &v) != nil {
				continue
			}
			if old != nil && target != nil {
				prune(old, v, target)
			}
			b, _ := json.Marshal(v)
			lit.Value = strconv.Quote(string(b))
		}
		return true
	})
}

// prune deletes from v, a JSON value of the merged Go type t, each member
// for a field of a struct type that old's version of the type does not have.
func prune(old *types.Package, v any, t types.Type) {
	switch t := types.Unalias(t).(type) {
	case *types.Pointer:
		prune(old, v, t.Elem())
	case *types.Slice:
		if items, ok := v.([]any); ok {
			for _, item := range items {
				prune(old, item, t.Elem())
			}
		}
	case *types.Map:
		if members, ok := v.(map[string]any); ok {
			for _, member := range members {
				prune(old, member, t.Elem())
			}
		}
	case *types.Named:
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			prune(old, v, t.Underlying())
			return
		}
		members, isObject := v.(map[string]any)
		stable, _ := old.Scope().Lookup(t.Obj().Name()).(*types.TypeName)
		if !isObject || stable == nil {
			return
		}
		stableStruct, _ := stable.Type().Underlying().(*types.Struct)
		for i := 0; i < st.NumFields(); i++ {
			field := st.Field(i)
			name, _, _ := strings.Cut(reflect.StructTag(st.Tag(i)).Get("json"), ",")
			if name == "" {
				name = field.Name()
			}
			member, ok := members[name]
			switch {
			case !ok:
			case stableStruct == nil || !hasField(stableStruct, field.Name()):
				delete(members, name)
			default:
				prune(old, member, field.Type())
			}
		}
	}
}

func hasField(st *types.Struct, name string) bool {
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Name() == name {
			return true
		}
	}
	return false
}

// text prints n as lines without indentation or empty lines, so that the
// statements a projection removed leave no trace.
func (s *side) text(n ast.Node) []string {
	var buf bytes.Buffer
	if err := (&printer.Config{Mode: printer.RawFormat}).Fprint(&buf, s.fset, n); err != nil {
		return []string{err.Error()}
	}
	var lines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// firstDifference returns the first line where a and b differ, or "" for
// the one that ends first.
func firstDifference(a, b []string) (string, string, bool) {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x, y, true
		}
	}
	return "", "", false
}
