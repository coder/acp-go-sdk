package roundtrip

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// maxDepth bounds how deep a full instance fills optional properties.
// Deeper, only required properties are set, so recursive defs end.
const maxDepth = 5

// instanceGen builds JSON values that a schema accepts. With full set it
// fills every property, else only required ones. It keeps no state.
type instanceGen struct {
	defs map[string]any
	full bool
}

// value returns a value for the schema node. variant picks the node's own
// anyOf/oneOf alternative or enum value; -1 takes the first usable one.
func (g instanceGen) value(node any, depth, variant int) (any, bool) {
	m, ok := node.(map[string]any)
	if !ok {
		b, isBool := node.(bool)
		return map[string]any{"example": "value"}, isBool && b
	}
	if c, ok := m["const"]; ok {
		return c, true
	}
	if e, ok := m["enum"].([]any); ok && len(e) > 0 {
		if variant >= 0 && variant < len(e) {
			return e[variant], true
		}
		return e[0], true
	}
	if depth > 4*maxDepth {
		return nil, false
	}
	var parts []any
	if ref, ok := m["$ref"].(string); ok {
		v, ok := g.value(g.defs[strings.TrimPrefix(ref, "#/$defs/")], depth+1, -1)
		if !ok {
			return nil, false
		}
		parts = append(parts, v)
	}
	if all, ok := m["allOf"].([]any); ok {
		for _, s := range all {
			v, ok := g.value(s, depth, -1)
			if !ok {
				return nil, false
			}
			parts = append(parts, v)
		}
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		alts, ok := m[key].([]any)
		if !ok {
			continue
		}
		v, ok := g.alternative(alts, depth, variant)
		if !ok {
			return nil, false
		}
		parts = append(parts, v)
	}
	typ := schemaType(m)
	if typ == "" && len(parts) == 0 {
		typ = "any"
	}
	if typ != "" {
		v, ok := g.ofType(typ, m, depth)
		if !ok {
			return nil, false
		}
		parts = append(parts, v)
	}
	return merge(parts), true
}

func (g instanceGen) alternative(alts []any, depth, variant int) (any, bool) {
	if variant >= 0 {
		if variant >= len(alts) {
			return nil, false
		}
		return g.value(alts[variant], depth, -1)
	}
	for _, nullOK := range []bool{false, true} {
		for _, alt := range alts {
			if am, _ := alt.(map[string]any); !nullOK && am["type"] == "null" {
				continue
			}
			if v, ok := g.value(alt, depth, -1); ok {
				return v, true
			}
		}
	}
	return nil, false
}

func (g instanceGen) ofType(typ string, m map[string]any, depth int) (any, bool) {
	switch typ {
	case "object":
		obj := map[string]any{}
		required := map[string]bool{}
		if req, ok := m["required"].([]any); ok {
			for _, r := range req {
				required[r.(string)] = true
			}
		}
		props, _ := m["properties"].(map[string]any)
		for _, name := range sortedKeys(props) {
			if !required[name] && !(g.full && depth < maxDepth) {
				continue
			}
			v, ok := g.value(props[name], depth+1, -1)
			if !ok {
				if required[name] {
					return nil, false
				}
				continue
			}
			obj[name] = v
		}
		// A free-form object such as _meta or a map type gets one member.
		// So does an object that takes members its schema does not name.
		ap := m["additionalProperties"]
		switch {
		case !g.full:
		case ap == true || m["unevaluatedProperties"] == true:
			obj["example"] = "value"
		case len(props) == 0:
			if v, ok := g.value(ap, depth+1, -1); ok && depth < maxDepth {
				obj["example"] = v
			}
		}
		return obj, true
	case "array":
		minItems, _ := m["minItems"].(json.Number)
		if !g.full && (minItems == "" || minItems == "0") {
			return []any{}, true
		}
		v, ok := g.value(m["items"], depth+1, -1)
		if !ok {
			return []any{}, minItems == "" || minItems == "0"
		}
		return []any{v}, true
	case "string":
		switch m["format"] {
		case "uri":
			return "https://example.com/x", true
		case "date-time":
			return "2024-01-01T00:00:00Z", true
		}
		return "example", true
	case "integer":
		if min, ok := m["minimum"].(json.Number); ok {
			return min, true
		}
		return json.Number("1"), true
	case "number":
		return json.Number("1.5"), true
	case "boolean":
		return true, true
	case "null":
		return nil, true
	case "any":
		return map[string]any{"example": "value"}, true
	}
	return nil, false
}

// schemaType is the node's first non-null type, "null" when null is its
// only type, "object" when it has properties but no type, else "".
func schemaType(m map[string]any) string {
	switch t := m["type"].(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if x != "null" {
				return x.(string)
			}
		}
		return "null"
	}
	if _, ok := m["properties"]; ok {
		return "object"
	}
	return ""
}

// merge combines the values that a node's $ref, allOf, alternative and own
// type produced: objects merge, otherwise the last value wins.
func merge(parts []any) any {
	var out any
	for _, p := range parts {
		om, ok1 := out.(map[string]any)
		pm, ok2 := p.(map[string]any)
		if ok1 && ok2 {
			merged := map[string]any{}
			for k, v := range om {
				merged[k] = v
			}
			for k, v := range pm {
				merged[k] = v
			}
			out = merged
			continue
		}
		out = p
	}
	return out
}

// variants is the number of alternatives or enum values at the top of a
// def, or 0 when it has none.
func variants(node any) int {
	m, _ := node.(map[string]any)
	for _, key := range []string{"anyOf", "oneOf", "enum"} {
		if alts, ok := m[key].([]any); ok {
			return len(alts)
		}
	}
	return 0
}

// TestInstances round-trips values built from every $defs entry: one with
// required properties only and one with every property, for each top-level
// alternative or enum value. A value the schema rejects is a gap in the
// builder, not in the SDK, so it is counted and skipped.
func TestInstances(t *testing.T) {
	s := load(t)
	var checked, invalid int
	var noType []string
	stable := s.files[0].defs
	for _, f := range s.files {
		for _, def := range sortedKeys(f.defs) {
			if _, ok := registry[def]; !ok {
				// A stable def must have a Go type. An unstable-only one may
				// lack it: it is listed in the summary, not failed.
				if _, isStable := stable[def]; isStable && f.name == "stable" {
					t.Errorf("NO-TYPE %s (stable)", def)
				} else if !isStable {
					noType = append(noType, def)
				}
				continue
			}
			seen := map[string]bool{}
			for _, full := range []bool{false, true} {
				for v := -1; v < variants(f.defs[def]); v++ {
					if v == -1 && variants(f.defs[def]) > 0 {
						continue
					}
					value, ok := instanceGen{defs: f.defs, full: full}.value(f.defs[def], 0, v)
					if !ok {
						invalid++
						t.Logf("builder: no value for %s %s variant %d", f.name, def, v)
						continue
					}
					data := mustJSON(t, value)
					if seen[string(data)] {
						continue
					}
					seen[string(data)] = true
					if err := f.validate(def, data); err != nil {
						invalid++
						t.Logf("builder: %s %s variant %d is not schema-valid: %v", f.name, def, v, err)
						continue
					}
					checked++
					t.Run(fmt.Sprintf("%s/%s/full=%v/variant=%d", f.name, def, full, v), func(t *testing.T) {
						checkRoundTrip(t, def, data, []*schemaFile{f})
					})
				}
			}
		}
	}
	t.Logf("SUMMARY instances: %d checked, %d builder gaps skipped, %d unstable-only defs without an SDK type %v",
		checked, invalid, len(noType), noType)
}

// An object whose schema takes members it does not name gets one in a full
// value, so that the round trip shows whether the SDK keeps it.
func TestInstanceGen_FullValueHasAMemberThatTheSchemaDoesNotName(t *testing.T) {
	defs := map[string]any{}
	if err := json.Unmarshal([]byte(`{
		"Open": {"type": "object", "properties": {"mode": {"type": "string"}}, "additionalProperties": true},
		"Unevaluated": {"type": "object", "properties": {"mode": {"type": "string"}}, "unevaluatedProperties": true},
		"Closed": {"type": "object", "properties": {"mode": {"type": "string"}}}}`), &defs); err != nil {
		t.Fatal(err)
	}
	for def, want := range map[string]bool{"Open": true, "Unevaluated": true, "Closed": false} {
		v, ok := instanceGen{defs: defs, full: true}.value(defs[def], 0, -1)
		obj, _ := v.(map[string]any)
		if _, got := obj["example"]; !ok || got != want {
			t.Errorf("full value of %s = %v; want an unnamed member: %v", def, v, want)
		}
	}
}
