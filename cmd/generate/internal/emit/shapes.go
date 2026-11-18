package emit

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/ir"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// shapeNode is a node of the table shapes that types_gen.go declares, as the
// generator builds it: the rules of a schema node that the Go types do not
// check. The type shape in the root package's shape.go reads it at run time,
// and its comment says what a value must do to fit.
type shapeNode struct {
	ref      string // a $defs entry
	kinds    []string
	values   []any
	required []string
	props    map[string]*shapeNode
	rest     *shapeNode
	items    *shapeNode
	anyOf    []*shapeNode
	not      *shapeNode
}

// jsonKinds maps a JSON Schema type to the jsonKind constants of shape.go.
var jsonKinds = map[string][]string{
	"null":    {"kindNull"},
	"boolean": {"kindBoolean"},
	"integer": {"kindInteger"},
	"number":  {"kindInteger", "kindFraction"},
	"string":  {"kindString"},
	"array":   {"kindArray"},
	"object":  {"kindObject"},
}

// shapeTable collects the shapes that the lenient decoders of types_gen.go
// name: the $defs entries that a hinted property reaches, and one shape for
// each hinted property whose schema is not a $ref.
type shapeTable struct {
	schema *load.Schema
	// defs holds the node of each $defs entry that node has translated.
	defs map[string]*shapeNode
	// sites maps the name of each shapeID constant that a decoder names to
	// its node: a $defs entry, or the schema of one property.
	sites map[string]*shapeNode
	// strictDefs holds the $defs entries whose node is strict.
	strictDefs map[string]bool
}

func newShapeTable(schema *load.Schema) *shapeTable {
	return &shapeTable{schema: schema, defs: map[string]*shapeNode{}, sites: map[string]*shapeNode{}, strictDefs: map[string]bool{}}
}

// site returns the shapeID constant that the decoder of field, a property of
// the struct owner with x-deserialize-default-on-error or
// x-deserialize-skip-invalid-items, checks a decoded value against: the
// whole value, or each item with x-deserialize-skip-invalid-items. It
// returns nil when the shape can reject no value that the Go type accepts.
// The decoder reads null as a bad value itself, so the shape need not.
func (t *shapeTable) site(owner string, field ir.Field, goType Code) Code {
	def := field.Def
	if field.Lenient.SkipInvalidItems {
		def, goType = def.Items, jenTypeFor(def.Items)
	}
	if def == nil {
		return nil
	}
	n := t.node(withoutNull(def))
	if !t.strict(n) && !n.untypedRules(fmt.Sprintf("%#v", goType)) {
		return nil
	}
	if n.onlyRef() {
		t.sites[shapeConst(n.ref)] = t.defs[n.ref]
		return Id(shapeConst(n.ref))
	}
	// A $defs entry or another property can have the name already.
	name := "shape" + owner + field.Name
	for i := 2; t.sites[name] != nil || t.schema.Defs[strings.TrimPrefix(name, "shape")] != nil; i++ {
		name = fmt.Sprintf("shape%s%s%d", owner, field.Name, i)
	}
	t.sites[name] = n
	return Id(name)
}

// withoutNull returns the one alternative of d that is not null, when d is
// an anyOf or oneOf of it and null, and d otherwise.
func withoutNull(d *load.Definition) *load.Definition {
	alts := d.AnyOf
	if len(alts) == 0 {
		alts = d.OneOf
	}
	var other *load.Definition
	for _, alt := range alts {
		switch {
		case alt != nil && alt.Type == "null":
		case other != nil:
			return d
		default:
			other = alt
		}
	}
	if other == nil {
		return d
	}
	return other
}

// untypedRules reports whether n has a rule that goType, the Go type of the
// decoded value, does not check because it holds any value: raw JSON, or a
// map or slice of it (Meta is such a map). Such a type checks only that a map
// is an object and a slice is an array.
func (n *shapeNode) untypedRules(goType string) bool {
	untyped := goType == "Meta" || strings.Contains(goType, "RawMessage")
	if n == nil || !untyped {
		return false
	}
	kinds := []string{"kindNull"}
	switch {
	case strings.HasPrefix(goType, "map[") || goType == "Meta":
		kinds = append(kinds, "kindObject")
	case strings.HasPrefix(goType, "[]"):
		kinds = append(kinds, "kindArray")
	}
	return !n.onlyScalar() || n.values != nil || slices.ContainsFunc(n.kinds, func(k string) bool { return !slices.Contains(kinds, k) })
}

func shapeConst(def string) string { return "shape" + def }

// node translates the schema d into a shape node, and each $defs entry that
// it refers to. A nil node fits every value.
func (t *shapeTable) node(d *load.Definition) *shapeNode {
	if d == nil {
		return nil
	}
	n := &shapeNode{}
	ref := d.Ref
	for _, part := range d.AllOf {
		// Every allOf from schema 0.13.5 through 1.32.0 wraps one $ref.
		// Another part adds no rule, so the shape fits more values.
		if ref == "" && part != nil && part.Ref != "" {
			ref = part.Ref
		}
	}
	if ref != "" {
		n.ref = t.def(strings.TrimPrefix(ref, "#/$defs/"))
	}
	n.kinds = kindsOf(d.Type)
	switch {
	case d.Const != nil:
		n.values = scalars([]any{d.Const})
	case len(d.Enum) > 0 && !hasOtherMember(d):
		n.values = scalars(d.Enum)
	}
	n.required = d.Required
	n.rest = t.node(d.AdditionalProperties.Schema)
	for _, k := range sortedPropertyKeys(d.Properties) {
		// A property that takes every value stays named while rest has a
		// rule, so that rest does not apply to it.
		if m := t.member(d.Properties[k]); m != nil || n.rest != nil {
			if n.props == nil {
				n.props = map[string]*shapeNode{}
			}
			n.props[k] = m
		}
	}
	n.items = t.node(d.Items)
	alts := d.AnyOf
	if len(alts) == 0 {
		alts = d.OneOf
	}
	if len(alts) > 0 {
		t.alternatives(n, d, alts)
	}
	n.not = t.node(d.Not)
	if n.empty() {
		return nil
	}
	return n
}

// alternatives sets the rules of n that the alternatives alts make. When
// every alternative is a const, n takes their values. When one alternative
// takes every value of the kinds of the others, as in an open enum, n takes
// those kinds. An enum with the member "other" takes every string: the Rust
// types read an unknown value as that member. Otherwise n takes anyOf.
func (t *shapeTable) alternatives(n *shapeNode, d *load.Definition, alts []*load.Definition) {
	nodes := make([]*shapeNode, 0, len(alts))
	for _, alt := range alts {
		an := t.node(alt)
		if an == nil {
			return // an alternative that takes every value
		}
		nodes = append(nodes, an)
	}
	if !slices.ContainsFunc(nodes, func(an *shapeNode) bool { return !an.onlyScalar() }) {
		var values []any
		var kinds, open []string // the kinds of all alternatives, and of those without values
		for _, an := range nodes {
			values = append(values, an.values...)
			kinds = append(kinds, an.kinds...)
			if an.values == nil {
				open = append(open, an.kinds...)
			}
		}
		// covered reports whether a value of an takes a kind that open
		// takes every value of.
		covered := func(an *shapeNode) bool {
			return an.values == nil || len(an.kinds) > 0 && !slices.ContainsFunc(an.kinds, func(k string) bool { return !slices.Contains(open, k) })
		}
		switch {
		case len(open) == 0 && hasOtherMember(d):
			n.kinds = intersectKinds(n.kinds, kinds)
			return
		case len(open) == 0:
			n.values = intersectValues(n.values, values)
			return
		case !slices.ContainsFunc(nodes, func(an *shapeNode) bool { return !covered(an) }):
			n.kinds = intersectKinds(n.kinds, open)
			return
		}
	}
	n.anyOf = nodes
}

// scalars returns values when each is a string, a number, a boolean or
// null, and nil otherwise: the walker compares values with ==, which panics
// on an array or an object, so such an enum or const adds no rule.
func scalars(values []any) []any {
	for _, v := range values {
		switch v.(type) {
		case string, float64, bool, nil:
		default:
			return nil
		}
	}
	return values
}

// intersectValues returns the values in both a and b, or b when a lists
// none. A value fits a node only when it fits all of the node's rules.
func intersectValues(a, b []any) []any {
	if a == nil {
		return b
	}
	both := []any{}
	for _, v := range a {
		if slices.Contains(b, v) {
			both = append(both, v)
		}
	}
	return both
}

// intersectKinds returns the kinds in both a and b, or b when a lists none.
// When they share no kind, it returns a: the shape then fits more values
// than the schema, never fewer.
func intersectKinds(a, b []string) []string {
	if len(a) == 0 {
		return mergeKinds(nil, b)
	}
	var both []string
	for _, k := range a {
		if slices.Contains(b, k) {
			both = append(both, k)
		}
	}
	if len(both) == 0 {
		return a
	}
	return mergeKinds(nil, both)
}

// member returns the node of a property, changed by its hints as the Rust
// types read it: a property with x-deserialize-default-on-error takes every
// value, one with x-deserialize-skip-invalid-items takes every item, and
// one with x-deserialize-default-on-null takes null.
func (t *shapeTable) member(d *load.Definition) *shapeNode {
	if d == nil || d.DefaultOnError {
		return nil
	}
	n := t.node(d)
	if n != nil && d.SkipInvalidItems {
		c := *n
		c.items = nil
		n = &c
		if n.empty() {
			return nil
		}
	}
	if n != nil && d.DefaultOnNull {
		n = &shapeNode{anyOf: []*shapeNode{{kinds: []string{"kindNull"}}, n}}
	}
	return n
}

// def translates the $defs entry name, once, and returns name.
func (t *shapeTable) def(name string) string {
	if _, ok := t.defs[name]; !ok {
		t.defs[name] = nil // a $ref back to name, while it is translated
		t.defs[name] = t.node(t.schema.Defs[name])
	}
	return name
}

func (n *shapeNode) empty() bool {
	return n.ref == "" && len(n.kinds) == 0 && n.values == nil && n.required == nil && n.props == nil &&
		n.rest == nil && n.items == nil && n.anyOf == nil && n.not == nil
}

// onlyRef reports whether n has no rule but its ref.
func (n *shapeNode) onlyRef() bool {
	c := *n
	c.ref = ""
	return n.ref != "" && c.empty()
}

// onlyScalar reports whether n has no rule but its kinds and values.
func (n *shapeNode) onlyScalar() bool {
	c := *n
	c.kinds, c.values = nil, nil
	return c.empty()
}

// strict reports whether a value that n describes and that its Go type
// decodes can still miss n: a required member is absent, a value is not one
// of the listed values, a member or item is null where n rejects null, or
// the value fits none of several alternatives other than null. A value of
// the wrong kind does not count: its Go type does not decode it.
func (t *shapeTable) strict(n *shapeNode) bool {
	t.settleStrictDefs()
	return t.strictNode(n)
}

// settleStrictDefs sets strictDefs for each translated $defs entry. A $defs
// entry is strict when its node is, given the entries it refers to, so the
// loop runs until no entry changes: a cycle of $refs adds no rule.
func (t *shapeTable) settleStrictDefs() {
	for changed := true; changed; {
		changed = false
		for name, n := range t.defs {
			if !t.strictDefs[name] && t.strictNode(n) {
				t.strictDefs[name] = true
				changed = true
			}
		}
	}
}

// strictNode is strict with strictDefs as it stands.
func (t *shapeTable) strictNode(n *shapeNode) bool {
	if n == nil {
		return false
	}
	if n.ref != "" && t.strictDefs[n.ref] {
		return true
	}
	alts := slices.DeleteFunc(slices.Clone(n.anyOf), func(alt *shapeNode) bool {
		return slices.Equal(alt.kinds, []string{"kindNull"}) && alt.onlyScalar() && alt.values == nil
	})
	if n.values != nil || n.required != nil || n.not != nil || len(alts) > 1 {
		return true
	}
	members := []*shapeNode{n.rest, n.items}
	for _, k := range sortedKeys(n.props) {
		members = append(members, n.props[k])
	}
	for _, m := range members {
		if m != nil && (!t.fitsNull(m) || t.strictNode(m)) {
			return true
		}
	}
	return slices.ContainsFunc(alts, t.strictNode)
}

// fitsNull reports whether null fits n.
func (t *shapeTable) fitsNull(n *shapeNode) bool {
	switch {
	case n == nil:
		return true
	case n.ref != "" && !t.fitsNull(t.defs[n.ref]):
		return false
	case len(n.kinds) > 0 && !slices.Contains(n.kinds, "kindNull"):
		return false
	case n.values != nil && !slices.Contains(n.values, nil):
		return false
	case n.anyOf != nil && !slices.ContainsFunc(n.anyOf, t.fitsNull):
		return false
	}
	return n.not == nil || !t.fitsNull(n.not)
}

func kindsOf(typ any) []string {
	var names []string
	switch typ := typ.(type) {
	case string:
		names = []string{typ}
	case []any:
		for _, x := range typ {
			if s, ok := x.(string); ok {
				names = append(names, s)
			}
		}
	}
	var kinds []string
	for _, name := range names {
		kinds = append(kinds, jsonKinds[name]...)
	}
	return mergeKinds(nil, kinds)
}

func mergeKinds(a, b []string) []string {
	kinds := append(append([]string(nil), a...), b...)
	sort.Strings(kinds)
	return slices.Compact(kinds)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// emit declares the shapeID constants and the table shapes: the sites, and
// the $defs entries that they refer to. It declares an empty table when no
// decoder names a shape, so that shape.go compiles.
func (t *shapeTable) emit(f *File) {
	nodes := map[string]*shapeNode{}
	var add func(name string, n *shapeNode)
	add = func(name string, n *shapeNode) {
		if _, ok := nodes[name]; ok {
			return
		}
		nodes[name] = n
		t.visitRefs(n, func(def string) { add(shapeConst(def), t.defs[def]) })
	}
	for _, name := range sortedKeys(t.sites) {
		add(name, t.sites[name])
	}
	names := sortedKeys(nodes)
	f.Comment("The shapes that the lenient decoders check a value against. See shape.go.")
	if len(names) > 0 {
		f.Const().DefsFunc(func(g *Group) {
			for i, name := range names {
				if i == 0 {
					g.Id(name).Id("shapeID").Op("=").Iota().Op("+").Lit(1)
				} else {
					g.Id(name)
				}
			}
		})
		f.Line()
	}
	f.Var().Id("shapes").Op("=").Index(Op("...")).Id("shape").Values(DictFunc(func(d Dict) {
		for _, name := range names {
			if n := nodes[name]; n != nil {
				d[Id(name)] = t.render(n)
			}
		}
	}))
	f.Line()
}

// visitRefs calls fn with each $defs entry that n refers to.
func (t *shapeTable) visitRefs(n *shapeNode, fn func(def string)) {
	if n == nil {
		return
	}
	if n.ref != "" {
		fn(n.ref)
	}
	for _, k := range sortedKeys(n.props) {
		t.visitRefs(n.props[k], fn)
	}
	for _, m := range append([]*shapeNode{n.rest, n.items, n.not}, n.anyOf...) {
		t.visitRefs(m, fn)
	}
}

// render returns the composite literal of n, without its type.
func (t *shapeTable) render(n *shapeNode) Code {
	d := Dict{}
	if n.ref != "" {
		d[Id("ref")] = Id(shapeConst(n.ref))
	}
	if len(n.kinds) > 0 {
		kinds := Id(n.kinds[0])
		for _, kind := range n.kinds[1:] {
			kinds = kinds.Op("|").Id(kind)
		}
		d[Id("kinds")] = kinds
	}
	if n.values != nil {
		values := []Code{}
		for _, v := range n.values {
			values = append(values, literal(v))
		}
		d[Id("values")] = Index().Any().Values(values...)
	}
	if n.required != nil {
		required := []Code{}
		for _, r := range n.required {
			required = append(required, Lit(r))
		}
		d[Id("required")] = Index().String().Values(required...)
	}
	if n.props != nil {
		d[Id("props")] = Map(String()).Op("*").Id("shape").Values(DictFunc(func(props Dict) {
			for k, m := range n.props {
				if m == nil {
					props[Lit(k)] = Nil()
				} else {
					props[Lit(k)] = t.render(m)
				}
			}
		}))
	}
	for _, field := range []struct {
		name string
		node *shapeNode
	}{{"rest", n.rest}, {"items", n.items}, {"not", n.not}} {
		if field.node != nil {
			d[Id(field.name)] = Op("&").Id("shape").Add(t.render(field.node))
		}
	}
	if n.anyOf != nil {
		alts := []Code{}
		for _, alt := range n.anyOf {
			alts = append(alts, t.render(alt))
		}
		d[Id("anyOf")] = Index().Op("*").Id("shape").Values(alts...)
	}
	return Values(d)
}

// literal returns a const or enum value as it reads from JSON with
// UseNumber: a number as a json.Number.
func literal(v any) Code {
	switch v := v.(type) {
	case float64:
		b, _ := json.Marshal(v)
		return Qual("encoding/json", "Number").Call(Lit(string(b)))
	case nil:
		return Nil()
	}
	return Lit(v)
}
