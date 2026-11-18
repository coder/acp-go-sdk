// Package roundtrip is gate G2 of the verify-schema-bump skill. It decodes
// ACP messages into the SDK's Go types, encodes them again and checks that
// nothing was lost and that the output is valid against the schema. The
// SDK is the checkout four levels up (go.mod replaces it), and the schema
// is that checkout's schema/ folder.
//
// Each problem is one t.Errorf line "TOKEN KEY: DETAIL". The key names the
// def and the property or path that the problem is about, and never the
// error text, so scripts/g2-roundtrip lists the keys in failures.txt and the
// whole lines in details.txt, and two runs diff by key. The tokens:
//
//	NO-TYPE   the schema def has no SDK Go type
//	DECODE    json.Unmarshal into the SDK type failed on a schema-valid value
//	VALIDATE  the SDK type's Validate method rejected a schema-valid value
//	LOSSY     the re-encoded JSON differs from the input
//	UNKNOWN   (logged, not an error) the SDK dropped an input member that the
//	          schema does not define
//	SCHEMA    the schema accepts the input but not the re-encoded JSON
//	HINT      the SDK does not honor an x-deserialize-* hint
package roundtrip

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
		sf, err := newSchemaFile(f.name, f.file, b)
		if err != nil {
			return nil, err
		}
		s.files = append(s.files, sf)
		for def, node := range sf.defs {
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

// newSchemaFile parses one schema document named file.
func newSchemaFile(name, file string, b []byte) (*schemaFile, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var raw struct {
		Defs map[string]any `json:"$defs"`
	}
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	sf := &schemaFile{
		name:     name,
		url:      "file:///" + file,
		defs:     raw.Defs,
		compiler: jsonschema.NewCompiler(),
		compiled: map[string]*jsonschema.Schema{},
	}
	if err := sf.compiler.AddResource(sf.url, doc); err != nil {
		return nil, err
	}
	return sf, nil
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
		return schemaError{msg: oneLine(err.Error(), 300), at: firstFailure(err)}
	}
	return nil
}

// schemaError is a one-line validation error that keeps where in the value
// the first rule failed.
type schemaError struct{ msg, at string }

func (e schemaError) Error() string { return e.msg }

// firstFailure returns the location in the value of the first rule that err
// reports, following the first cause down: "/" for the value itself.
func firstFailure(err error) string {
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return ""
	}
	for len(verr.Causes) > 0 {
		verr = verr.Causes[0]
	}
	return "/" + strings.Join(verr.InstanceLocation, "/")
}

// decodeKey names the property that a decode error is about, when it names
// one: the Field of a *json.UnmarshalTypeError.
func decodeKey(def string, err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return def + " " + typeErr.Field
	}
	return def
}

// requiredMessage matches the error of a generated Validate method for a
// missing property.
var requiredMessage = regexp.MustCompile(`^(\S+) is required$`)

// validateKey names the property that a Validate error is about, when it
// names one.
func validateKey(def string, err error) string {
	if m := requiredMessage.FindStringSubmatch(err.Error()); m != nil {
		return def + " " + m[1]
	}
	return def
}

// checkRoundTrip decodes data into the SDK type for def, runs its Validate
// method when it has one, encodes it again, and checks the result against
// data (see compareJSON). The result must also be valid in each file of
// files whose $defs/def accepts data: an input that the schema rejects says
// nothing about the SDK.
func checkRoundTrip(t *testing.T, def string, data []byte, files []*schemaFile) {
	t.Helper()
	newValue, ok := registry[def]
	if !ok {
		t.Errorf("NO-TYPE %s", def)
		return
	}
	v := newValue()
	if err := json.Unmarshal(data, v); err != nil {
		t.Errorf("DECODE %s: %v", decodeKey(def, err), oneLine(err.Error(), 200))
		return
	}
	if val, ok := v.(interface{ Validate() error }); ok {
		if err := val.Validate(); err != nil {
			t.Errorf("VALIDATE %s: %v", validateKey(def, err), oneLine(err.Error(), 200))
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Errorf("DECODE %s re-encode: %v", def, err)
		return
	}
	c, err := compareJSON(data, out, defRefs(def, files))
	if err != nil {
		t.Errorf("LOSSY %s compare: %v", def, err)
		return
	}
	if len(c.lost) > 0 {
		t.Errorf("LOSSY %s %s: %s", def, strings.Join(diffPaths(c.lost), " "), describe(c.lost))
	}
	if len(c.unknown) > 0 {
		t.Logf("UNKNOWN %s %s: dropped, not defined in this schema version", def, strings.Join(c.unknown, " "))
	}
	for _, f := range files {
		if _, ok := f.defs[def]; !ok || f.validate(def, data) != nil {
			continue
		}
		if err := f.validate(def, out); err != nil {
			var at string
			if serr, ok := err.(schemaError); ok {
				at = " " + serr.at
			}
			t.Errorf("SCHEMA %s (%s)%s: %v", def, f.name, at, err)
		}
	}
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

// The key of a problem line names the property or the path, never the
// error text, so that a changed message keeps the key.
func TestProblemKeys_NameThePropertyNotTheMessage(t *testing.T) {
	var v struct {
		A struct {
			B int `json:"b"`
		} `json:"a"`
	}
	decodeErr := json.Unmarshal([]byte(`{"a":{"b":"x"}}`), &v)
	f, err := newSchemaFile("test", "test.json", []byte(`{"$defs": {"D": {"type": "object", "properties": {
		"a": {"type": "object", "properties": {"b": {"type": "integer"}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	schemaErr, _ := f.validate("D", []byte(`{"a":{"b":"x"}}`)).(schemaError)
	for _, tc := range []struct{ got, want string }{
		{decodeKey("D", decodeErr), "D a.b"},
		{decodeKey("D", errors.New("no matching variant for union")), "D"},
		{validateKey("D", errors.New("sessions is required")), "D sessions"},
		{validateKey("D", errors.New("D must have exactly one variant set")), "D"},
		{schemaErr.at, "/a/b"},
	} {
		if tc.got != tc.want {
			t.Errorf("key = %q, want %q", tc.got, tc.want)
		}
	}
}
