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
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
//     cases and their values, local struct fields, and composite literal
//     elements that only refer to what merged adds: new fields of the
//     receiver type and new package-level names (newProjection);
//   - a method that merged adds to a stable type and that refers to a stable
//     name, such as a Validate that checks a stable property;
//   - a field tag that changes, such as one that gains omitempty;
//   - an entry of the shapes table that loses a rule (shapeChanges);
//   - a call in a stable function that checks a stable value against an
//     entry of the shapes table that only merged declares, and that takes
//     fewer values than the Go type (siteChanges);
//   - in an UnmarshalJSON method, such an added statement that can return
//     with a nil error before code for a stable field, which can then decode
//     a stable value as the new one. A return in a literal case of a switch
//     with a tag, such as a switch on a discriminator, is exempt: the case
//     takes only its own values.
//
// A JSON literal passed with a pointer, such as a default, may gain members
// for the fields that merged adds to the pointed-to type. Both bodies first
// lose their statements without effect (pruneInert), and a call of a method
// that merged adds and that does nothing (noopMethods) counts as added. A
// return of a call that gets an added field, which a stable value leaves
// empty, is a return of what the call then returns (emptyAdded), and both
// bodies fold the error check after a json.Marshal (foldMarshal).
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
	changes = append(changes, tagChanges(old, new)...)
	bodies, sites := bodyChanges(old, new)
	changes = append(changes, bodies...)
	changes = append(changes, shapeChanges(old, new)...)
	changes = append(changes, siteChanges(old, new, sites)...)
	if len(changes) > 0 {
		return fmt.Errorf("cannot merge unstable schema: the merged schema changes stable Go types:\n\t%s", strings.Join(changes, "\n\t"))
	}
	return nil
}

// side is the acp package with the generated files of one run.
type side struct {
	pkg  *types.Package
	info *types.Info
	// funcs holds the functions and methods of the generated files, by
	// name: "T.M" for the method M of T.
	funcs map[string]*ast.FuncDecl
	// declared holds the package-level names that the generated files
	// declare.
	declared map[string]bool
	// vars holds the package-level variables of the generated files.
	vars map[string]*ast.ValueSpec
}

// typecheck type-checks the package of the hand-written files in pkgDir and
// the generated files in genDir. Type errors do not stop it: the
// hand-written code may refer to what only the merged files declare.
func typecheck(fset *token.FileSet, imp types.Importer, pkgDir, genDir string) (*side, error) {
	s := &side{funcs: map[string]*ast.FuncDecl{}, declared: map[string]bool{}, vars: map[string]*ast.ValueSpec{}}
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
	s.info = &types.Info{
		Types:     map[ast.Expr]types.TypeAndValue{},
		Defs:      map[*ast.Ident]types.Object{},
		Uses:      map[*ast.Ident]types.Object{},
		Instances: map[*ast.Ident]types.Instance{},
	}
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
						if decl.Tok == token.VAR {
							s.vars[name.Name] = spec
						}
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
// under name, and whether there is one.
func (s *side) fields(name string) (map[string]bool, bool) {
	obj, ok := s.pkg.Scope().Lookup(name).(*types.TypeName)
	if !ok {
		return nil, false
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return nil, false
	}
	out := map[string]bool{}
	for i := 0; i < st.NumFields(); i++ {
		out[st.Field(i).Name()] = true
	}
	return out, true
}

// tagChanges compares the tag of each field of each struct type that old's
// generated files declare with its version in new: apidiff does not read
// tags, and a json tag that gains omitempty drops a zero value from the wire.
func tagChanges(old, new *side) []string {
	var out []string
	for _, name := range sortedKeys(old.declared) {
		ost, nst := structOf(old, name), structOf(new, name)
		if ost == nil || nst == nil {
			continue
		}
		for i := 0; i < ost.NumFields(); i++ {
			for j := 0; j < nst.NumFields(); j++ {
				if f := ost.Field(i); f.Name() == nst.Field(j).Name() && ost.Tag(i) != nst.Tag(j) {
					out = append(out, fmt.Sprintf("%s.%s: changes its tag from `%s` to `%s`", name, f.Name(), ost.Tag(i), nst.Tag(j)))
				}
			}
		}
	}
	return out
}

func structOf(s *side, name string) *types.Struct {
	if obj, ok := s.pkg.Scope().Lookup(name).(*types.TypeName); ok {
		st, _ := obj.Type().Underlying().(*types.Struct)
		return st
	}
	return nil
}

// bodyChanges compares the body of each function and method of old's
// generated files that is not a method of a hand-written type with its
// version in new, and checks each method that new adds to a type that old
// declares (see Compare). It returns the sites where new checks a stable
// value against a new entry of the shapes table, for siteChanges.
func bodyChanges(old, new *side) ([]string, []site) {
	var out []string
	var sites []site
	noops := noopMethods(old, new)
	for _, name := range sortedKeys(old.funcs) {
		recv, _, isMethod := strings.Cut(name, ".")
		if isMethod && !old.declared[recv] || fastDecoder(name) {
			continue
		}
		of, nf := old.funcs[name], new.funcs[name]
		if nf == nil {
			if !ast.IsExported(of.Name.Name) {
				out = append(out, name+": removed")
			}
			continue
		}
		p := newProjection(old, new, recv, isMethod)
		p.noops = noops
		body := p.block(nf.Body)
		p.acceptRejected(of.Body)
		if strings.HasSuffix(name, ".UnmarshalJSON") {
			out = append(out, p.earlyReturns(name, body)...)
		}
		for _, s := range p.sites {
			s.fn = name
			sites = append(sites, s)
		}
		old.pruneInert(of.Body)
		new.pruneInert(body)
		old.foldMarshal(of.Body)
		new.foldMarshal(body)
		old.canonicalJSON(of.Body, nil)
		new.canonicalJSON(body, old.pkg)
		if s, m, ok := firstDifference(old.text(of.Body), new.text(body)); ok {
			out = append(out, fmt.Sprintf("%s: changes; stable has %q where merged has %q", name, s, m))
		}
	}
	// A method that new adds to a stable type, such as an UnmarshalJSON for
	// a hint, may only handle what new adds.
	for _, name := range sortedKeys(new.funcs) {
		recv, _, isMethod := strings.Cut(name, ".")
		if !isMethod || !old.declared[recv] || old.funcs[name] != nil || fastDecoder(name) {
			continue
		}
		p := newProjection(old, new, recv, true)
		if stable := p.firstStable(p.block(new.funcs[name].Body)); stable != "" {
			out = append(out, fmt.Sprintf("%s: is new and refers to the stable %s", name, stable))
		}
	}
	return out, sites
}

// fastDecoder reports whether name is the decodeFast of a type, which
// bodyChanges does not compare: it decodes a value as the exact decoder of
// its type does, or declines and leaves the value to that decoder
// (decode_fast.go of the acp package). So what a stable caller can observe
// changes only with the exact decoder, whose body bodyChanges compares.
func fastDecoder(name string) bool { return strings.HasSuffix(name, ".decodeFast") }

// noopMethods returns the methods that new adds to the types that old
// declares and that do nothing once projected: their body is empty or only
// returns nil, such as a Validate that only checks the members that new
// adds. A call of one does nothing for a stable value (projection.refers).
func noopMethods(old, new *side) map[*types.Func]bool {
	out := map[*types.Func]bool{}
	for _, name := range sortedKeys(new.funcs) {
		recv, _, isMethod := strings.Cut(name, ".")
		if !isMethod || !old.declared[recv] || old.funcs[name] != nil {
			continue
		}
		decl := new.funcs[name]
		body := newProjection(old, new, recv, true).block(decl.Body)
		new.pruneInert(body)
		if f, ok := new.info.Defs[decl.Name].(*types.Func); ok && returnsOnlyNil(body) {
			out[f] = true
		}
	}
	return out
}

// returnsOnlyNil reports whether body is empty or is one return of nil
// values.
func returnsOnlyNil(body *ast.BlockStmt) bool {
	if len(body.List) == 0 {
		return true
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(body.List) > 1 {
		return false
	}
	for _, r := range ret.Results {
		if id, ok := r.(*ast.Ident); !ok || id.Name != "nil" {
			return false
		}
	}
	return true
}

// newProjection returns the projection for a function, or for a method of
// recv, in a body of new. The stable names are the fields of a struct
// receiver, else the package-level names of old's generated files, such as
// the constants of an enum. The added names are the fields that new adds to
// the receiver and the package-level names that only new declares.
func newProjection(old, new *side, recv string, isMethod bool) *projection {
	p := &projection{
		info: new.info, scope: new.pkg.Scope(), funcs: new.funcs,
		stable: old.declared, added: map[string]bool{}, addedFields: map[string]bool{},
		addedMembers: map[string]bool{}, guards: map[string]*ast.IfStmt{},
	}
	if stable, ok := old.fields(recv); isMethod && ok {
		p.stable, p.stableFields = nil, stable
		fields, _ := new.fields(recv)
		for field := range fields {
			if !stable[field] {
				p.addedFields[field] = true
			}
		}
		addedMembers(old, new, recv, p.addedMembers, map[string]bool{})
		// A call names the properties of the receiver, such as the check
		// for a repeated one: a stable property keeps its name there,
		// though a field of another type that merged adds has it too.
		if st := structOf(old, recv); st != nil {
			for i := 0; i < st.NumFields(); i++ {
				json, _, _ := strings.Cut(reflect.StructTag(st.Tag(i)).Get("json"), ",")
				delete(p.addedMembers, strconv.Quote(json))
			}
		}
	}
	for _, n := range new.pkg.Scope().Names() {
		if old.pkg.Scope().Lookup(n) == nil {
			p.added[n] = true
		}
	}
	return p
}

// addedMembers adds to out the JSON names, quoted, of the fields that new
// adds to the struct type name and to the stable struct types of its fields,
// at any depth, such as a member of a scope that a catch-all variant holds.
func addedMembers(old, new *side, name string, out, seen map[string]bool) {
	st, stable := structOf(new, name), structOf(old, name)
	if st == nil || stable == nil || seen[name] {
		return
	}
	seen[name] = true
	for i := 0; i < st.NumFields(); i++ {
		field := st.Field(i)
		if json, _, _ := strings.Cut(reflect.StructTag(st.Tag(i)).Get("json"), ","); !hasField(stable, field.Name()) && json != "" && json != "-" {
			out[strconv.Quote(json)] = true
		}
		if named := heldNamed(field.Type()); named != nil && named.Obj().Pkg() == new.pkg {
			addedMembers(old, new, named.Obj().Name(), out, seen)
		}
	}
}

// heldNamed returns the named type that t is or holds through pointers,
// slices, and maps, or nil.
func heldNamed(t types.Type) *types.Named {
	for {
		switch u := types.Unalias(t).(type) {
		case *types.Pointer:
			t = u.Elem()
		case *types.Slice:
			t = u.Elem()
		case *types.Map:
			t = u.Elem()
		case *types.Named:
			return u
		default:
			return nil
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// projection removes from a body of the merged files what refers to names in
// added and to none in stable. It records the removed statements that can
// return with a nil error.
type projection struct {
	info  *types.Info
	scope *types.Scope
	// funcs holds the functions and methods of the merged generated files.
	funcs map[string]*ast.FuncDecl
	// stable and added hold package-level names, stableFields and
	// addedFields the names of the fields of a struct receiver.
	stable, added             map[string]bool
	stableFields, addedFields map[string]bool
	// addedMembers holds the quoted JSON names of addedMembers, such as the
	// names that additionalProperties leaves out of a catch-all's map.
	addedMembers map[string]bool
	// noops holds the methods of noopMethods.
	noops map[*types.Func]bool
	early []ast.Node
	// guards holds the removed if statements without init or else, by the
	// text of their condition.
	guards map[string]*ast.IfStmt
	// values holds, for each local that a switch with a tag switches on,
	// the literal values of the cases that refer only to added names, such
	// as the discriminator value of a new variant.
	values map[types.Object]map[string]bool
	// sites holds the calls that lost a new shapes entry.
	sites []site
}

// site is a call in a stable function that checks a value of the Go type t
// against shape, an entry of the shapes table that only new declares, such
// as decodeLenient(raw.Caps, &v.Caps, shapeCaps) once Caps gains a member
// that cannot be null.
type site struct {
	fn, shape string
	t         types.Type
}

// acceptRejected lets a removed if statement take the place of a stable one
// with its condition that only returns an error, as when a union gains a null
// alternative: the merged code then accepts only values that the stable code
// rejected. It removes those stable statements from old, the stable body, and
// drops the removed ones from the early returns.
func (p *projection) acceptRejected(old *ast.BlockStmt) {
	ast.Inspect(old, func(n ast.Node) bool {
		b, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		b.List = slices.DeleteFunc(b.List, func(s ast.Stmt) bool {
			guard, ok := s.(*ast.IfStmt)
			if !ok || guard.Init != nil || guard.Else != nil || len(guard.Body.List) != 1 {
				return false
			}
			ret, ok := guard.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) == 0 || returnsNil(ret) {
				return false
			}
			merged := p.guards[types.ExprString(guard.Cond)]
			if merged == nil {
				return false
			}
			p.early = slices.DeleteFunc(p.early, func(e ast.Node) bool { return e == merged })
			return true
		})
		return true
	})
}

// refers reports whether n names something added, and something stable. A
// call of a method of noops counts as added, and its receiver as neither.
func (p *projection) refers(n ast.Node) (added, stable bool) {
	ast.Inspect(n, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if f, ok := p.info.Uses[sel.Sel].(*types.Func); ok && p.noops[f] {
				added = true
				return false
			}
		}
		if id, ok := n.(*ast.Ident); ok {
			a, s := p.names(id)
			added, stable = added || a, stable || s
		}
		return true
	})
	return added, stable
}

// names reports whether id stands for an added name and for a stable one,
// by the object that it denotes: a field, of the receiver or of a local
// struct type that mirrors it, stands only for a field name, a
// package-level object only for a package-level name, and a local object,
// such as the type Alias of a MarshalJSON, for neither. An identifier that
// the type checker did not resolve stands for what it spells.
func (p *projection) names(id *ast.Ident) (added, stable bool) {
	obj := p.info.Uses[id]
	if obj == nil {
		obj = p.info.Defs[id]
	}
	field, isVar := obj.(*types.Var)
	switch {
	case obj == nil:
		return p.added[id.Name] || p.addedFields[id.Name], p.stable[id.Name] || p.stableFields[id.Name]
	case isVar && field.IsField():
		return p.addedFields[id.Name], p.stableFields[id.Name]
	case obj.Parent() == p.scope:
		return p.added[id.Name], p.stable[id.Name]
	}
	return false, false
}

// block removes from b, at any depth, what refers only to added names: a
// statement, a switch case, a field of a local struct type, or an element of
// a composite literal. A return of a call that a stable value makes return
// at once goes first to what it then returns (emptyAdded).
func (p *projection) block(b *ast.BlockStmt) *ast.BlockStmt {
	p.emptyAdded(b)
	p.values = p.addedValues(b)
	ast.Inspect(b, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SwitchStmt:
			n.Body.List = filter(p, n.Body.List, n.Tag != nil)
			// Another switch on the same local, such as the one with which a
			// catch-all variant leaves out the discriminator values of the
			// other variants, loses the added values too.
			if values := p.values[p.local(n.Tag)]; values != nil {
				n.Body.List = slices.DeleteFunc(n.Body.List, func(s ast.Stmt) bool {
					c := s.(*ast.CaseClause)
					if !literalCase(c) {
						return false
					}
					c.List = slices.DeleteFunc(c.List, func(e ast.Expr) bool { return values[e.(*ast.BasicLit).Value] })
					return len(c.List) == 0
				})
			}
		case *ast.CallExpr:
			if s, ok := p.newCheck(n); ok {
				n.Args = n.Args[:len(n.Args)-1]
				p.sites = append(p.sites, s)
			}
			n.Args = slices.DeleteFunc(n.Args, func(arg ast.Expr) bool {
				lit, ok := arg.(*ast.BasicLit)
				return ok && lit.Kind == token.STRING && p.addedMembers[lit.Value]
			})
		case *ast.BlockStmt:
			n.List = filter(p, n.List, false)
		case *ast.CaseClause:
			// A case may list stable and added values together.
			if list := filter(p, n.List, false); len(list) > 0 {
				n.List = list
			}
			n.Body = filter(p, n.Body, false)
		case *ast.StructType:
			n.Fields.List = filter(p, n.Fields.List, false)
		case *ast.CompositeLit:
			n.Elts = filter(p, n.Elts, false)
		}
		return true
	})
	return b
}

// addedValues returns, for each local that a switch with a tag in b switches
// on, the values of its literal cases that refer only to added names.
func (p *projection) addedValues(b *ast.BlockStmt) map[types.Object]map[string]bool {
	out := map[types.Object]map[string]bool{}
	ast.Inspect(b, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		local := p.local(sw.Tag)
		if local == nil {
			return true
		}
		for _, c := range sw.Body.List {
			clause := c.(*ast.CaseClause)
			if added, stable := p.refers(clause); !added || stable || !literalCase(clause) {
				continue
			}
			if out[local] == nil {
				out[local] = map[string]bool{}
			}
			for _, e := range clause.List {
				out[local][e.(*ast.BasicLit).Value] = true
			}
		}
		return true
	})
	return out
}

// local returns the local variable that e names, or nil.
func (p *projection) local(e ast.Expr) types.Object {
	id, ok := e.(*ast.Ident)
	if !ok {
		return nil
	}
	if v, ok := p.info.Uses[id].(*types.Var); ok && !v.IsField() && v.Parent() != p.scope {
		return v
	}
	return nil
}

// newCheck reports whether the last argument of call is a constant of the
// shapes table that only new declares, and returns its site: the value that
// the call checks has the Go type of the call's one type argument, such as T
// of decodeLenient[T].
func (p *projection) newCheck(call *ast.CallExpr) (site, bool) {
	if len(call.Args) == 0 {
		return site{}, false
	}
	id, ok := call.Args[len(call.Args)-1].(*ast.Ident)
	if !ok {
		return site{}, false
	}
	c, ok := p.info.Uses[id].(*types.Const)
	if !ok || c.Parent() != p.scope || !p.added[c.Name()] {
		return site{}, false
	}
	if named, ok := c.Type().(*types.Named); !ok || named.Obj().Name() != "shapeID" {
		return site{}, false
	}
	fn, ok := call.Fun.(*ast.Ident)
	if !ok {
		return site{}, false
	}
	inst, ok := p.info.Instances[fn]
	if !ok || inst.TypeArgs.Len() != 1 {
		return site{}, false
	}
	return site{shape: c.Name(), t: inst.TypeArgs.At(0)}, true
}

// filter keeps the elements of list that do not refer only to added names.
// tagged says that list holds the cases of a switch with a tag.
func filter[T ast.Node](p *projection, list []T, tagged bool) []T {
	var out []T
	for _, n := range list {
		p.elements(n)
		if added, stable := p.refers(n); !added || stable {
			out = append(out, n)
			continue
		}
		if clause, ok := ast.Node(n).(*ast.CaseClause); (!tagged || !ok || !literalCase(clause)) && returnsNil(n) {
			p.early = append(p.early, n)
		}
		if guard, ok := ast.Node(n).(*ast.IfStmt); ok && guard.Init == nil && guard.Else == nil {
			p.guards[types.ExprString(guard.Cond)] = guard
		}
	}
	return out
}

// elements removes from n the struct fields and composite literal elements
// that refer only to added names, outside the blocks, cases, and function
// literals in n, so that a statement that holds them, such as the
// declaration of a local struct type that mirrors the receiver, is judged by
// what is left.
func (p *projection) elements(n ast.Node) {
	ast.Inspect(n, func(m ast.Node) bool {
		switch m := m.(type) {
		case *ast.BlockStmt, *ast.CaseClause, *ast.FuncLit:
			return false
		case *ast.StructType:
			m.Fields.List = filter(p, m.Fields.List, false)
		case *ast.CompositeLit:
			m.Elts = filter(p, m.Elts, false)
		}
		return true
	})
}

// literalCase reports whether c is a case of literal values, which a switch
// with a tag takes only for values equal to them.
func literalCase(c *ast.CaseClause) bool {
	for _, e := range c.List {
		if _, ok := e.(*ast.BasicLit); !ok {
			return false
		}
	}
	return len(c.List) > 0
}

// returnsNil reports whether n has a return statement whose last result is
// nil, outside function literals and the literal cases of switches with a
// tag, such as a switch on a discriminator.
func returnsNil(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.SwitchStmt:
			if n.Tag != nil {
				for _, c := range n.Body.List {
					if clause := c.(*ast.CaseClause); !literalCase(clause) && returnsNil(clause) {
						found = true
					}
				}
				return false
			}
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
			if id, ok := m.(*ast.Ident); ok && later == "" && id.Pos() > n.Pos() {
				if _, stable := p.names(id); stable {
					later = id.Name
				}
			}
			return later == ""
		})
		if later != "" {
			out = append(out, fmt.Sprintf("%s: tries the new %s before the stable %s, so a stable value can decode as the new one", name, p.firstAdded(n), later))
		}
	}
	return out
}

// firstStable names the first stable name that n refers to, or "".
func (p *projection) firstStable(n ast.Node) string {
	name := ""
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && name == "" {
			if _, stable := p.names(id); stable {
				name = id.Name
			}
		}
		return name == ""
	})
	return name
}

// firstAdded names the first new field of the receiver type that n refers
// to, else the first new name.
func (p *projection) firstAdded(n ast.Node) string {
	field, name := "", ""
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if added, _ := p.names(id); added {
				if field == "" && p.addedFields[id.Name] {
					field = id.Name
				}
				if name == "" {
					name = id.Name
				}
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
// statements a projection removed leave no trace. Tokens keep their text,
// such as the spaces in a string literal, and one space stands for the
// spaces between two tokens.
func (s *side) text(n ast.Node) []string {
	var buf bytes.Buffer
	// An empty file set leaves out the line breaks of the source, such as
	// those between the elements of a composite literal that lost some.
	if err := (&printer.Config{Mode: printer.RawFormat}).Fprint(&buf, token.NewFileSet(), n); err != nil {
		return []string{err.Error()}
	}
	src := buf.Bytes()
	file := token.NewFileSet().AddFile("", -1, len(src))
	var sc scanner.Scanner
	sc.Init(file, src, nil, 0)
	var lines []string
	var line strings.Builder
	lineNo, end := 0, 0
	for {
		pos, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON && lit == "\n" {
			continue
		}
		if lit == "" {
			lit = tok.String()
		}
		switch offset := file.Offset(pos); {
		case file.Line(pos) != lineNo:
			if line.Len() > 0 {
				lines = append(lines, line.String())
			}
			line.Reset()
			lineNo = file.Line(pos)
		case offset > end:
			line.WriteByte(' ')
		}
		line.WriteString(lit)
		end = file.Offset(pos) + len(lit)
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
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
