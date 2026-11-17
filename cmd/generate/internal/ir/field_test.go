package ir

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

func parseObject(t *testing.T, schemaJSON string) *load.Definition {
	t.Helper()
	var def load.Definition
	if err := json.Unmarshal([]byte(schemaJSON), &def); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return &def
}

func TestFields_ReadsNamesAndRequired(t *testing.T) {
	def := parseObject(t, `{"type": "object", "required": ["sessionId"], "properties": {
		"sessionId": {"type": "string"},
		"_meta": {"type": ["object", "null"]},
		"mcpServers": {"type": "array", "items": {"type": "string"}}
	}}`)

	type summary struct {
		Prop, Name string
		Required   bool
	}
	var got []summary
	for _, f := range Fields(def.Properties, def.Required) {
		if f.Def != def.Properties[f.Prop] {
			t.Errorf("field %q does not point at its property schema", f.Prop)
		}
		got = append(got, summary{f.Prop, f.Name, f.Required})
	}
	want := []summary{
		{"_meta", "Meta", false},
		{"mcpServers", "McpServers", false},
		{"sessionId", "SessionId", true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Fields() =\n %+v\nwant, sorted by property:\n %+v", got, want)
	}
}
