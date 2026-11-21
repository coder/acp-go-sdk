package load

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestReadSchemaVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{name: "trailing newline", content: "1.32.0\n", want: "1.32.0"},
		{name: "empty", content: "\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "version"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := ReadSchemaVersion(dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReadSchemaVersion() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ReadSchemaVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefinition_UnmarshalJSON_UnevaluatedPropertiesIsSetOnlyByTrue(t *testing.T) {
	tests := []struct {
		schema string
		want   bool
	}{
		{`{"type": "object", "unevaluatedProperties": true}`, true},
		{`{"type": "object", "unevaluatedProperties": false}`, false},
		{`{"type": "object", "unevaluatedProperties": {"type": "string"}}`, false},
		{`{"type": "object", "unevaluatedProperties": "true"}`, false},
		{`{"type": "object"}`, false},
	}
	for _, tt := range tests {
		var def Definition
		if err := json.Unmarshal([]byte(tt.schema), &def); err != nil {
			t.Fatalf("parse %s: %v", tt.schema, err)
		}
		if got := bool(def.UnevaluatedProperties); got != tt.want {
			t.Errorf("UnevaluatedProperties of %s = %v, want %v", tt.schema, got, tt.want)
		}
	}
}

func TestDefinition_UnmarshalJSON_ReadsAdditionalProperties(t *testing.T) {
	tests := []struct {
		schema     string
		open       bool
		schemaType any
	}{
		{`{"type": "object", "additionalProperties": true}`, true, nil},
		{`{"type": "object", "additionalProperties": false}`, false, nil},
		{`{"type": "object", "additionalProperties": {"type": "string"}}`, false, "string"},
		{`{"type": "object"}`, false, nil},
	}
	for _, tt := range tests {
		var def Definition
		if err := json.Unmarshal([]byte(tt.schema), &def); err != nil {
			t.Fatalf("parse %s: %v", tt.schema, err)
		}
		got := def.AdditionalProperties
		var gotType any
		if got.Schema != nil {
			gotType = got.Schema.Type
		}
		if got.Open != tt.open || gotType != tt.schemaType {
			t.Errorf("AdditionalProperties of %s = open %v, schema type %v; want open %v, schema type %v", tt.schema, got.Open, gotType, tt.open, tt.schemaType)
		}
	}
}

func TestDefinition_Unmodeled_ListsTheKeywordsThatNoFieldHolds(t *testing.T) {
	tests := []struct {
		schema string
		want   []string
	}{
		{`{"type": "string", "pattern": "x", "maxLength": 3}`, []string{"maxLength", "pattern"}},
		{`{"const": null}`, []string{"const"}},
		{`{"type": "object", "additionalProperties": false, "unevaluatedProperties": false}`, []string{"additionalProperties", "unevaluatedProperties"}},
		{`{"const": 0, "required": [], "minimum": 0, "additionalProperties": true, "not": {}}`, nil},
		{`{"title": "", "description": "d", "$comment": "c", "examples": [1], "default": null, "x-docs-ignore": false, "x-other": 1}`, nil},
		{`{}`, nil},
		{`true`, nil},
	}
	for _, tt := range tests {
		var def Definition
		if err := json.Unmarshal([]byte(tt.schema), &def); err != nil {
			t.Fatalf("parse %s: %v", tt.schema, err)
		}
		if got := def.Unmodeled(); !slices.Equal(got, tt.want) {
			t.Errorf("Unmodeled of %s = %q, want %q", tt.schema, got, tt.want)
		}
	}
}
