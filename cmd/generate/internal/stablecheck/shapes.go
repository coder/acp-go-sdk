package stablecheck

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// rule is an entry of the shapes table that types_gen.go declares, or a
// node inside one: what a hinted value must do to fit, as the type shape of
// the hand-written shape.go reads it. A nil rule takes any value.
type rule struct {
	ref    string
	kinds  []string
	values []string
	// ints is the range of integers that fit, as "min..max"; "" for any.
	ints     string
	required []string
	props    map[string]*rule
	rest     *rule
	items    *rule
	not      *rule
	anyOf    []*rule
}

// shapeChanges compares each entry of old's shapes table with its version
// in new. The new version may only add to it: values, kinds, members, and
// alternatives, and its not rules may only take less. When a union of the
// schema gains a variant, the not rule of its catch-all variant may gain an
// alternative for the variant's discriminator value (claims). Any other
// change can make the decoder default or drop a stable value.
func shapeChanges(old, new *side) []string {
	oldEntries, err := old.shapes()
	if err != nil {
		return []string{"shapes: the stable table cannot be read: " + err.Error()}
	}
	newEntries, err := new.shapes()
	if err != nil {
		return []string{"shapes: the merged table cannot be read: " + err.Error()}
	}
	var out []string
	for _, key := range sortedKeys(oldEntries) {
		n, ok := newEntries[key]
		if !ok {
			out = append(out, fmt.Sprintf("shapes[%s]: removed", key))
			continue
		}
		if change := oldEntries[key].lostIn(n, "shapes["+key+"]"); change != "" {
			out = append(out, change+", so the decoder can default or drop a stable value")
		}
	}
	return out
}

// shapes reads the entries of the shapes table of s's generated files by
// their keys, the shape constants. A run without hints has no table. An
// entry of a form that rule does not model is an error, so that the check
// fails rather than skip it.
func (s *side) shapes() (map[string]*rule, error) {
	out := map[string]*rule{}
	spec := s.vars["shapes"]
	if spec == nil {
		return out, nil
	}
	var table *ast.CompositeLit
	if len(spec.Values) == 1 {
		table, _ = spec.Values[0].(*ast.CompositeLit)
	}
	if table == nil {
		return nil, fmt.Errorf("not a composite literal")
	}
	for _, elt := range table.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return nil, fmt.Errorf("an entry without a key: %s", types.ExprString(elt))
		}
		r, err := s.parseRule(kv.Value)
		if err != nil {
			return nil, err
		}
		out[types.ExprString(kv.Key)] = r
	}
	return out, nil
}

// parseRule reads a shape literal, such as {kinds: kindString, values:
// []any{"a"}}, or &shape{...}. nil, which a member that takes any value
// gets, is the nil rule. The bounds of ints are read as the constants that
// they evaluate to, such as math.MaxInt64.
func (s *side) parseRule(e ast.Expr) (*rule, error) {
	if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
		return nil, nil
	}
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("not a shape literal: %s", types.ExprString(e))
	}
	r := &rule{}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		field, isIdent := kv.Key.(*ast.Ident)
		if !ok || !isIdent {
			return nil, fmt.Errorf("not a shape field: %s", types.ExprString(elt))
		}
		var err error
		switch field.Name {
		case "ref":
			r.ref = types.ExprString(kv.Value)
		case "kinds":
			ast.Inspect(kv.Value, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					r.kinds = append(r.kinds, id.Name)
				}
				return true
			})
			sort.Strings(r.kinds)
		case "values":
			r.values = exprs(kv.Value)
		case "ints":
			r.ints, err = s.intRange(kv.Value)
		case "required":
			r.required = exprs(kv.Value)
			sort.Strings(r.required)
		case "props":
			r.props = map[string]*rule{}
			for _, p := range elements(kv.Value) {
				pkv, ok := p.(*ast.KeyValueExpr)
				if !ok {
					return nil, fmt.Errorf("not a member: %s", types.ExprString(p))
				}
				name, uerr := strconv.Unquote(types.ExprString(pkv.Key))
				if uerr != nil {
					return nil, uerr
				}
				if r.props[name], err = s.parseRule(pkv.Value); err != nil {
					return nil, err
				}
			}
		case "rest":
			r.rest, err = s.parseRule(kv.Value)
		case "items":
			r.items, err = s.parseRule(kv.Value)
		case "not":
			r.not, err = s.parseRule(kv.Value)
		case "anyOf":
			for _, alt := range elements(kv.Value) {
				a, err := s.parseRule(alt)
				if err != nil {
					return nil, err
				}
				r.anyOf = append(r.anyOf, a)
			}
		default:
			return nil, fmt.Errorf("unknown shape field %s", field.Name)
		}
		if err != nil {
			return nil, err
		}
	}
	return r, nil
}

// intRange reads &intRange{min, max} as "min..max".
func (s *side) intRange(e ast.Expr) (string, error) {
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	bounds := elements(e)
	if len(bounds) != 2 {
		return "", fmt.Errorf("not an integer range: %s", types.ExprString(e))
	}
	var out [2]string
	for i, b := range bounds {
		v := s.info.Types[b].Value
		if v == nil || v.Kind() != constant.Int {
			return "", fmt.Errorf("not an integer bound: %s", types.ExprString(b))
		}
		out[i] = v.ExactString()
	}
	return out[0] + ".." + out[1], nil
}

func elements(e ast.Expr) []ast.Expr {
	if lit, ok := e.(*ast.CompositeLit); ok {
		return lit.Elts
	}
	return nil
}

func exprs(e ast.Expr) []string {
	var out []string
	for _, elt := range elements(e) {
		out = append(out, types.ExprString(elt))
	}
	return out
}

// lostIn returns what r loses in n, its merged version, at path, or "" when
// n is r with additions: values beside the values of r, kinds beside the
// kinds of r, members, and alternatives.
func (r *rule) lostIn(n *rule, path string) string {
	switch {
	case r == nil && n == nil:
		return ""
	case r == nil:
		return path + ": gains rules"
	case n == nil:
		return path + ": loses its rules"
	case r.ref != n.ref:
		return fmt.Sprintf("%s: refers to %q instead of %q", path, n.ref, r.ref)
	case (r.kinds == nil) != (n.kinds == nil) || slices.ContainsFunc(r.kinds, func(k string) bool { return !slices.Contains(n.kinds, k) }):
		return fmt.Sprintf("%s: takes the kinds %v instead of %v", path, n.kinds, r.kinds)
	case r.ints != n.ints:
		return fmt.Sprintf("%s: takes the integers %s instead of %s", path, rangeText(n.ints), rangeText(r.ints))
	case !slices.Equal(r.required, n.required):
		return fmt.Sprintf("%s: requires %v instead of %v", path, n.required, r.required)
	case r.values != nil && n.values == nil:
		return path + ": takes any value"
	case r.values == nil && n.values != nil:
		return fmt.Sprintf("%s: takes only the values %v", path, n.values)
	}
	for _, v := range r.values {
		if !slices.Contains(n.values, v) {
			return fmt.Sprintf("%s: loses the value %s", path, v)
		}
	}
	for _, name := range sortedKeys(r.props) {
		np, ok := n.props[name]
		if !ok {
			return fmt.Sprintf("%s: loses the member %s", path, name)
		}
		if change := r.props[name].lostIn(np, path+"."+name); change != "" {
			return change
		}
	}
	for _, sub := range []struct {
		name string
		r, n *rule
	}{{"rest", r.rest, n.rest}, {"items", r.items, n.items}} {
		if change := sub.r.lostIn(sub.n, path+"."+sub.name); change != "" {
			return change
		}
	}
	// A value fits a rule only if it does not fit its not rule, so the
	// merged not rule may take only values that the stable one takes.
	switch {
	case n.not == nil:
	case r.not == nil:
		return path + ".not: is new"
	case n.not.lostIn(r.not, "") != "":
		return path + ".not: takes values that the stable rule does not"
	}
	if (r.anyOf == nil) != (n.anyOf == nil) {
		return path + ": gains or loses its alternatives"
	}
	// Each alternative of r gets a merged version that takes what it takes.
	used := map[*rule]bool{}
	pair := func(alt *rule, relax func(*rule) *rule) bool {
		j := slices.IndexFunc(n.anyOf, func(b *rule) bool { return !used[b] && alt.lostIn(relax(b), "") == "" })
		if j >= 0 {
			used[n.anyOf[j]] = true
		}
		return j >= 0
	}
	var left []int
	for i, alt := range r.anyOf {
		if !pair(alt, func(b *rule) *rule { return b }) {
			left = append(left, i)
		}
	}
	// The merged alternatives without a version and without a not rule are
	// new, such as a new variant. The not rule of a catch-all may leave out
	// the values that they claim.
	var added []*rule
	for _, b := range n.anyOf {
		if !used[b] && b.not == nil {
			added = append(added, b)
		}
	}
	for _, i := range left {
		alt := r.anyOf[i]
		if !pair(alt, func(b *rule) *rule { return b.withoutClaimedExclusions(alt, added) }) {
			return fmt.Sprintf("%s.anyOf[%d]: has no merged version", path, i)
		}
	}
	return ""
}

func rangeText(ints string) string {
	if ints == "" {
		return "any integer"
	}
	return ints
}

// withoutClaimedExclusions returns b, an alternative of a union, without the
// alternatives of its not rule that the not rule of alt, its stable version,
// has no version of and that an alternative of added claims. A catch-all
// variant gains them when its union gains a variant: the new variant claims
// the values that the catch-all then leaves out.
func (b *rule) withoutClaimedExclusions(alt *rule, added []*rule) *rule {
	if b.not == nil || alt.not == nil || b.not.anyOf == nil || alt.not.anyOf == nil {
		return b
	}
	not := *b.not
	not.anyOf = slices.DeleteFunc(slices.Clone(not.anyOf), func(x *rule) bool {
		return !slices.ContainsFunc(alt.not.anyOf, func(y *rule) bool { return x.lostIn(y, "") == "" }) &&
			slices.ContainsFunc(added, func(a *rule) bool { return a.claims(x) })
	})
	out := *b
	out.not = &not
	return &out
}

// claims reports whether x takes only objects that a pins by a member:
// objects whose member m, which x requires, holds one of the values that the
// rule of a for m lists. A variant pins its discriminator so, and the not
// rule of a catch-all leaves out the discriminator values of the variants.
func (a *rule) claims(x *rule) bool {
	if !slices.Equal(x.kinds, []string{"kindObject"}) {
		return false
	}
	return slices.ContainsFunc(x.required, func(quoted string) bool {
		m, _ := strconv.Unquote(quoted)
		xm, am := x.props[m], a.props[m]
		return xm != nil && am != nil && xm.values != nil &&
			!slices.ContainsFunc(xm.values, func(v string) bool { return !slices.Contains(am.values, v) })
	})
}

// siteChanges checks each site where the merged code checks a stable value
// against an entry of the shapes table that only it declares, a check that
// bodyChanges leaves out of the comparison: the entry, but for its rules on
// the members that the stable Go type does not have, must take every value
// that the Go type decodes. A stable struct gets such a check when it gains
// a member that cannot be null.
func siteChanges(old, new *side, sites []site) []string {
	entries, err := new.shapes()
	if err != nil {
		return nil // shapeChanges reports it
	}
	var out []string
	for _, s := range sites {
		r, ok := entries[s.shape]
		reason := "is not in the table"
		if ok {
			reason = takesAll{old: old, entries: entries, refs: map[string]bool{}}.check(r, s.t, false)
		}
		if reason != "" {
			out = append(out, fmt.Sprintf("%s: checks a stable value against the new %s, which %s", s.fn, s.shape, reason))
		}
	}
	return out
}

// takesAll checks that a rule takes every value of a Go type. entries holds
// the merged shapes table, and refs the entries on the path to the rule.
type takesAll struct {
	old     *side
	entries map[string]*rule
	refs    map[string]bool
}

// check returns why r does not take every value that the Go type t decodes,
// or "". A member that the stable version of a struct type does not have
// does not count. member says that the value is a member or an item, which
// may be null; the decoder itself rejects a null value at a site, even of a
// pointer type. A type
// that decodes itself, other than an object struct, takes no rule.
func (c takesAll) check(r *rule, t types.Type, member bool) string {
	if r == nil {
		return ""
	}
	if r.ref != "" && !c.refs[r.ref] {
		ref, ok := c.entries[r.ref]
		if !ok {
			return "refers to " + r.ref + ", which is not in the table"
		}
		c.refs[r.ref] = true
		reason := c.check(ref, t, member)
		delete(c.refs, r.ref)
		if reason != "" {
			return reason
		}
	}
	switch {
	case r.values != nil:
		return fmt.Sprintf("takes only the values %v", r.values)
	case r.ints != "":
		return "takes only the integers " + r.ints
	case r.required != nil:
		return fmt.Sprintf("requires %v", r.required)
	case r.not != nil:
		return "has a not rule"
	case r.anyOf != nil && !slices.ContainsFunc(r.anyOf, func(alt *rule) bool { return c.check(alt, t, member) == "" }):
		return "takes only the values of its alternatives"
	}
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		t = ptr.Elem()
	}
	kinds, known := goKinds(t)
	if member {
		kinds = append(kinds, "kindNull")
	}
	if !known && (r.kinds != nil || r.props != nil || r.rest != nil || r.items != nil) {
		return "has rules for " + types.TypeString(t, nil) + ", which decodes itself"
	}
	if r.kinds != nil && slices.ContainsFunc(kinds, func(k string) bool { return !slices.Contains(r.kinds, k) }) {
		return fmt.Sprintf("takes the kinds %v, and %s takes %v", r.kinds, types.TypeString(t, nil), kinds)
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		named, _ := types.Unalias(t).(*types.Named)
		var stable *types.Struct
		if named != nil {
			stable = structOf(c.old, named.Obj().Name())
		}
		for _, name := range sortedKeys(r.props) {
			field := fieldByJSONName(u, name)
			switch {
			case r.props[name] == nil:
			case field == nil:
				return fmt.Sprintf("has a rule for the member %s, which %s does not decode", name, types.TypeString(t, nil))
			case stable != nil && !hasField(stable, field.Name()):
				// A member that the merged schema adds.
			default:
				if reason := c.check(r.props[name], field.Type(), true); reason != "" {
					return fmt.Sprintf("has a rule for the stable member %s that %s", name, reason)
				}
			}
		}
		if r.rest != nil || r.items != nil {
			return "has a rule for the members of an object that " + types.TypeString(t, nil) + " does not decode"
		}
	case *types.Map:
		for _, name := range sortedKeys(r.props) {
			if reason := c.check(r.props[name], u.Elem(), true); reason != "" {
				return fmt.Sprintf("has a rule for the member %s that %s", name, reason)
			}
		}
		if reason := c.check(r.rest, u.Elem(), true); reason != "" {
			return "has a rule for its members that " + reason
		}
		if r.items != nil {
			return "has a rule for items"
		}
	case *types.Slice:
		if reason := c.check(r.items, u.Elem(), true); reason != "" {
			return "has a rule for its items that " + reason
		}
		if r.props != nil || r.rest != nil {
			return "has a rule for members"
		}
	default:
		if r.props != nil || r.rest != nil || r.items != nil {
			return "has a rule for members or items"
		}
	}
	return ""
}

// goKinds returns the kinds of JSON value that the Go type t decodes, and
// whether they are known: a named type with its own UnmarshalJSON decodes
// what it decodes, unless it is a struct of members, which decodes objects.
func goKinds(t types.Type) ([]string, bool) {
	all := []string{"kindNull", "kindBoolean", "kindInteger", "kindFraction", "kindString", "kindArray", "kindObject"}
	if named, ok := types.Unalias(t).(*types.Named); ok {
		if obj := named.Obj(); obj.Pkg() != nil && obj.Pkg().Path() == "encoding/json" && obj.Name() == "RawMessage" {
			return all, true
		}
		if m, _, _ := types.LookupFieldOrMethod(types.NewPointer(named), true, named.Obj().Pkg(), "UnmarshalJSON"); m != nil {
			st, ok := named.Underlying().(*types.Struct)
			if !ok || st.NumFields() == 0 || fieldByJSONName(st, "-") != nil {
				return nil, false
			}
			return []string{"kindObject"}, true
		}
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch {
		case u.Info()&types.IsString != 0:
			return []string{"kindString"}, true
		case u.Info()&types.IsBoolean != 0:
			return []string{"kindBoolean"}, true
		case u.Info()&types.IsInteger != 0:
			return []string{"kindInteger"}, true
		case u.Info()&types.IsFloat != 0:
			return []string{"kindInteger", "kindFraction"}, true
		}
	case *types.Slice:
		return []string{"kindArray"}, true
	case *types.Map, *types.Struct:
		return []string{"kindObject"}, true
	case *types.Interface:
		return all, true
	}
	return nil, false
}

// fieldByJSONName returns the field of st whose json tag names name, or
// that has no json tag and is called name.
func fieldByJSONName(st *types.Struct, name string) *types.Var {
	for i := 0; i < st.NumFields(); i++ {
		tag, ok := reflect.StructTag(st.Tag(i)).Lookup("json")
		key, _, _ := strings.Cut(tag, ",")
		if !ok || key == "" {
			key = st.Field(i).Name()
		}
		if key == name {
			return st.Field(i)
		}
	}
	return nil
}
