package roundtrip

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

// malformed are the values tried as a bad value next to null: the first one
// that makes the instance schema-invalid is used.
var malformed = []any{"malformed", json.Number("12345"), true, map[string]any{"malformed": true}, []any{"malformed"}}

// hintSite is a property that carries hints, in a value that the SDK reads as
// def: in the def itself, in one of its top-level alternatives (variant >= 0),
// or in the items of an alternative that is an array (items is their schema).
type hintSite struct {
	file     *schemaFile
	def      string
	variant  int
	items    any
	prop     string
	required bool
	schema   map[string]any
	hint     string
}

// path names the site: Def.prop, Def[2].prop in an alternative, and
// Def[2][].prop in the items of one.
func (site hintSite) path() string {
	switch {
	case site.variant < 0:
		return site.def + "." + site.prop
	case site.items != nil:
		return fmt.Sprintf("%s[%d][].%s", site.def, site.variant, site.prop)
	}
	return fmt.Sprintf("%s[%d].%s", site.def, site.variant, site.prop)
}

// findHints lists the hinted properties of each schema file. A def that
// both files share is listed once per file, because the files can define
// it differently. A property behind the $ref of an alternative is listed for
// the union too, because the SDK must select the variant before it reads the
// property.
func findHints(s *suite) []hintSite {
	var sites []hintSite
	add := func(f *schemaFile, def string, variant int, items, node any) {
		props, required := objectOf(f.defs, node)
		for _, prop := range sortedKeys(props) {
			ps, _ := props[prop].(map[string]any)
			for _, hint := range hintNames {
				if ps[hint] == true {
					sites = append(sites, hintSite{f, def, variant, items, prop, required[prop], ps, hint})
				}
			}
		}
	}
	for _, f := range s.files {
		for _, def := range sortedKeys(f.defs) {
			add(f, def, -1, nil, f.defs[def])
			m, _ := f.defs[def].(map[string]any)
			for _, key := range []string{"anyOf", "oneOf"} {
				alts, _ := m[key].([]any)
				for i, alt := range alts {
					add(f, def, i, nil, alt)
					if am, _ := alt.(map[string]any); am["items"] != nil {
						add(f, def, i, am["items"], am["items"])
					}
				}
			}
		}
	}
	return sites
}

// objectOf returns the properties of the object that node describes and the
// names of the required ones, following $ref and allOf. Where node and a def
// behind its $ref both define a property, the schema of node counts.
func objectOf(defs map[string]any, node any) (props map[string]any, required map[string]bool) {
	props, required = map[string]any{}, map[string]bool{}
	seen := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		m, _ := node.(map[string]any)
		own, _ := m["properties"].(map[string]any)
		for name, schema := range own {
			if _, ok := props[name]; !ok {
				props[name] = schema
			}
		}
		names, _ := m["required"].([]any)
		for _, name := range names {
			if name, ok := name.(string); ok {
				required[name] = true
			}
		}
		parts, _ := m["allOf"].([]any)
		for _, part := range parts {
			walk(part)
		}
		if ref, ok := m["$ref"].(string); ok && !seen[ref] {
			seen[ref] = true
			walk(defs[strings.TrimPrefix(ref, "#/$defs/")])
		}
	}
	walk(node)
	return props, required
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
		name := fmt.Sprintf("%s/%s/%s", site.file.name, site.path(), site.hint)
		outcome := "pass"
		t.Run(name, func(t *testing.T) {
			skip, err := checkHint(site)
			switch {
			case skip != "":
				outcome = "skip"
				t.Logf("skip %s: %s", name, skip)
			case err != "":
				outcome = "fail"
				t.Errorf("HINT %s %s (%s): %s", site.hint, site.path(), site.file.name, err)
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
	if site.items != nil {
		base, ok = instanceGen{defs: site.file.defs}.value(site.items, 1, -1)
	}
	obj, isObj := base.(map[string]any)
	if !ok || !isObj {
		return "no object instance", ""
	}
	// encode returns the value of the def that holds o.
	encode := func(o map[string]any) []byte {
		var v any = o
		if site.items != nil {
			v = []any{o}
		}
		b, _ := json.Marshal(v)
		return b
	}
	with := func(v any) []byte {
		o := map[string]any{}
		for k, x := range obj {
			o[k] = x
		}
		o[site.prop] = v
		return encode(o)
	}
	without := func() []byte {
		o := map[string]any{}
		for k, x := range obj {
			if k != site.prop {
				o[k] = x
			}
		}
		return encode(o)
	}
	// absent is what a bad property must read as: the value without the
	// property, or with an empty array when the property is a required array,
	// because a union selects no variant without its required keys.
	absent := without
	if site.required && schemaType(site.schema) == "array" {
		absent = func() []byte { return with([]any{}) }
	}
	// badInputs builds the instance around each bad value to try: null and the
	// first malformed value, each when the schema rejects the instance.
	badInputs := func(node any, build func(any) []byte) (inputs [][]byte) {
		try := func(bad any) bool {
			input := build(bad)
			rejected := site.file.validate(site.def, input) != nil
			if rejected {
				inputs = append(inputs, input)
			}
			return rejected
		}
		try(nil)
		for _, bad := range malformed {
			if _, isString := bad.(string); isString && readsEveryString(site.file.defs, node) {
				continue
			}
			if try(bad) {
				break
			}
		}
		return inputs
	}

	var inputs [][]byte
	var want []byte
	switch site.hint {
	case "x-deserialize-default-on-error":
		// The value with a good property must be schema-valid, so that a
		// bad value is what makes it invalid.
		good, ok := instanceGen{defs: site.file.defs}.value(site.schema, 1, -1)
		if !ok || site.file.validate(site.def, with(good)) != nil {
			return "no schema-valid instance with the property set", ""
		}
		inputs, want = badInputs(site.schema, with), absent()
		if len(inputs) == 0 {
			return "the property accepts every tried value", ""
		}
	case "x-deserialize-skip-invalid-items":
		item, ok := instanceGen{defs: site.file.defs}.value(itemsOf(site.file.defs, site.schema), 1, -1)
		if !ok {
			return "no item instance", ""
		}
		if err := site.file.validate(site.def, with([]any{item})); err != nil {
			return "built item is not schema-valid: " + err.Error(), ""
		}
		inputs = badInputs(itemsOf(site.file.defs, site.schema), func(bad any) []byte { return with([]any{item, bad}) })
		if len(inputs) == 0 {
			return "the items accept every tried value", ""
		}
		want = with([]any{item})
	case "x-deserialize-default-on-null":
		inputs, want = [][]byte{with(nil)}, absent()
	}

	expected, expectedErr := decodeEncode(site.def, want)
	for _, input := range inputs {
		got, err := decodeEncode(site.def, input)
		if err != nil {
			return "", fmt.Sprintf("decode %s: %v", oneLine(string(input), 160), oneLine(err.Error(), 160))
		}
		if expectedErr != nil {
			return "the instance without the bad part does not decode: " + expectedErr.Error(), ""
		}
		c, err := compareJSON(expected, got, defRefs(site.def, []*schemaFile{site.file}))
		if err != nil {
			return "", "compare: " + err.Error()
		}
		if diffs := append(c.lost, c.unknown...); len(diffs) > 0 {
			return "", fmt.Sprintf("decoded %s differently from %s: %s", oneLine(string(input), 160), oneLine(string(want), 160), describe(diffs))
		}
		// compareJSON counts null as absent, so check that null was not kept.
		if site.member(input) == "null" && site.member(got) == "null" && site.member(expected) != "null" {
			return "", fmt.Sprintf("decoded %s and kept %s as null", oneLine(string(input), 160), site.prop)
		}
	}
	return "", ""
}

// member returns the JSON of the site's property in doc, a value of the
// site's def, or "" when doc has no such member.
func (site hintSite) member(doc []byte) string {
	var obj map[string]json.RawMessage
	if site.items != nil {
		var arr []map[string]json.RawMessage
		if json.Unmarshal(doc, &arr) != nil || len(arr) == 0 {
			return ""
		}
		obj = arr[0]
	} else if json.Unmarshal(doc, &obj) != nil {
		return ""
	}
	return string(obj[site.prop])
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

// readsEveryString reports whether node resolves to a closed string enum that
// has the member "other". The Rust schema crate marks that member
// #[serde(other)] and reads every string the enum does not list as it, so a
// string is no bad value there.
func readsEveryString(defs map[string]any, node any) bool {
	m, _ := node.(map[string]any)
	if ref, ok := m["$ref"].(string); ok {
		return readsEveryString(defs, defs[ref[len("#/$defs/"):]])
	}
	members, _ := m["enum"].([]any)
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		alts, _ := m[key].([]any)
		for _, alt := range alts {
			am, _ := alt.(map[string]any)
			if member, ok := am["const"]; ok {
				members = append(members, member)
			} else if readsEveryString(defs, alt) {
				return true
			}
		}
	}
	for _, member := range members {
		if member == "other" {
			return true
		}
	}
	return false
}

// readsOther reads kind as the generated SDK reads ToolCallUpdate.kind: a
// string that Kind does not list is "other", and a value that is no string
// reads as absent.
type readsOther struct {
	Kind *string `json:"kind,omitempty"`
}

func (v *readsOther) UnmarshalJSON(b []byte) error {
	var raw struct {
		Kind json.RawMessage `json:"kind"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	var kind string
	if json.Unmarshal(raw.Kind, &kind) != nil {
		return nil
	}
	if kind != "read" {
		kind = "other"
	}
	v.Kind = &kind
	return nil
}

// keyed reads Launch as the SDK reads the variant of a union that the
// required key args selects, when it ignores the hint of args there: null
// selects no variant, and another value that is no array reads as empty.
type keyed struct {
	Args []string `json:"args"`
}

func (v *keyed) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if raw := string(m["args"]); raw == "" || raw == "null" {
		return errors.New("no matching variant for union")
	}
	if json.Unmarshal(m["args"], &v.Args) != nil {
		v.Args = []string{}
	}
	return nil
}

// hinted is keyed with the hint honored: a null args reads as empty too.
type hinted struct{ keyed }

func (v *hinted) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if string(m["args"]) == "null" {
		v.Args = []string{}
		return nil
	}
	return v.keyed.UnmarshalJSON(b)
}

const unionHintSchema = `{"$defs": {
	"Launch": {"type": "object", "required": ["args"], "properties": {
		"args": {"type": "array", "items": {"type": "string"}, "x-deserialize-default-on-error": true}}},
	"Runner": {"anyOf": [
		{"type": "object", "required": ["url"], "properties": {"url": {"type": "string"}}},
		{"allOf": [{"$ref": "#/$defs/Launch"}]}]},
	"Runners": {"anyOf": [{"type": "array", "items": {"$ref": "#/$defs/Launch"}}]}}}`

func TestFindHints_ListsAPropertyForTheUnionThatSelectsIt(t *testing.T) {
	f, err := newSchemaFile("test", "test.json", []byte(unionHintSchema))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, site := range findHints(&suite{files: []*schemaFile{f}}) {
		got = append(got, fmt.Sprintf("%s required=%v", site.path(), site.required))
	}
	want := []string{"Launch.args required=true", "Runner[1].args required=true", "Runners[0][].args required=true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findHints found %v, want %v", got, want)
	}
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
			"p": {"type": "string", "x-deserialize-default-on-error": true}}},
		"Kind": {"oneOf": [{"const": "read", "type": "string"}, {"const": "other", "type": "string"}]},
		"O": {"type": "object", "properties": {
			"kind": {"anyOf": [{"$ref": "#/$defs/Kind"}, {"type": "null"}], "x-deserialize-default-on-error": true}}}}}`))
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
	u, err := newSchemaFile("test", "test.json", []byte(unionHintSchema))
	if err != nil {
		t.Fatal(err)
	}
	sites := map[string]hintSite{}
	for _, site := range findHints(&suite{files: []*schemaFile{f, u}}) {
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
		{"an enum with the member other takes every string", "O", func() any { return new(readsOther) }, false, false},
		{"a null required key selects no variant", "Runner", func() any { return new(keyed) }, false, true},
		{"a null required key reads as the default", "Runner", func() any { return new(hinted) }, false, false},
		{"a null required key of an item selects no variant", "Runners", func() any { return new([]keyed) }, false, true},
		{"a null required key of an item reads as the default", "Runners", func() any { return new([]hinted) }, false, false},
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
