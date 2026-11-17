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

// findHints lists the hinted properties of each schema file. A def that
// both files share is listed once per file, because the files can define
// it differently.
func findHints(s *suite) []hintSite {
	var sites []hintSite
	add := func(f *schemaFile, def string, variant int, node any) {
		m, _ := node.(map[string]any)
		props, _ := m["properties"].(map[string]any)
		for _, prop := range sortedKeys(props) {
			ps, _ := props[prop].(map[string]any)
			for _, hint := range hintNames {
				if ps[hint] == true {
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
				t.Errorf("HINT %s %s.%s (%s): %s", site.hint, site.def, site.prop, site.file.name, err)
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
		// The value with a good property must be schema-valid, so that a
		// bad value is what makes it invalid.
		good, ok := instanceGen{defs: site.file.defs}.value(site.schema, 1, -1)
		if !ok || site.file.validate(site.def, with(good)) != nil {
			return "no schema-valid instance with the property set", ""
		}
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
	c, err := compareJSON(expected, got, defRefs(site.def, []*schemaFile{site.file}))
	if err != nil {
		return "", "compare: " + err.Error()
	}
	if diffs := append(c.lost, c.unknown...); len(diffs) > 0 {
		return "", fmt.Sprintf("decoded %s differently from %s: %s", oneLine(string(input), 160), oneLine(string(want), 160), describe(diffs))
	}
	// compareJSON counts null as absent, so check that null was not kept.
	if site.hint == "x-deserialize-default-on-null" && isNullMember(got, site.prop) && !isNullMember(expected, site.prop) {
		return "", fmt.Sprintf("decoded %s and kept %s as null", oneLine(string(input), 160), site.prop)
	}
	return "", ""
}

// isNullMember reports whether the JSON object doc has prop set to null.
func isNullMember(doc []byte, prop string) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(doc, &m) == nil && string(m[prop]) == "null"
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

func TestFindHints_KeepsEachSchemaFile(t *testing.T) {
	def := map[string]any{"properties": map[string]any{"p": map[string]any{"x-deserialize-default-on-null": true}}}
	s := &suite{files: []*schemaFile{
		{name: "stable", defs: map[string]any{"D": def}},
		{name: "unstable", defs: map[string]any{"D": def}},
	}}
	if got := len(findHints(s)); got != 2 {
		t.Errorf("findHints found %d sites, want one per schema file (2)", got)
	}
}

func TestCheckHint(t *testing.T) {
	f, err := newSchemaFile("test", "test.json", []byte(`{"$defs": {
		"N": {"type": "object", "properties": {
			"p": {"type": ["string", "null"], "x-deserialize-default-on-null": true}}},
		"E": {"type": "object", "required": ["name"], "properties": {
			"name": {"type": "string", "minLength": 20},
			"p": {"type": "string", "x-deserialize-default-on-error": true}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	type keepsNull struct {
		P json.RawMessage `json:"p,omitempty"`
	}
	type readsNullAsAbsent struct {
		P *string `json:"p,omitempty"`
	}
	type plain struct {
		Name string `json:"name"`
		P    string `json:"p,omitempty"`
	}
	sites := map[string]hintSite{}
	for _, site := range findHints(&suite{files: []*schemaFile{f}}) {
		sites[site.def] = site
	}
	for _, tc := range []struct {
		name, def   string
		sdkType     func() any
		skip, fails bool
	}{
		{"null kept as null", "N", func() any { return new(keepsNull) }, false, true},
		{"null read as absent", "N", func() any { return new(readsNullAsAbsent) }, false, false},
		{"no schema-valid baseline", "E", func() any { return new(plain) }, true, false},
	} {
		saved, had := registry[tc.def]
		registry[tc.def] = tc.sdkType
		skip, problem := checkHint(sites[tc.def])
		if had {
			registry[tc.def] = saved
		} else {
			delete(registry, tc.def)
		}
		if (skip != "") != tc.skip || (problem != "") != tc.fails {
			t.Errorf("%s: checkHint = skip %q, problem %q; want skip %v, fails %v", tc.name, skip, problem, tc.skip, tc.fails)
		}
	}
}
