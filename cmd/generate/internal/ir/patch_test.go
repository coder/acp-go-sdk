package ir

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// The descriptions mark patch fields in three ways: the object says that its
// optional properties are all patch fields, the object names them in a "have
// patch semantics" sentence, or the property says that omitted means unchanged
// or that null clears it. A required property is never one.
func TestPatchFields_ReadsTheDescriptions(t *testing.T) {
	var schema load.Schema
	if err := json.Unmarshal([]byte(`{"$defs": {
		"Update": {"description": "All fields except the id are optional - only changed fields need to be included.", "type": "object", "required": ["id"],
			"properties": {"id": {"type": "string"}, "title": {"type": ["string", "null"]}}},
		"Info": {"description": "All fields are optional to support partial updates.", "type": "object",
			"properties": {"title": {"type": ["string", "null"]}}},
		"Named": {"description": "`+"`summary` and `_meta` have patch semantics: omission leaves it unchanged."+`", "type": "object",
			"properties": {"summary": {"type": ["array", "null"]}, "_meta": {"type": ["object", "null"]}, "status": {"type": "string"}}},
		"Own": {"type": "object", "properties": {
			"cwd": {"type": ["string", "null"], "description": "Omitted means unchanged. `+"`null`"+` clears it."},
			"title": {"type": ["string", "null"], "description": "Set to null to clear."},
			"meta": {"type": ["object", "null"], "description": "Omitted leaves metadata unchanged. `+"`null`"+` removes it."},
			"name": {"type": ["string", "null"], "description": "The name."}}},
		"Plain": {"description": "A request.", "type": "object", "properties": {"title": {"type": ["string", "null"]}}}
	}}`), &schema); err != nil {
		t.Fatal(err)
	}
	patch := PatchFields(&schema)
	var got []string
	for name, def := range schema.Defs {
		for prop, d := range def.Properties {
			if patch[d] {
				got = append(got, name+"."+prop)
			}
		}
	}
	sort.Strings(got)
	const want = "Info.title Named._meta Named.summary Own.cwd Own.meta Own.title Update.title"
	if strings.Join(got, " ") != want {
		t.Errorf("patch fields = %v, want %s", got, want)
	}
}

// A field of an object that says only that it carries the changed fields,
// and whose own description says nothing about null, has no clear: ACP v1
// reads null there as absent.
func TestNullAsAbsent_ReadsTheDescriptions(t *testing.T) {
	var schema load.Schema
	if err := json.Unmarshal([]byte(`{"$defs": {
		"Update": {"description": "All fields except the id are optional - only changed fields need to be included.", "type": "object", "required": ["id"],
			"properties": {"id": {"type": "string"}, "title": {"type": ["string", "null"]},
				"name": {"type": ["string", "null"], "description": "Omitting it or sending `+"`null`"+` both leave the name unchanged."}}},
		"Info": {"description": "All fields are optional to support partial updates.", "type": "object",
			"properties": {"title": {"type": ["string", "null"], "description": "Set to null to clear."}, "_meta": {"type": ["object", "null"]}}},
		"Sub": {"description": "Omitted patch fields keep their previous values.", "type": "object",
			"properties": {"cwd": {"type": ["string", "null"]}}},
		"Own": {"type": "object", "properties": {
			"cwd": {"type": ["string", "null"], "description": "Omitted means unchanged."}}}
	}}`), &schema); err != nil {
		t.Fatal(err)
	}
	keep := NullAsAbsent(&schema)
	var got []string
	for name, def := range schema.Defs {
		for prop, d := range def.Properties {
			if keep[d] {
				got = append(got, name+"."+prop)
			}
		}
	}
	sort.Strings(got)
	const want = "Info._meta Update.title"
	if strings.Join(got, " ") != want {
		t.Errorf("null-as-absent fields = %v, want %s", got, want)
	}
}
