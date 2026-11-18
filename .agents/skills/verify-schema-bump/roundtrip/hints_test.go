package roundtrip

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
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
	if path := os.Getenv("G2_PROBES"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		probeLog = json.NewEncoder(f)
		defer func() { probeLog = nil }()
	}
	counts := map[string]int{}
	probes := 0
	for _, site := range sites {
		name := fmt.Sprintf("%s/%s/%s", site.file.name, site.path(), site.hint)
		outcome := "pass"
		t.Run(name, func(t *testing.T) {
			r := checkHint(site)
			probes += r.probes
			switch {
			case r.skip != "":
				outcome = "skip"
				t.Logf("skip %s: %s", name, r.skip)
			case len(r.problems) > 0:
				outcome = "fail"
				for _, p := range r.problems {
					t.Errorf("HINT %s %s (%s) [%s]: %s", site.hint, site.path(), site.file.name, p.probe, p.detail)
				}
			}
		})
		counts[outcome]++
	}
	t.Logf("SUMMARY hints: %d sites, %d pass, %d fail, %d skipped (no Go type, or no value the schema rejects); %d bad values tried",
		len(sites), counts["pass"], counts["fail"], counts["skip"], probes)
}

// probeLog, when G2_PROBES names a file, receives every bad value that
// checkHint tries, so that another SDK can decode the same values.
var probeLog *json.Encoder

// hintResult is what checkHint found at a site: a reason to skip it, or the
// bad values that the SDK read wrong, one problem per probe class.
type hintResult struct {
	skip     string
	probes   int
	problems []hintProblem
}

type hintProblem struct{ probe, detail string }

// badValue is one input that checkHint tries: the instance around a bad
// value of probe class probe (null, wrong-type, or a deep probe), at member
// path at in the hinted value.
type badValue struct {
	probe, at string
	input     []byte
}

// checkHint tries the bad values of each probe class at the site and
// returns what the SDK read wrong, or a reason to skip the site.
func checkHint(site hintSite) hintResult {
	if _, ok := registry[site.def]; !ok {
		return hintResult{skip: "no SDK type"}
	}
	gen := instanceGen{defs: site.file.defs}
	base, ok := gen.value(site.file.defs[site.def], 0, site.variant)
	if site.items != nil {
		base, ok = gen.value(site.items, 1, -1)
	}
	obj, isObj := base.(map[string]any)
	if !ok || !isObj {
		return hintResult{skip: "no object instance"}
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
	var inputs []badValue
	// try adds the instance that build makes around bad when the schema
	// rejects it.
	try := func(probe, at string, bad any, build func(any) []byte) bool {
		input := build(bad)
		rejected := site.file.validate(site.def, input) != nil
		if rejected {
			inputs = append(inputs, badValue{probe, at, input})
		}
		return rejected
	}
	// shallow tries null and the first malformed value in place of a value
	// of node.
	shallow := func(node any, build func(any) []byte) {
		try("null", "/", nil, build)
		for _, bad := range malformed {
			if _, isString := bad.(string); isString && readsEveryString(site.file.defs, node) {
				continue
			}
			if try("wrong-type", "/", bad, build) {
				break
			}
		}
	}
	// deep tries each deep probe of a full value of node in place of a
	// value of node, when that full value is schema-valid there.
	deep := func(node any, build func(any) []byte) {
		full, ok := instanceGen{defs: site.file.defs, full: true}.value(node, 1, -1)
		if !ok || site.file.validate(site.def, build(full)) != nil {
			return
		}
		for _, m := range mutations(site.file.defs, node, full) {
			try(m.probe, m.String(), m.apply(full), build)
		}
	}

	var want []byte
	switch site.hint {
	case "x-deserialize-default-on-error":
		// The value with a good property must be schema-valid, so that a
		// bad value is what makes it invalid.
		good, ok := gen.value(site.schema, 1, -1)
		if !ok || site.file.validate(site.def, with(good)) != nil {
			return hintResult{skip: "no schema-valid instance with the property set"}
		}
		shallow(site.schema, with)
		// The items of an array that also has
		// x-deserialize-skip-invalid-items are probed at that hint.
		if ps, _ := site.schema["x-deserialize-skip-invalid-items"].(bool); !ps {
			deep(site.schema, with)
		}
		want = absent()
	case "x-deserialize-skip-invalid-items":
		items := itemsOf(site.file.defs, site.schema)
		item, ok := gen.value(items, 1, -1)
		if !ok {
			return hintResult{skip: "no item instance"}
		}
		if err := site.file.validate(site.def, with([]any{item})); err != nil {
			return hintResult{skip: "built item is not schema-valid: " + err.Error()}
		}
		withBad := func(bad any) []byte { return with([]any{item, bad}) }
		shallow(items, withBad)
		deep(items, withBad)
		want = with([]any{item})
	case "x-deserialize-default-on-null":
		inputs, want = []badValue{{"null", "/", with(nil)}}, absent()
	}
	if len(inputs) == 0 {
		return hintResult{skip: "the property accepts every tried value"}
	}
	expected, err := decodeEncode(site.def, want)
	if err != nil {
		return hintResult{skip: "the instance without the bad part does not decode: " + err.Error()}
	}

	r := hintResult{probes: len(inputs)}
	failed := map[string][]string{} // probe class -> problems
	var classes []string
	for _, in := range inputs {
		if probeLog != nil {
			probeLog.Encode(map[string]any{"def": site.def, "file": site.file.name, "site": site.path(), "hint": site.hint,
				"probe": in.probe, "at": in.at, "json": json.RawMessage(in.input), "want": json.RawMessage(want)})
		}
		if problem := readsAsWant(site, in.input, want, expected); problem != "" {
			if failed[in.probe] == nil {
				classes = append(classes, in.probe)
			}
			failed[in.probe] = append(failed[in.probe], fmt.Sprintf("at %s: %s", in.at, problem))
		}
	}
	for _, class := range classes {
		n := 0
		for _, in := range inputs {
			if in.probe == class {
				n++
			}
		}
		detail := failed[class][0]
		if len(failed[class]) > 1 || n > 1 {
			detail = fmt.Sprintf("%d of %d bad values; first %s", len(failed[class]), n, detail)
		}
		r.problems = append(r.problems, hintProblem{class, detail})
	}
	return r
}

// readsAsWant returns how the SDK read input other than as want, which it
// reads as expected, or "" when it read them the same.
func readsAsWant(site hintSite, input, want, expected []byte) string {
	got, err := decodeEncode(site.def, input)
	if err != nil {
		return fmt.Sprintf("decode %s: %v", oneLine(string(input), 160), oneLine(err.Error(), 160))
	}
	c, err := compareJSON(expected, got, defRefs(site.def, []*schemaFile{site.file}))
	if err != nil {
		return "compare: " + err.Error()
	}
	if diffs := append(c.lost, c.unknown...); len(diffs) > 0 {
		return fmt.Sprintf("decoded %s differently from %s: %s", oneLine(string(input), 160), oneLine(string(want), 160), describe(diffs))
	}
	// compareJSON counts null as absent, so check that null was not kept.
	if site.member(input) == "null" && site.member(got) == "null" && site.member(expected) != "null" {
		return fmt.Sprintf("decoded %s and kept %s as null", oneLine(string(input), 160), site.prop)
	}
	return ""
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
// selects no variant, and another value that is no array of strings reads as
// empty.
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
	// A null item breaks args as a value of the wrong type does.
	var args []*string
	if json.Unmarshal(m["args"], &args) != nil || slices.Contains(args, nil) {
		v.Args = []string{}
		return nil
	}
	v.Args = make([]string, len(args))
	for i, arg := range args {
		v.Args[i] = *arg
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

// entry reads Entry as encoding/json does: a member that the JSON omits, or
// that is null, reads as the zero value.
type entry struct {
	Name string `json:"name"`
}

// checkedEntry reads Entry as the Rust types do: without a name, or with a
// null one, the value is no Entry.
type checkedEntry struct{ entry }

func (e *checkedEntry) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if raw := string(m["name"]); raw == "" || raw == "null" {
		return errors.New("missing field name")
	}
	return json.Unmarshal(b, &e.entry)
}

// lenientHolder reads H as the hint on entry asks: an entry that does not
// decode as E reads as absent.
type lenientHolder[E any] struct {
	Entry *E `json:"entry,omitempty"`
}

func (h *lenientHolder[E]) UnmarshalJSON(b []byte) error {
	var raw struct {
		Entry json.RawMessage `json:"entry"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	var e E
	if string(raw.Entry) != "null" && json.Unmarshal(raw.Entry, &e) == nil {
		h.Entry = &e
	}
	return nil
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
			"kind": {"anyOf": [{"$ref": "#/$defs/Kind"}, {"type": "null"}], "x-deserialize-default-on-error": true}}},
		"Entry": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
		"H": {"type": "object", "properties": {
			"entry": {"anyOf": [{"$ref": "#/$defs/Entry"}, {"type": "null"}], "x-deserialize-default-on-error": true}}}}}`))
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
		{"a value without a required member is kept", "H", func() any { return new(lenientHolder[entry]) }, false, true},
		{"a value without a required member reads as absent", "H", func() any { return new(lenientHolder[checkedEntry]) }, false, false},
	} {
		saved, had := registry[tc.def]
		registry[tc.def] = tc.sdkType
		r := checkHint(sites[tc.def])
		if had {
			registry[tc.def] = saved
		} else {
			delete(registry, tc.def)
		}
		if (r.skip != "") != tc.skip || (len(r.problems) > 0) != tc.fails {
			t.Errorf("%s: checkHint = skip %q, problems %+v; want skip %v, fails %v", tc.name, r.skip, r.problems, tc.skip, tc.fails)
		}
	}
}
