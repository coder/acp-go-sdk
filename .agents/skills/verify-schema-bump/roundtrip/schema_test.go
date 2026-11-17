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
//	UNKNOWN   (logged, not an error) the SDK dropped an input member that the
//	          schema does not define
//	SCHEMA    the schema accepts the input but not the re-encoded JSON
//	HINT      the SDK does not honor an x-deserialize-* hint
package roundtrip

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	c, err := compareJSON(data, out, defRefs(def, files))
	if err != nil {
		t.Errorf("LOSSY %s: %v", def, err)
		return
	}
	if len(c.lost) > 0 {
		t.Errorf("LOSSY %s: %s", def, describe(c.lost))
	}
	if len(c.unknown) > 0 {
		t.Logf("UNKNOWN %s: dropped %s, not defined in this schema version", def, strings.Join(c.unknown, " "))
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
