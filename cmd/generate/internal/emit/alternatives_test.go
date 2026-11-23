package emit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// alternativeKindsSchema has one union per kind of alternative that is not
// an object, each beside the object Named. %[1]s is anyOf or oneOf.
const alternativeKindsSchema = `{"$defs": {
  "Named": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
  "Label": {"type": "string"},
  "Choice": {"enum": ["a", "b"]},
  "Wide": {"type": "integer", "format": "int64"},
  "NamedAlias": {"allOf": [{"$ref": "#/$defs/Named"}]},
  "LabelAlias": {"allOf": [{"$ref": "#/$defs/Label"}]},
  "PickString": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"type": "string"}]},
  "PickTitled": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"title": "Text", "type": "string"}]},
  "PickConst": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"type": "string", "const": "a"}, {"type": "string", "const": "b"}]},
  "PickUntypedConst": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"const": "a"}, {"const": 1.5}, {"const": true}]},
  "PickEnum": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"type": "string", "enum": ["a", "b"]}]},
  "PickUntypedEnum": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"enum": ["a", "b"]}]},
  "PickNumber": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"type": "number"}]},
  "PickInteger": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"type": "integer"}]},
  "PickWideConst": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"type": "integer", "format": "int64", "const": 4294967295}]},
  "PickFormatConsts": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"type": "integer", "format": "uint16", "const": 65535}, {"type": "integer", "format": "uint32", "const": 4294967295}, {"type": "integer", "format": "int32", "const": -2147483648}, {"type": "integer", "format": "uint64", "const": 9007199254740991}]},
  "PickBoolean": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"type": "boolean"}]},
  "PickArray": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"type": "array", "items": {"type": "string"}}]},
  "PickRef": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"$ref": "#/$defs/Label"}]},
  "PickRefConst": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"const": "a", "allOf": [{"$ref": "#/$defs/Label"}]}]},
  "PickRefEnumConst": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"const": "a", "allOf": [{"$ref": "#/$defs/Choice"}]}]},
  "PickRefWide": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"const": 4294967295, "allOf": [{"$ref": "#/$defs/Wide"}]}]},
  "PickAlias": {"%[1]s": [{"$ref": "#/$defs/NamedAlias"}, {"$ref": "#/$defs/LabelAlias"}]},
  "PickRefAliasConst": {"%[1]s": [{"$ref": "#/$defs/Named"}, {"const": "a", "allOf": [{"$ref": "#/$defs/LabelAlias"}]}]},
  "PickOpen": {"%[1]s": [{"type": "string", "const": "a"}, {"type": "string", "const": "b"}, {"title": "Other", "type": "string"}]}
}}`

// roundTripTest is the helper of the generated tests: each JSON value of
// accept decodes into a T that encodes back to the same JSON, and no value
// of reject decodes. A null member counts as absent, as in the G2 comparison
// (roundtrip/compare_test.go normalize), since an optional field drops it.
const roundTripTest = `package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func withoutNulls(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, member := range v {
			if member == nil {
				delete(v, k)
			} else {
				v[k] = withoutNulls(member)
			}
		}
	case []any:
		for i, item := range v {
			v[i] = withoutNulls(item)
		}
	}
	return v
}

func roundTrips[T any](t *testing.T, accept, reject []string) {
	t.Helper()
	for _, in := range accept {
		var v T
		if err := json.Unmarshal([]byte(in), &v); err != nil {
			t.Errorf("%T: unmarshal %s: %v", v, in, err)
			continue
		}
		out, err := json.Marshal(v)
		if err != nil {
			t.Errorf("%T: marshal the value of %s: %v", v, in, err)
			continue
		}
		var want, got any
		_ = json.Unmarshal([]byte(in), &want)
		_ = json.Unmarshal(out, &got)
		if !reflect.DeepEqual(withoutNulls(want), withoutNulls(got)) {
			t.Errorf("%T: %s encodes back as %s", v, in, out)
		}
	}
	for _, in := range reject {
		var v T
		if err := json.Unmarshal([]byte(in), &v); err == nil {
			out, _ := json.Marshal(v)
			t.Errorf("%T: unmarshal %s: got %s, want an error", v, in, out)
		}
	}
}
`

const alternativeKindsTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestUnion_KeepsEveryKindOfAlternative(t *testing.T) {
	named := ` + "`" + `{"name":"n"}` + "`" + `
	roundTrips[PickString](t, []string{named, ` + "`" + `"x"` + "`" + `}, []string{` + "`" + `5` + "`" + `})
	roundTrips[PickTitled](t, []string{named, ` + "`" + `"x"` + "`" + `}, []string{` + "`" + `5` + "`" + `})
	roundTrips[PickConst](t, []string{named, ` + "`" + `"a"` + "`" + `, ` + "`" + `"b"` + "`" + `}, []string{` + "`" + `"c"` + "`" + `})
	roundTrips[PickUntypedConst](t, []string{named, ` + "`" + `"a"` + "`" + `, ` + "`" + `1.5` + "`" + `, ` + "`" + `true` + "`" + `}, []string{` + "`" + `"b"` + "`" + `, ` + "`" + `2` + "`" + `, ` + "`" + `false` + "`" + `})
	roundTrips[PickEnum](t, []string{named, ` + "`" + `"a"` + "`" + `, ` + "`" + `"b"` + "`" + `}, []string{` + "`" + `"c"` + "`" + `})
	roundTrips[PickUntypedEnum](t, []string{named, ` + "`" + `"a"` + "`" + `, ` + "`" + `"b"` + "`" + `}, []string{` + "`" + `"c"` + "`" + `})
	roundTrips[PickNumber](t, []string{named, ` + "`" + `1.5` + "`" + `}, []string{` + "`" + `"x"` + "`" + `})
	roundTrips[PickInteger](t, []string{named, ` + "`" + `7` + "`" + `}, []string{` + "`" + `1.5` + "`" + `, ` + "`" + `"x"` + "`" + `})
	roundTrips[PickWideConst](t, []string{named, ` + "`" + `4294967295` + "`" + `}, []string{` + "`" + `4294967294` + "`" + `, ` + "`" + `-2147483648` + "`" + `})
	// A const at the limit of the Go type of its format.
	roundTrips[PickFormatConsts](t, []string{named, ` + "`" + `65535` + "`" + `, ` + "`" + `4294967295` + "`" + `, ` + "`" + `-2147483648` + "`" + `, ` + "`" + `9007199254740991` + "`" + `}, []string{` + "`" + `65534` + "`" + `, ` + "`" + `0` + "`" + `, ` + "`" + `-1` + "`" + `})
	roundTrips[PickBoolean](t, []string{named, ` + "`" + `true` + "`" + `}, []string{` + "`" + `"x"` + "`" + `})
	roundTrips[PickArray](t, []string{named, ` + "`" + `["a"]` + "`" + `}, []string{` + "`" + `"x"` + "`" + `, ` + "`" + `[1]` + "`" + `})
	roundTrips[PickRef](t, []string{named, ` + "`" + `"x"` + "`" + `}, []string{` + "`" + `5` + "`" + `})
	roundTrips[PickRefConst](t, []string{named, ` + "`" + `"a"` + "`" + `}, []string{` + "`" + `"b"` + "`" + `})
	roundTrips[PickRefEnumConst](t, []string{named, ` + "`" + `"a"` + "`" + `}, []string{` + "`" + `"b"` + "`" + `})
	roundTrips[PickRefWide](t, []string{named, ` + "`" + `4294967295` + "`" + `}, []string{` + "`" + `7` + "`" + `})
	// An alias is the type of the entry that it names.
	roundTrips[PickAlias](t, []string{named, ` + "`" + `"x"` + "`" + `}, []string{` + "`" + `5` + "`" + `})
	roundTrips[PickRefAliasConst](t, []string{named, ` + "`" + `"a"` + "`" + `}, []string{` + "`" + `"b"` + "`" + `})
	// Consts beside an open string make an open enum under either keyword.
	roundTrips[PickOpen](t, []string{` + "`" + `"a"` + "`" + `, ` + "`" + `"zzz"` + "`" + `}, []string{` + "`" + `5` + "`" + `})
	if out, err := json.Marshal(PickOpenA); err != nil || string(out) != ` + "`" + `"a"` + "`" + ` {
		t.Errorf("PickOpenA encodes as %s, %v; want \"a\"", out, err)
	}
}
`

// oneOfConstructorsTest calls the constructor of each alternative that is not
// an object. It takes the value, or none when the alternative is a const.
const oneOfConstructorsTest = `package acp

import (
	"encoding/json"
	"testing"
)

func builds(t *testing.T, v interface{ Validate() error }, want string) {
	t.Helper()
	if err := v.Validate(); err != nil {
		t.Errorf("%T: Validate: %v", v, err)
	}
	if out, err := json.Marshal(v); err != nil || string(out) != want {
		t.Errorf("%T encodes as %s, %v; want %s", v, out, err, want)
	}
}

func ptr[T any](v T) *T { return &v }

func TestNewUnion_BuildsEveryKindOfAlternative(t *testing.T) {
	builds(t, ptr(NewPickStringPickStringVariant2("x")), ` + "`" + `"x"` + "`" + `)
	builds(t, ptr(NewPickTitledText("x")), ` + "`" + `"x"` + "`" + `)
	builds(t, ptr(NewPickConstPickConstVariant2()), ` + "`" + `"a"` + "`" + `)
	builds(t, ptr(NewPickConstPickConstVariant3()), ` + "`" + `"b"` + "`" + `)
	builds(t, ptr(NewPickUntypedConstPickUntypedConstVariant2()), ` + "`" + `"a"` + "`" + `)
	builds(t, ptr(NewPickUntypedConstPickUntypedConstVariant3()), ` + "`" + `1.5` + "`" + `)
	builds(t, ptr(NewPickUntypedConstPickUntypedConstVariant4()), ` + "`" + `true` + "`" + `)
	builds(t, ptr(NewPickEnumPickEnumVariant2("b")), ` + "`" + `"b"` + "`" + `)
	builds(t, ptr(NewPickUntypedEnumPickUntypedEnumVariant2("b")), ` + "`" + `"b"` + "`" + `)
	builds(t, ptr(NewPickNumberPickNumberVariant2(1.5)), ` + "`" + `1.5` + "`" + `)
	builds(t, ptr(NewPickIntegerPickIntegerVariant2(7)), ` + "`" + `7` + "`" + `)
	builds(t, ptr(NewPickWideConstPickWideConstVariant2()), ` + "`" + `4294967295` + "`" + `)
	builds(t, ptr(NewPickBooleanPickBooleanVariant2(true)), ` + "`" + `true` + "`" + `)
	builds(t, ptr(NewPickArrayPickArrayVariant2([]string{"a"})), ` + "`" + `["a"]` + "`" + `)
	builds(t, ptr(NewPickRefLabel("x")), ` + "`" + `"x"` + "`" + `)
	builds(t, ptr(NewPickRefConstLabel()), ` + "`" + `"a"` + "`" + `)
	builds(t, ptr(NewPickRefWideWide()), ` + "`" + `4294967295` + "`" + `)
	// An alias of a struct builds a struct, as its entry does.
	builds(t, ptr(NewPickAliasNamedAlias()), ` + "`" + `{"name":""}` + "`" + `)
	builds(t, ptr(NewPickAliasLabelAlias("x")), ` + "`" + `"x"` + "`" + `)
	builds(t, ptr(NewPickRefAliasConstLabelAlias()), ` + "`" + `"a"` + "`" + `)
}
`

func TestWriteTypesJen_UnionKeepsEveryKindOfAlternative(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		t.Run(keyword, func(t *testing.T) {
			extra := map[string]string{"roundtrip_test.go": roundTripTest, "kinds_test.go": alternativeKindsTest}
			if keyword == "oneOf" {
				extra["constructors_test.go"] = oneOfConstructorsTest
			}
			checkGenerated(t, fmt.Sprintf(alternativeKindsSchema, keyword), extra)
		})
	}
}

// realUnions are the $defs entries of schema.json whose alternatives are not
// objects, with the G2 transcript values of each: ElicitationContentValue
// holds the content members of a "create elicitation" response, and
// SessionConfigSelectOptions the options of a "select" config option.
var realUnions = map[string]func(msg map[string]any) []any{
	"ElicitationContentValue": func(msg map[string]any) []any {
		result, _ := msg["result"].(map[string]any)
		content, _ := result["content"].(map[string]any)
		if result["action"] != "accept" {
			return nil
		}
		var values []any
		for _, v := range content {
			values = append(values, v)
		}
		return values
	},
	"SessionConfigSelectOptions": func(msg map[string]any) []any {
		var values []any
		var walk func(any)
		walk = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				if v["type"] == "select" && v["options"] != nil {
					values = append(values, v["options"])
				}
				for _, member := range v {
					walk(member)
				}
			case []any:
				for _, item := range v {
					walk(item)
				}
			}
		}
		walk(msg)
		return values
	},
}

// The unions of schema.json whose alternatives are strings, numbers,
// booleans, and arrays have titles, under anyOf. Their recorded values must
// decode and encode back with the titles too, without them, and under oneOf.
func TestWriteTypesJen_RealUnionValuesRoundTripUnderEveryForm(t *testing.T) {
	var top struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "schema", "schema.json"))
	if err != nil {
		t.Fatalf("read schema.json: %v", err)
	}
	if err := json.Unmarshal(src, &top); err != nil {
		t.Fatalf("parse schema.json: %v", err)
	}
	values := map[string][]string{}
	for _, name := range sortedKeys(realUnions) {
		for _, v := range transcriptValues(t, realUnions[name]) {
			values[name] = append(values[name], "`"+v+"`")
		}
		if len(values[name]) == 0 {
			t.Fatalf("the G2 transcripts hold no value of %s", name)
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		t.Run(keyword, func(t *testing.T) {
			defs := map[string]json.RawMessage{}
			var calls strings.Builder
			for _, name := range sortedKeys(realUnions) {
				addWithRefs(t, top.Defs, defs, name)
				for _, form := range []struct {
					suffix string
					titled bool
				}{{"Titled", true}, {"Untitled", false}} {
					defs[name+form.suffix] = realUnionForm(t, top.Defs[name], keyword, form.titled)
					fmt.Fprintf(&calls, "\troundTrips[%s%s](t, []string{%s}, nil)\n", name, form.suffix, strings.Join(values[name], ", "))
				}
			}
			schema, err := json.Marshal(map[string]any{"$defs": defs})
			if err != nil {
				t.Fatalf("encode the schema: %v", err)
			}
			test := "package acp\n\nimport \"testing\"\n\nfunc TestRealValues_RoundTrip(t *testing.T) {\n" + calls.String() + "}\n"
			checkGenerated(t, string(schema), map[string]string{"roundtrip_test.go": roundTripTest, "real_test.go": test})
		})
	}
}

// realUnionForm returns the union def with its anyOf alternatives under
// keyword, and without their titles unless titled.
func realUnionForm(t *testing.T, def json.RawMessage, keyword string, titled bool) json.RawMessage {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(def, &d); err != nil {
		t.Fatalf("parse the union: %v", err)
	}
	alternatives, _ := d["anyOf"].([]any)
	if len(alternatives) == 0 {
		t.Fatalf("the union %s has no anyOf", def)
	}
	for _, alt := range alternatives {
		if !titled {
			delete(alt.(map[string]any), "title")
		}
	}
	delete(d, "anyOf")
	d[keyword] = alternatives
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("encode the union: %v", err)
	}
	return out
}

var defRef = regexp.MustCompile(`"#/\$defs/([^"]+)"`)

// addWithRefs copies the $defs entry name of all into defs, with each entry
// that it refers to.
func addWithRefs(t *testing.T, all, defs map[string]json.RawMessage, name string) {
	t.Helper()
	if defs[name] != nil {
		return
	}
	if all[name] == nil {
		t.Fatalf("schema.json has no $defs entry %s", name)
	}
	defs[name] = all[name]
	for _, m := range defRef.FindAllStringSubmatch(string(all[name]), -1) {
		addWithRefs(t, all, defs, m[1])
	}
}

// transcriptValues returns, as JSON, the values that pick finds in the
// messages of the G2 transcripts.
func transcriptValues(t *testing.T, pick func(map[string]any) []any) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "..", ".agents", "skills", "verify-schema-bump", "roundtrip", "testdata", "transcripts", "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("find the G2 transcripts: %v", err)
	}
	seen := map[string]bool{}
	var values []string
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			t.Fatalf("open %s: %v", file, err)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(nil, 16<<20)
		for scanner.Scan() {
			var line struct {
				Msg map[string]any `json:"msg"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
				t.Fatalf("parse a line of %s: %v", file, err)
			}
			for _, v := range pick(line.Msg) {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatalf("encode a value of %s: %v", file, err)
				}
				if s := string(b); !seen[s] && !strings.Contains(s, "`") {
					seen[s] = true
					values = append(values, s)
				}
			}
		}
		f.Close()
		if err := scanner.Err(); err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
	}
	sort.Strings(values)
	return values
}

// A const or enum value that the Go type of its union alternative cannot
// hold exactly stops the generator. Without a literal for it, the decoder
// would take every value of the type, a rounded value, or not the value
// itself: it would take values that the schema rejects or reject one that it
// takes. A number of magnitude 2^53 or more may be rounded when it loads,
// and an int beyond int32 does not compile on a 32-bit target.
func TestWriteTypesJen_PanicsOnAValueTheAlternativeCannotHold(t *testing.T) {
	for _, tc := range []struct{ name, alternative, value string }{
		{"string const under integer", `{"type": "integer", "const": "a"}`, `"a"`},
		{"fraction under integer", `{"type": "integer", "const": 1.5}`, `1.5`},
		{"int above int32", `{"type": "integer", "const": 4294967295}`, `4294967295`},
		{"int32 above int32", `{"type": "integer", "format": "int32", "const": 2147483648}`, `2147483648`},
		{"uint16 above uint16", `{"type": "integer", "format": "uint16", "const": 65536}`, `65536`},
		{"negative uint32", `{"type": "integer", "format": "uint32", "const": -1}`, `-1`},
		{"negative uint64 enum member", `{"type": "integer", "format": "uint64", "enum": [1, -1]}`, `-1`},
		{"const on a uint32 $ref above uint32", `{"const": 4294967296, "allOf": [{"$ref": "#/$defs/Count"}]}`, `4294967296`},
		{"null enum member", `{"type": "string", "enum": ["a", null]}`, `null`},
		{"array const", `{"type": "array", "items": {"type": "integer"}, "const": [1]}`, `[1]`},
		{"object const", `{"type": "object", "const": {"a": 1}}`, `{"a":1}`},
		{"number const on a string $ref", `{"const": 5, "allOf": [{"$ref": "#/$defs/Label"}]}`, `5`},
		{"const on an untyped $ref", `{"const": "a", "allOf": [{"$ref": "#/$defs/Raw"}]}`, `"a"`},
		{"number const on an integer enum $ref", `{"const": 2, "allOf": [{"$ref": "#/$defs/Levels"}]}`, `2`},
		{"integer above 2^53", `{"type": "integer", "const": 9007199254740993}`, `9007199254740992`},
		{"untyped integer above 2^53", `{"const": 9007199254740993}`, `9007199254740992`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var schema load.Schema
			in := `{"$defs": {
  "Named": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
  "Label": {"type": "string"},
  "Raw": {"description": "Any JSON value."},
  "Levels": {"type": "integer", "enum": [1, 2]},
  "Count": {"type": "integer", "format": "uint32"},
  "Pick": {"anyOf": [{"$ref": "#/$defs/Named"}, ` + tc.alternative + `]}
}}`
			if err := json.Unmarshal([]byte(in), &schema); err != nil {
				t.Fatal(err)
			}
			defer func() {
				r := recover()
				if msg, _ := r.(string); !strings.Contains(msg, `"Pick"`) || !strings.Contains(msg, tc.value) {
					t.Errorf("WriteTypesJen recovered %v, want a panic that names Pick and the value %s", r, tc.value)
				}
			}()
			_ = WriteTypesJen(t.TempDir(), &schema, &load.Meta{})
		})
	}
}
