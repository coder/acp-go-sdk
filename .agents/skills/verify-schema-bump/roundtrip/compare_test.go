package roundtrip

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
)

// schemaRef is a schema node and the $defs that its $ref values point into.
type schemaRef struct {
	defs map[string]any
	node any
}

// defRefs returns $defs/def of each file of files that has it.
func defRefs(def string, files []*schemaFile) []schemaRef {
	var refs []schemaRef
	for _, f := range files {
		if node, ok := f.defs[def]; ok {
			refs = append(refs, schemaRef{f.defs, node})
		}
	}
	return refs
}

// comparison is the outcome of comparing an input with the SDK's output.
type comparison struct {
	lost    []string // "path: in=... out=..." for each differing path
	unknown []string // differing paths at or under an input member that the schema does not define
}

// compareJSON normalizes both documents against schemas and compares them.
// Normalizing drops an object member that is null or equals its property's
// schema default: the SDK, like the Rust SDK's #[serde(default)], reads an
// absent member as its default and may write the default back. Numbers
// compare by exact value (1.0 equals 1; no digit is rounded away). A
// difference at or under an input member that no schema node defines goes
// to unknown rather than lost: a field newer than the schema version is not
// the SDK's to keep.
func compareJSON(in, out []byte, schemas []schemaRef) (comparison, error) {
	a, err := decodeExact(in)
	if err != nil {
		return comparison{}, fmt.Errorf("input: %w", err)
	}
	b, err := decodeExact(out)
	if err != nil {
		return comparison{}, fmt.Errorf("output: %w", err)
	}
	var unknownIn, unknownOut, diffs []string
	walkDiff("", normalize(a, schemas, "", &unknownIn), normalize(b, schemas, "", &unknownOut), &diffs)
	var c comparison
	for _, d := range diffs {
		path, _, _ := strings.Cut(d, ": in=")
		if under(path, unknownIn) {
			c.unknown = append(c.unknown, path)
		} else {
			c.lost = append(c.lost, d)
		}
	}
	return c, nil
}

// describe lists every differing path and the values at the first.
func describe(diffs []string) string {
	paths := make([]string, len(diffs))
	for i, d := range diffs {
		paths[i], _, _ = strings.Cut(d, ": in=")
	}
	return fmt.Sprintf("%d difference(s) at %s; first %s", len(diffs), strings.Join(paths, " "), diffs[0])
}

func under(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+"/") {
			return true
		}
	}
	return false
}

// normalize returns v without null members and without members equal to
// their property's schema default. It appends to unknown the path of each
// member that the schemas describing its object do not define.
func normalize(v any, schemas []schemaRef, path string, unknown *[]string) any {
	switch v := v.(type) {
	case map[string]any:
		nodes := expand(schemas, v)
		constrained := false
		for _, n := range nodes {
			_, ok := n.node.(map[string]any)["properties"]
			constrained = constrained || ok
		}
		out := map[string]any{}
		for _, k := range sortedKeys(v) {
			if v[k] == nil {
				continue
			}
			var child []schemaRef
			known := !constrained
			for _, n := range nodes {
				m := n.node.(map[string]any)
				props, _ := m["properties"].(map[string]any)
				if p, ok := props[k]; ok {
					known = true
					child = append(child, schemaRef{n.defs, p})
					continue
				}
				switch ap := m["additionalProperties"].(type) {
				case bool:
					known = known || ap
				case map[string]any:
					known = true
					child = append(child, schemaRef{n.defs, ap})
				}
			}
			if !known {
				*unknown = append(*unknown, path+"/"+k)
			}
			x := normalize(v[k], child, path+"/"+k, unknown)
			if !isDefault(x, child) {
				out[k] = x
			}
		}
		return out
	case []any:
		var items []schemaRef
		for _, n := range expand(schemas, v) {
			if it, ok := n.node.(map[string]any)["items"]; ok {
				items = append(items, schemaRef{n.defs, it})
			}
		}
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = normalize(x, items, fmt.Sprintf("%s/%d", path, i), unknown)
		}
		return out
	}
	return v
}

// isDefault reports whether the normalized value x equals the default of
// one of the property schemas that describe it.
func isDefault(x any, props []schemaRef) bool {
	for _, p := range props {
		m, _ := p.node.(map[string]any)
		def, ok := m["default"]
		if !ok {
			continue
		}
		var ignored []string
		if equalJSON(x, normalize(def, props, "", &ignored)) {
			return true
		}
	}
	return false
}

// expand returns the schema nodes that can describe v: the given ones and
// those they reach through $ref, allOf, anyOf and oneOf, without the
// alternatives whose type or const discriminator v does not match.
func expand(schemas []schemaRef, v any) []schemaRef {
	var out []schemaRef
	var visit func(r schemaRef, depth int)
	visit = func(r schemaRef, depth int) {
		m, ok := r.node.(map[string]any)
		if !ok || depth > 16 || !fits(m, v) {
			return
		}
		out = append(out, r)
		if ref, ok := m["$ref"].(string); ok {
			visit(schemaRef{r.defs, r.defs[strings.TrimPrefix(ref, "#/$defs/")]}, depth+1)
		}
		for _, key := range []string{"allOf", "anyOf", "oneOf"} {
			alts, _ := m[key].([]any)
			for _, alt := range alts {
				visit(schemaRef{r.defs, alt}, depth+1)
			}
		}
	}
	for _, r := range schemas {
		visit(r, 0)
	}
	return out
}

func fits(m map[string]any, v any) bool {
	if t, ok := m["type"]; ok && !typeAllows(t, v) {
		return false
	}
	obj, isObj := v.(map[string]any)
	props, _ := m["properties"].(map[string]any)
	for k, p := range props {
		pm, _ := p.(map[string]any)
		c, hasConst := pm["const"]
		if x, present := obj[k]; hasConst && isObj && present && !equalJSON(c, x) {
			return false
		}
	}
	return true
}

func typeAllows(t, v any) bool {
	kind := "null"
	switch v := v.(type) {
	case map[string]any:
		kind = "object"
	case []any:
		kind = "array"
	case string:
		kind = "string"
	case bool:
		kind = "boolean"
	case json.Number:
		kind = "number"
		if !strings.ContainsAny(string(v), ".eE") {
			kind = "integer"
		}
	}
	types, _ := t.([]any)
	if s, ok := t.(string); ok {
		types = []any{s}
	}
	for _, x := range types {
		if x == kind || x == "number" && kind == "integer" {
			return true
		}
	}
	return false
}

func equalJSON(a, b any) bool {
	var diffs []string
	walkDiff("", a, b, &diffs)
	return len(diffs) == 0
}

func walkDiff(path string, a, b any, diffs *[]string) {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for _, k := range sortedKeys(keys) {
			walkDiff(path+"/"+k, am[k], bm[k], diffs)
		}
		return
	}
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if aok && bok && len(as) == len(bs) {
		for i := range as {
			walkDiff(fmt.Sprintf("%s/%d", path, i), as[i], bs[i], diffs)
		}
		return
	}
	an, aNum := a.(json.Number)
	bn, bNum := b.(json.Number)
	if aNum && bNum && sameNumber(an, bn) || !aNum && reflect.DeepEqual(a, b) {
		return
	}
	if path == "" {
		path = "/"
	}
	*diffs = append(*diffs, fmt.Sprintf("%s: in=%s out=%s", path, short(a), short(b)))
}

func decodeExact(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

func sameNumber(a, b json.Number) bool {
	x, okA := new(big.Rat).SetString(string(a))
	y, okB := new(big.Rat).SetString(string(b))
	return okA && okB && x.Cmp(y) == 0
}

func TestCompareJSON(t *testing.T) {
	defs := map[string]any{}
	if err := json.Unmarshal([]byte(`{"D": {"type": "object", "properties": {
		"flag": {"type": "boolean", "default": false},
		"name": {"type": "string"},
		"meta": {"type": "object", "additionalProperties": true}}}}`), &defs); err != nil {
		t.Fatal(err)
	}
	d := []schemaRef{{defs, defs["D"]}}
	for _, tc := range []struct {
		name, in, out string
		schemas       []schemaRef
		lost, unknown int
	}{
		{"integers beyond float64 differ", `{"a":9007199254740993}`, `{"a":9007199254740992}`, nil, 1, 0},
		{"1.0 equals 1", `{"a":1.0}`, `{"a":1}`, nil, 0, 0},
		{"number is not string", `{"a":1}`, `{"a":"1"}`, nil, 1, 0},
		{"null is absent", `{"a":null}`, `{}`, nil, 0, 0},
		{"every path is listed", `{"a":1,"b":1}`, `{"a":2,"b":2}`, nil, 2, 0},
		{"default written back", `{"name":"x"}`, `{"name":"x","flag":false}`, d, 0, 0},
		{"default dropped", `{"name":"x","flag":false}`, `{"name":"x"}`, d, 0, 0},
		{"non-default dropped", `{"name":"x","flag":true}`, `{"name":"x"}`, d, 1, 0},
		{"undefined member dropped", `{"name":"x","extra":1}`, `{"name":"x"}`, d, 0, 1},
		{"free-form member dropped", `{"meta":{"k":"v"}}`, `{}`, d, 1, 0},
	} {
		c, err := compareJSON([]byte(tc.in), []byte(tc.out), tc.schemas)
		if err != nil || len(c.lost) != tc.lost || len(c.unknown) != tc.unknown {
			t.Errorf("%s: compareJSON(%s, %s) = lost %q, unknown %q, %v; want %d lost, %d unknown",
				tc.name, tc.in, tc.out, c.lost, c.unknown, err, tc.lost, tc.unknown)
		}
	}
}
