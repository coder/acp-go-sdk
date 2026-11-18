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

func TestFields_ReadsNamesRequiredAndHints(t *testing.T) {
	def := parseObject(t, `{"type": "object", "required": ["sessionId"], "properties": {
		"sessionId": {"type": "string"},
		"_meta": {"type": ["object", "null"], "x-deserialize-default-on-error": true},
		"entries": {"type": "array", "items": {"type": "string"},
			"x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true},
		"mcpServers": {"type": "array", "items": {"type": "string"}, "x-deserialize-default-on-null": true}
	}}`)

	type summary struct {
		Prop, Name string
		Required   bool
		Lenient    Lenient
	}
	var got []summary
	for _, f := range Fields(def.Properties, def.Required) {
		if f.Def != def.Properties[f.Prop] {
			t.Errorf("field %q does not point at its property schema", f.Prop)
		}
		got = append(got, summary{f.Prop, f.Name, f.Required, f.Lenient})
	}
	want := []summary{
		{"_meta", "Meta", false, Lenient{DefaultOnError: true}},
		{"entries", "Entries", false, Lenient{DefaultOnError: true, SkipInvalidItems: true}},
		{"mcpServers", "McpServers", false, Lenient{DefaultOnNull: true}},
		{"sessionId", "SessionId", true, Lenient{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Fields() =\n %+v\nwant, sorted by property:\n %+v", got, want)
	}
}

func TestFields_PanicsOnSkipInvalidItemsOutsideAnArray(t *testing.T) {
	def := parseObject(t, `{"type": "object", "properties": {
		"title": {"type": "string", "x-deserialize-skip-invalid-items": true}
	}}`)
	defer func() {
		if recover() == nil {
			t.Errorf("Fields() did not panic for a string property with x-deserialize-skip-invalid-items")
		}
	}()
	Fields(def.Properties, def.Required)
}

func TestFields_SkipsAnOptionalPropertyWhoseTypeIsOnlyNull(t *testing.T) {
	def := parseObject(t, `{"type": "object", "required": ["marker"], "properties": {
		"marker": {"type": "null"},
		"stopReason": {"type": "null"},
		"usage": {"type": ["integer", "null"]}
	}}`)
	var got []string
	for _, f := range Fields(def.Properties, def.Required) {
		got = append(got, f.Prop)
	}
	if want := []string{"marker", "usage"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Fields() has the properties %v, want %v: the required marker, and not the optional stopReason", got, want)
	}
}
