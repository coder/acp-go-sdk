package emit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// TestWriteTypesJen_RejectsARefLoop checks that a chain of $defs entries that
// refer back to the first one in place, through $ref, allOf, anyOf, oneOf or
// not, fails each writer that walks the schema with an error that names the
// chain. JSON Schema 2020-12 leaves such a schema undefined (section 9.4.1).
// Each case reaches a different walker that follows the chain forever: the
// check of null on a member of a shape (fitsNull), the merge of an allOf
// variant (expandAllOf), the check of null on a variant's required key
// (acceptsNull), and the walker of the generated shapes at decode time
// (shape.fits). A type that refers to itself through a member or an item, as
// a tree does, is no loop.
func TestWriteTypesJen_RejectsARefLoop(t *testing.T) {
	tests := []struct{ name, defs, loop string }{
		{"$ref", `"Alpha": {"$ref": "#/$defs/Beta"}, "Beta": {"$ref": "#/$defs/Alpha"},
  "Holder": {"type": "object", "properties": {"a": {"type": "object", "properties": {"x": {"$ref": "#/$defs/Alpha"}},
    "x-deserialize-default-on-error": true}}}`, "Alpha -> Beta -> Alpha"},
		{"allOf", `"Alpha": {"allOf": [{"$ref": "#/$defs/Alpha"}]},
  "Union": {"oneOf": [{"allOf": [{"$ref": "#/$defs/Alpha"}]}, {"type": "string"}]}`, "Alpha -> Alpha"},
		{"anyOf and oneOf", `"Alpha": {"anyOf": [{"$ref": "#/$defs/Beta"}, {"type": "null"}]},
  "Beta": {"oneOf": [{"$ref": "#/$defs/Alpha"}, {"type": "string"}]},
  "Union": {"oneOf": [{"type": "object", "required": ["k"], "properties": {"k": {"$ref": "#/$defs/Alpha"}}}, {"type": "string"}]}`, "Alpha -> Beta -> Alpha"},
		{"not", `"Alpha": {"type": "object", "not": {"$ref": "#/$defs/Alpha"}},
  "Holder": {"type": "object", "properties": {"a": {"$ref": "#/$defs/Alpha", "x-deserialize-default-on-error": true}}}`, "Alpha -> Alpha"},
		{"a tree", `"Alpha": {"type": "object", "properties": {
    "parent": {"anyOf": [{"$ref": "#/$defs/Alpha"}, {"type": "null"}]},
    "children": {"type": "array", "items": {"$ref": "#/$defs/Alpha"}},
    "named": {"type": "object", "additionalProperties": {"$ref": "#/$defs/Alpha"}}}},
  "Holder": {"type": "object", "properties": {"a": {"$ref": "#/$defs/Alpha", "x-deserialize-default-on-error": true}}},
  "Union": {"oneOf": [{"type": "object", "required": ["k"], "properties": {"k": {"$ref": "#/$defs/Alpha"}}}, {"type": "string"}]}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var schema load.Schema
			if err := json.Unmarshal([]byte(`{"$defs": {`+tt.defs+`}}`), &schema); err != nil {
				t.Fatal(err)
			}
			for _, w := range []struct {
				name  string
				write func(string, *load.Schema, *load.Meta) error
			}{{"WriteTypesJen", WriteTypesJen}, {"WriteHelpersJen", WriteHelpersJen}} {
				dir := t.TempDir()
				done := make(chan error, 1)
				go func() { done <- w.write(dir, &schema, &load.Meta{}) }()
				var err error
				select {
				case err = <-done:
				case <-time.After(10 * time.Second):
					t.Fatalf("%s did not return within 10s", w.name)
				}
				switch {
				case tt.loop == "" && err != nil:
					t.Errorf("%s: %v", w.name, err)
				case tt.loop != "" && (err == nil || !strings.Contains(err.Error(), tt.loop)):
					t.Errorf("%s returned %v, want an error naming %s", w.name, err, tt.loop)
				}
			}
		})
	}
}
