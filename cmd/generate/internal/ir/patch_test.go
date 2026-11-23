package ir

import (
	"encoding/json"
	"path/filepath"
	"regexp"
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
// and whose own description does not say that null clears it, has no clear:
// ACP v1 reads null there as absent, also where the description says so, as
// it does for name of ToolCallUpdate. So has a patch field of any object
// whose own description says that omitted and null are equivalent, as anchor
// of CursorUpdate does, but not a field that is no patch field.
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
			"cwd": {"type": ["string", "null"], "description": "Omitted means unchanged."}}},
		"Cursor": {"description": "`+"`caps` and `_meta` have patch semantics: omission leaves the stored value unchanged, `null` clears it."+`", "type": "object",
			"properties": {"caps": {"type": ["object", "null"]}, "_meta": {"type": ["object", "null"]},
				"anchor": {"type": ["object", "null"], "description": "Omitted and `+"`null`"+` are\nequivalent and leave the stored anchor unchanged."}}},
		"Plain": {"type": "object", "properties": {
			"title": {"type": ["string", "null"], "description": "Omitted and `+"`null`"+` are equivalent and mean no title."}}}
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
	const want = "Cursor.anchor Info._meta Update.name Update.title"
	if strings.Join(got, " ") != want {
		t.Errorf("null-as-absent fields = %v, want %s", got, want)
	}
}

// mentionsNull and saysClear read a patch field's own description for the
// guard below. saysClear is wider than nullClears: "`null` or `[]` clears
// it" (content of SessionMessage) says that null clears too.
var (
	mentionsNull = regexp.MustCompile(`(?i)\bnull\b`)
	saysClear    = regexp.MustCompile("(?i)\\bnull`?(?: or `[^`]*`)? (?:clears|removes)\\b|set to null to clear")
)

// nullTextUnread returns each patch field of schema whose own description
// speaks of null, that NullAsAbsent leaves out, so that the SDK reads null
// as a clear, and whose description does not say that null clears it. Such
// text says something about null that neither rule reads, as a reworded
// anchor of CursorUpdate ("Omitting it or sending `null` both leave the
// stored anchor unchanged") would.
func nullTextUnread(schema *load.Schema) []string {
	patch, absent := PatchFields(schema), NullAsAbsent(schema)
	var out []string
	for name, def := range schema.Defs {
		if def == nil {
			continue
		}
		for prop, d := range def.Properties {
			if patch[d] && !absent[d] && mentionsNull.MatchString(d.Description) && !saysClear.MatchString(d.Description) {
				out = append(out, name+"."+prop)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Every patch field of the checkout's schema that speaks of null says that
// null clears it, or NullAsAbsent reads its null as absent. A schema whose
// text says otherwise fails here, so that a reword cannot turn a null that
// means "unchanged" into a clear without notice. Update NullAsAbsent and the
// copy of its rule in .agents/skills/verify-schema-bump/roundtrip/patches_test.go.
func TestNullAsAbsent_ReadsEveryNullTextOfTheSchema(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "..", "schema")
	stable, err := load.ReadSchema(dir)
	if err != nil {
		t.Fatal(err)
	}
	unstable, found, err := load.ReadSchemaUnstable(dir)
	if err != nil || !found {
		t.Fatalf("read schema.unstable.json: found %v, %v", found, err)
	}
	for file, schema := range map[string]*load.Schema{"schema.json": stable, "schema.unstable.json": unstable} {
		for _, field := range nullTextUnread(schema) {
			t.Errorf("%s: the patch field %s speaks of null, does not say that null clears it, and NullAsAbsent reads its null as a clear", file, field)
		}
	}
}

// The guard finds a reworded null-means-unchanged field and nothing that a
// rule reads.
func TestNullTextUnread_FindsTextThatNoRuleReads(t *testing.T) {
	var schema load.Schema
	if err := json.Unmarshal([]byte(`{"$defs": {
		"Cursor": {"description": "`+"`caps` and `_meta` have patch semantics: omission leaves the stored value unchanged, `null` clears it."+`", "type": "object",
			"properties": {"caps": {"type": ["object", "null"]}, "_meta": {"type": ["object", "null"]},
				"anchor": {"type": ["object", "null"], "description": "Omitted and `+"`null`"+` are equivalent and leave the stored anchor unchanged."},
				"reworded": {"type": ["object", "null"], "description": "Omitting it or sending `+"`null`"+` both leave the stored anchor unchanged."}}},
		"Message": {"description": "Omitted patch fields keep their previous values.", "type": "object",
			"properties": {"content": {"type": ["array", "null"], "description": "Omitted leaves content unchanged. `+"`null` or `[]`"+` clears it."}}},
		"Update": {"description": "Only changed fields need to be included.", "type": "object",
			"properties": {"name": {"type": ["string", "null"], "description": "Omitting it or sending `+"`null`"+` both leave the name unchanged."}}}
	}}`), &schema); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(nullTextUnread(&schema), " "); got != "Cursor.reworded" {
		t.Errorf("nullTextUnread = %q, want Cursor.reworded", got)
	}
}
