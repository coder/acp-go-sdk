// Package roundtrip is gate G2 of the verify-schema-bump skill. It decodes
// ACP messages into the SDK's Go types, encodes them again and checks that
// nothing was lost and that the output is valid against the schema. The
// SDK is the checkout four levels up (go.mod replaces it), and the schema
// is that checkout's schema/ folder.
//
// Each problem is one t.Errorf line that starts with a fixed token, so
// scripts/g2-roundtrip can list them:
//
//	NO-TYPE   the schema def has no SDK Go type
//	DECODE    json.Unmarshal into the SDK type failed on a schema-valid value
//	VALIDATE  the SDK type's Validate method rejected a schema-valid value
//	LOSSY     the re-encoded JSON differs from the input
//	SCHEMA    the schema accepts the input but not the re-encoded JSON
//	HINT      the SDK does not honor an x-deserialize-* hint
package roundtrip

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const sdkRoot = "../../../.."

// schemaFile is one of schema/schema.json and schema/schema.unstable.json.
type schemaFile struct {
	name     string
	url      string
	defs     map[string]any // raw $defs, numbers kept as json.Number
	compiler *jsonschema.Compiler
	compiled map[string]*jsonschema.Schema
}

type suite struct {
	files []*schemaFile
	// methodDefs maps a wire method to its defs by kind: "Request",
	// "Response" or "Notification".
	methodDefs map[string]map[string]string
}

var (
	loadOnce   sync.Once
	loaded     *suite
	loadFailed error
)

func load(t *testing.T) *suite {
	t.Helper()
	loadOnce.Do(func() { loaded, loadFailed = loadSuite() })
	if loadFailed != nil {
		t.Fatal(loadFailed)
	}
	return loaded
}

func loadSuite() (*suite, error) {
	s := &suite{methodDefs: map[string]map[string]string{}}
	for _, f := range []struct{ name, file string }{{"stable", "schema.json"}, {"unstable", "schema.unstable.json"}} {
		b, err := os.ReadFile(filepath.Join(sdkRoot, "schema", f.file))
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var raw struct {
			Defs map[string]any `json:"$defs"`
		}
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("%s: %w", f.file, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.file, err)
		}
		sf := &schemaFile{
			name:     f.name,
			url:      "file:///" + f.file,
			defs:     raw.Defs,
			compiler: jsonschema.NewCompiler(),
			compiled: map[string]*jsonschema.Schema{},
		}
		if err := sf.compiler.AddResource(sf.url, doc); err != nil {
			return nil, err
		}
		s.files = append(s.files, sf)
		for def, node := range raw.Defs {
			m, _ := node.(map[string]any)
			method, _ := m["x-method"].(string)
			for _, kind := range []string{"Request", "Response", "Notification"} {
				if method != "" && strings.HasSuffix(def, kind) {
					if s.methodDefs[method] == nil {
						s.methodDefs[method] = map[string]string{}
					}
					s.methodDefs[method][kind] = def
				}
			}
		}
	}
	return s, nil
}

// validate checks data against $defs/def. It returns a one-line error.
func (f *schemaFile) validate(def string, data []byte) error {
	sch, ok := f.compiled[def]
	if !ok {
		var err error
		sch, err = f.compiler.Compile(f.url + "#/$defs/" + def)
		if err != nil {
			return err
		}
		f.compiled[def] = sch
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if err := sch.Validate(v); err != nil {
		return fmt.Errorf("%s", oneLine(err.Error(), 300))
	}
	return nil
}

// checkRoundTrip decodes data into the SDK type for def, runs its Validate
// method when it has one, encodes it again, and checks the result against
// data. The result must also be valid in each file of files whose $defs/def
// accepts data: an input that the schema rejects says nothing about the SDK.
func checkRoundTrip(t *testing.T, def string, data []byte, files []*schemaFile) {
	t.Helper()
	newValue, ok := registry[def]
	if !ok {
		t.Errorf("NO-TYPE %s", def)
		return
	}
	v := newValue()
	if err := json.Unmarshal(data, v); err != nil {
		t.Errorf("DECODE %s: %v", def, oneLine(err.Error(), 200))
		return
	}
	if val, ok := v.(interface{ Validate() error }); ok {
		if err := val.Validate(); err != nil {
			t.Errorf("VALIDATE %s: %v", def, oneLine(err.Error(), 200))
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Errorf("DECODE %s: re-encode: %v", def, err)
		return
	}
	if d := diffJSON(data, out); d != "" {
		t.Errorf("LOSSY %s: %s", def, d)
	}
	for _, f := range files {
		if _, ok := f.defs[def]; !ok || f.validate(def, data) != nil {
			continue
		}
		if err := f.validate(def, out); err != nil {
			t.Errorf("SCHEMA %s (%s): %v", def, f.name, err)
		}
	}
}

// diffJSON compares two JSON documents after normalizing them: numbers
// compare by exact value (1.0 equals 1, and no digit is rounded away) and an
// object member whose value is null counts as absent. It returns "" when
// they are equal, else the number of differing paths and the first one.
func diffJSON(in, out []byte) string {
	a, err := decodeExact(in)
	if err != nil {
		return "input: " + err.Error()
	}
	b, err := decodeExact(out)
	if err != nil {
		return "output: " + err.Error()
	}
	var diffs []string
	walkDiff("", dropNulls(a), dropNulls(b), &diffs)
	if len(diffs) == 0 {
		return ""
	}
	return fmt.Sprintf("%d difference(s), first %s", len(diffs), diffs[0])
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

func dropNulls(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			if x != nil {
				out[k] = dropNulls(x)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = dropNulls(x)
		}
		return out
	}
	return v
}

func short(v any) string {
	if v == nil {
		return "<absent>"
	}
	b, _ := json.Marshal(v)
	return oneLine(string(b), 120)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDiffJSON_ComparesNumbersExactly(t *testing.T) {
	for _, tc := range []struct {
		in, out string
		equal   bool
	}{
		{`{"a":9007199254740993}`, `{"a":9007199254740992}`, false},
		{`{"a":1.0}`, `{"a":1}`, true},
		{`{"a":1}`, `{"a":"1"}`, false},
	} {
		if got := diffJSON([]byte(tc.in), []byte(tc.out)) == ""; got != tc.equal {
			t.Errorf("diffJSON(%s, %s) equal = %v, want %v", tc.in, tc.out, got, tc.equal)
		}
	}
}
