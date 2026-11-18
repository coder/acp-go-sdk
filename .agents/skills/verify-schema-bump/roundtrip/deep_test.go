package roundtrip

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The deep probes break a rule of the schema inside a hinted value, where the
// value still decodes into the SDK's Go type: a required member is absent, a
// closed enum or const holds a value that it does not list, or a member is
// null where its schema rejects null. The Rust types reject such a value, so
// the hint reads it as absent or drops the item, as it does for a value of
// the wrong type.
const (
	probeMissing = "missing-required"
	probeEnum    = "unknown-enum"
	probeNull    = "null-member"
)

// unknownValue replaces a string to break a closed enum or a const.
const unknownValue = "x-unknown-value"

// mutation is one deep probe: the change of class probe at path, the keys
// and indexes from the hinted value to the member.
type mutation struct {
	probe string
	path  []any
}

func (m mutation) String() string {
	var b strings.Builder
	for _, step := range m.path {
		fmt.Fprintf(&b, "/%v", step)
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

// mutations lists the deep probes of value, a value of the schema node. It
// does not look into a member whose own hint tolerates a bad value: that
// member reads as its default, and the value around it stays. Each probe is
// a candidate: only a probe that the schema rejects says something.
func mutations(defs map[string]any, node, value any) []mutation {
	var out []mutation
	var walk func(schemas []schemaRef, v any, path []any)
	walk = func(schemas []schemaRef, v any, path []any) {
		at := func(step any) []any { return append(path[:len(path):len(path)], step) }
		switch v := v.(type) {
		case map[string]any:
			nodes := expand(schemas, v)
			for _, k := range sortedKeys(v) {
				child, hints := memberSchemas(nodes, k)
				if hints["x-deserialize-default-on-error"] {
					continue
				}
				out = append(out, mutation{probeMissing, at(k)})
				if !hints["x-deserialize-default-on-null"] {
					out = append(out, mutation{probeNull, at(k)})
				}
				if hints["x-deserialize-skip-invalid-items"] {
					continue
				}
				walk(child, v[k], at(k))
			}
		case []any:
			var items []schemaRef
			for _, n := range expand(schemas, v) {
				if it, ok := n.node.(map[string]any)["items"]; ok {
					items = append(items, schemaRef{n.defs, it})
				}
			}
			for i, x := range v {
				out = append(out, mutation{probeNull, at(i)})
				walk(items, x, at(i))
			}
		case string:
			// An enum with the member "other" reads every string (see
			// readsEveryString), so no string breaks it.
			for _, n := range schemas {
				if readsEveryString(n.defs, n.node) {
					return
				}
			}
			out = append(out, mutation{probeEnum, path})
		}
	}
	walk([]schemaRef{{defs, node}}, value, nil)
	return out
}

// memberSchemas returns the schemas of member k in the object nodes, and the
// x-deserialize-* hints that any of them carries.
func memberSchemas(nodes []schemaRef, k string) ([]schemaRef, map[string]bool) {
	var child []schemaRef
	hints := map[string]bool{}
	for _, n := range nodes {
		m := n.node.(map[string]any)
		props, _ := m["properties"].(map[string]any)
		p, ok := props[k]
		if !ok {
			if ap, isSchema := m["additionalProperties"].(map[string]any); isSchema {
				p, ok = ap, true
			}
		}
		if !ok {
			continue
		}
		child = append(child, schemaRef{n.defs, p})
		pm, _ := p.(map[string]any)
		for _, hint := range hintNames {
			if pm[hint] == true {
				hints[hint] = true
			}
		}
	}
	return child, hints
}

// apply returns a copy of root with m applied. It copies only the objects
// and arrays on the path, so root is not changed.
func (m mutation) apply(root any) any {
	var set func(v any, path []any) any
	set = func(v any, path []any) any {
		if len(path) == 0 {
			if m.probe == probeEnum {
				return unknownValue
			}
			return nil
		}
		switch v := v.(type) {
		case map[string]any:
			k := path[0].(string)
			c := make(map[string]any, len(v))
			for key, x := range v {
				c[key] = x
			}
			if len(path) == 1 && m.probe == probeMissing {
				delete(c, k)
				return c
			}
			c[k] = set(v[k], path[1:])
			return c
		case []any:
			c := append([]any(nil), v...)
			i := path[0].(int)
			c[i] = set(v[i], path[1:])
			return c
		}
		panic(fmt.Sprintf("mutation %s %s: no member there", m.probe, m))
	}
	return set(root, m.path)
}

func mustDecode(t *testing.T, s string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestMutations_SkipMembersWhoseHintToleratesThem(t *testing.T) {
	defs := map[string]any{}
	mustDecode(t, `{
		"Entry": {"type": "object", "required": ["name", "kind"], "properties": {
			"name": {"type": "string"},
			"kind": {"$ref": "#/$defs/Kind"},
			"note": {"type": "string", "x-deserialize-default-on-error": true},
			"tags": {"type": "array", "items": {"type": "string"}, "x-deserialize-skip-invalid-items": true},
			"list": {"type": "array", "items": {"type": "string"}, "x-deserialize-default-on-null": true},
			"open": {"$ref": "#/$defs/Open"}}},
		"Kind": {"type": "string", "enum": ["a", "b"]},
		"Open": {"oneOf": [{"type": "string", "const": "a"}, {"type": "string", "const": "other"}]}}`, &defs)
	value := map[string]any{"name": "n", "kind": "a", "note": "x", "tags": []any{"t"}, "list": []any{"l"}, "open": "a"}
	var got []string
	for _, m := range mutations(defs, defs["Entry"], value) {
		got = append(got, m.probe+" "+m.String())
	}
	want := []string{
		"missing-required /kind", "null-member /kind", "unknown-enum /kind",
		"missing-required /list", "null-member /list/0", "unknown-enum /list/0",
		"missing-required /name", "null-member /name", "unknown-enum /name",
		"missing-required /open", "null-member /open",
		"missing-required /tags", "null-member /tags",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mutations = %q\nwant %q", got, want)
	}
}

func TestMutation_ApplyCopiesThePath(t *testing.T) {
	root := map[string]any{"a": []any{map[string]any{"b": "x", "c": "y"}}}
	for _, tc := range []struct {
		m    mutation
		want string
	}{
		{mutation{probeMissing, []any{"a", 0, "b"}}, `{"a":[{"c":"y"}]}`},
		{mutation{probeNull, []any{"a", 0}}, `{"a":[null]}`},
		{mutation{probeEnum, []any{"a", 0, "c"}}, `{"a":[{"b":"x","c":"x-unknown-value"}]}`},
	} {
		if got := string(mustJSON(t, tc.m.apply(root))); got != tc.want {
			t.Errorf("%s %s: apply = %s, want %s", tc.m.probe, tc.m, got, tc.want)
		}
	}
	if got := string(mustJSON(t, root)); got != `{"a":[{"b":"x","c":"y"}]}` {
		t.Errorf("apply changed its input: %s", got)
	}
}
