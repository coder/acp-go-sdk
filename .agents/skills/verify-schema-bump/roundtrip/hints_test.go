package roundtrip

import (
	"encoding/json"
	"fmt"
	"testing"
)

// The schema marks properties whose bad values a reader must tolerate:
//
//	x-deserialize-default-on-error     a value of the wrong shape reads as if absent
//	x-deserialize-skip-invalid-items   bad array items are dropped, good ones kept
//	x-deserialize-default-on-null      null reads as if absent
var hintNames = []string{
	"x-deserialize-default-on-error",
	"x-deserialize-skip-invalid-items",
	"x-deserialize-default-on-null",
}

// malformed are the values tried as a bad value; the first one that makes
// the instance schema-invalid is used.
var malformed = []any{"malformed", json.Number("12345"), true, map[string]any{"malformed": true}, []any{"malformed"}}

// hintSite is a property that carries hints, in a def or in one of the
// def's top-level alternatives (variant >= 0).
type hintSite struct {
	file    *schemaFile
	def     string
	variant int
	prop    string
	schema  map[string]any
	hint    string
}

func findHints(s *suite) []hintSite {
	var sites []hintSite
	seen := map[string]bool{}
	add := func(f *schemaFile, def string, variant int, node any) {
		m, _ := node.(map[string]any)
		props, _ := m["properties"].(map[string]any)
		for _, prop := range sortedKeys(props) {
			ps, _ := props[prop].(map[string]any)
			for _, hint := range hintNames {
				key := fmt.Sprintf("%s/%d/%s/%s", def, variant, prop, hint)
				if ps[hint] == true && !seen[key] {
					seen[key] = true
					sites = append(sites, hintSite{f, def, variant, prop, ps, hint})
				}
			}
		}
	}
	for _, f := range s.files {
		for _, def := range sortedKeys(f.defs) {
			add(f, def, -1, f.defs[def])
			m, _ := f.defs[def].(map[string]any)
			for _, key := range []string{"anyOf", "oneOf"} {
				alts, _ := m[key].([]any)
				for i, alt := range alts {
					add(f, def, i, alt)
				}
			}
		}
	}
	return sites
}

// TestHints checks that the SDK reads each hinted property the way its hint
// says: the SDK decodes the bad value without an error, and the result
// encodes the same as the instance without the bad part.
func TestHints(t *testing.T) {
	s := load(t)
	sites := findHints(s)
	if len(sites) == 0 {
		t.Logf("SUMMARY hints: no hints in this schema")
		return
	}
	counts := map[string]int{}
	for _, site := range sites {
		name := fmt.Sprintf("%s/%s.%s/%s", site.file.name, site.def, site.prop, site.hint)
		if site.variant >= 0 {
			name = fmt.Sprintf("%s/%s[%d].%s/%s", site.file.name, site.def, site.variant, site.prop, site.hint)
		}
		outcome := "pass"
		t.Run(name, func(t *testing.T) {
			skip, err := checkHint(site)
			switch {
			case skip != "":
				outcome = "skip"
				t.Logf("skip %s: %s", name, skip)
			case err != "":
				outcome = "fail"
				t.Errorf("HINT %s %s.%s: %s", site.hint, site.def, site.prop, err)
			}
		})
		counts[outcome]++
	}
	t.Logf("SUMMARY hints: %d sites, %d pass, %d fail, %d skipped (no Go type, or no value the schema rejects)",
		len(sites), counts["pass"], counts["fail"], counts["skip"])
}

// checkHint returns a reason to skip the site, or what the SDK did wrong.
func checkHint(site hintSite) (skip, problem string) {
	if _, ok := registry[site.def]; !ok {
		return "no SDK type", ""
	}
	base, ok := instanceGen{defs: site.file.defs}.value(site.file.defs[site.def], 0, site.variant)
	obj, isObj := base.(map[string]any)
	if !ok || !isObj {
		return "no object instance", ""
	}
	with := func(v any) []byte {
		o := map[string]any{}
		for k, x := range obj {
			o[k] = x
		}
		o[site.prop] = v
		b, _ := json.Marshal(o)
		return b
	}
	without := func() []byte {
		o := map[string]any{}
		for k, x := range obj {
			if k != site.prop {
				o[k] = x
			}
		}
		b, _ := json.Marshal(o)
		return b
	}
	invalid := func(build func(any) []byte) (any, bool) {
		for _, bad := range malformed {
			if site.file.validate(site.def, build(bad)) != nil {
				return bad, true
			}
		}
		return nil, false
	}

	var input, want []byte
	switch site.hint {
	case "x-deserialize-default-on-error":
		bad, ok := invalid(with)
		if !ok {
			return "the property accepts every tried value", ""
		}
		input, want = with(bad), without()
	case "x-deserialize-skip-invalid-items":
		item, ok := instanceGen{defs: site.file.defs}.value(itemsOf(site.file.defs, site.schema), 1, -1)
		if !ok {
			return "no item instance", ""
		}
		if err := site.file.validate(site.def, with([]any{item})); err != nil {
			return "built item is not schema-valid: " + err.Error(), ""
		}
		bad, ok := invalid(func(bad any) []byte { return with([]any{item, bad}) })
		if !ok {
			return "the items accept every tried value", ""
		}
		input, want = with([]any{item, bad}), with([]any{item})
	case "x-deserialize-default-on-null":
		input, want = with(nil), without()
	}

	got, err := decodeEncode(site.def, input)
	if err != nil {
		return "", fmt.Sprintf("decode %s: %v", oneLine(string(input), 160), oneLine(err.Error(), 160))
	}
	expected, err := decodeEncode(site.def, want)
	if err != nil {
		return "the instance without the bad part does not decode: " + err.Error(), ""
	}
	if d := diffJSON(expected, got); d != "" {
		return "", fmt.Sprintf("decoded %s differently from %s: %s", oneLine(string(input), 160), oneLine(string(want), 160), d)
	}
	return "", ""
}

func decodeEncode(def string, data []byte) ([]byte, error) {
	v := registry[def]()
	if err := json.Unmarshal(data, v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// itemsOf returns the items schema of an array property, following $ref,
// allOf and anyOf/oneOf until it finds one.
func itemsOf(defs map[string]any, node any) any {
	m, _ := node.(map[string]any)
	if items, ok := m["items"]; ok {
		return items
	}
	if ref, ok := m["$ref"].(string); ok {
		return itemsOf(defs, defs[ref[len("#/$defs/"):]])
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		alts, _ := m[key].([]any)
		for _, alt := range alts {
			if items := itemsOf(defs, alt); items != nil {
				return items
			}
		}
	}
	return nil
}
