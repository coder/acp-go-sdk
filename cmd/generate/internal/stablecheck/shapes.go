package stablecheck

import (
	"fmt"
	"go/ast"
	"go/types"
	"slices"
	"sort"
	"strconv"
)

// rule is an entry of the shapes table that types_gen.go declares, or a
// node inside one: what a hinted value must do to fit, as the type shape of
// the hand-written shape.go reads it. A nil rule takes any value.
type rule struct {
	ref      string
	kinds    []string
	values   []string
	required []string
	props    map[string]*rule
	rest     *rule
	items    *rule
	not      *rule
	anyOf    []*rule
}

// shapeChanges compares each entry of old's shapes table with its version
// in new. The new version may only add to it: values, members, and
// alternatives, and its not rules may only take less. When a union of the
// schema gains a variant, the not rule of its catch-all variant gains an
// alternative for the variant's discriminator value. Any other change can
// make the decoder default or drop a stable value.
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
		r, err := parseRule(kv.Value)
		if err != nil {
			return nil, err
		}
		out[types.ExprString(kv.Key)] = r
	}
	return out, nil
}

// parseRule reads a shape literal, such as {kinds: kindString, values:
// []any{"a"}}, or &shape{...}. nil, which a member that takes any value
// gets, is the nil rule.
func parseRule(e ast.Expr) (*rule, error) {
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
				if r.props[name], err = parseRule(pkv.Value); err != nil {
					return nil, err
				}
			}
		case "rest":
			r.rest, err = parseRule(kv.Value)
		case "items":
			r.items, err = parseRule(kv.Value)
		case "not":
			r.not, err = parseRule(kv.Value)
		case "anyOf":
			for _, alt := range elements(kv.Value) {
				a, err := parseRule(alt)
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
// n is r with additions: values beside the values of r, members, and
// alternatives.
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
	case !slices.Equal(r.kinds, n.kinds):
		return fmt.Sprintf("%s: takes the kinds %v instead of %v", path, n.kinds, r.kinds)
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
	grows := len(n.anyOf) > len(r.anyOf)
	used := map[*rule]bool{}
	for i, alt := range r.anyOf {
		j := slices.IndexFunc(n.anyOf, func(b *rule) bool {
			if used[b] {
				return false
			}
			if grows {
				b = b.withoutNewExclusions(alt)
			}
			return alt.lostIn(b, "") == ""
		})
		if j < 0 {
			return fmt.Sprintf("%s.anyOf[%d]: has no merged version", path, i)
		}
		used[n.anyOf[j]] = true
	}
	return ""
}

// withoutNewExclusions returns b, an alternative of a union that gains
// alternatives, without the alternatives of its not rule that the not rule
// of alt, its stable version, has no version of. A catch-all variant gains
// them when its union gains a variant: the new variant takes the values
// that the catch-all then leaves out.
func (b *rule) withoutNewExclusions(alt *rule) *rule {
	if b.not == nil || alt.not == nil || b.not.anyOf == nil || alt.not.anyOf == nil {
		return b
	}
	not := *b.not
	not.anyOf = slices.DeleteFunc(slices.Clone(not.anyOf), func(x *rule) bool {
		return !slices.ContainsFunc(alt.not.anyOf, func(y *rule) bool { return x.lostIn(y, "") == "" })
	})
	out := *b
	out.not = &not
	return &out
}
