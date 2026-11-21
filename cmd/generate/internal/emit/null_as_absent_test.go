package emit

import "testing"

// nullAsAbsentSchema has an object that says only that it carries the
// changed fields, as ToolCallUpdate does: title without hints, status and
// tags with them, as the fields of ToolCallUpdate have, and note, whose
// description says that null clears it, as title of SessionInfoUpdate says.
const nullAsAbsentSchema = `{"$defs": {
  "Status": {"type": "string", "enum": ["pending", "completed"]},
  "Update": {"description": "Only changed fields need to be included.", "type": "object", "required": ["id"], "properties": {
    "id": {"type": "string"},
    "title": {"type": ["string", "null"]},
    "status": {"anyOf": [{"$ref": "#/$defs/Status"}, {"type": "null"}], "x-deserialize-default-on-error": true},
    "tags": {"type": ["array", "null"], "items": {"type": "string"}, "x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true},
    "note": {"type": ["string", "null"], "description": "Set to null to clear."}
  }}
}}`

const nullAsAbsentTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestUpdate_NullWithoutAClearIsUnset(t *testing.T) {
	var u Update
	if err := json.Unmarshal([]byte(` + "`" + `{"id":"u","title":null,"status":null,"tags":null,"note":null}` + "`" + `), &u); err != nil {
		t.Fatal(err)
	}
	if !u.Title.IsUnset() || !u.Status.IsUnset() || !u.Tags.IsUnset() {
		t.Errorf("null title, status, tags read as %+v, %+v, %+v, want unset", u.Title, u.Status, u.Tags)
	}
	if !u.Note.IsNull() {
		t.Errorf("null note reads as %+v, want null", u.Note)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"id":"u","title":"t","status":"completed","tags":[]}` + "`" + `), &u); err != nil {
		t.Fatal(err)
	}
	if title, ok := u.Title.Value(); !ok || title != "t" {
		t.Errorf("title = %q, %v, want t", title, ok)
	}
	if status, ok := u.Status.Value(); !ok || status != StatusCompleted {
		t.Errorf("status = %q, %v, want completed", status, ok)
	}
	if tags, ok := u.Tags.Value(); !ok || len(tags) != 0 {
		t.Errorf("tags = %v, %v, want set and empty", tags, ok)
	}
}

func TestUpdate_LeavesOutANullPatchWithoutAClear(t *testing.T) {
	u := Update{Id: "u", Title: PatchNull[string](), Status: PatchNull[Status](), Tags: PatchNull[[]string](), Note: PatchNull[string]()}
	if out, err := json.Marshal(u); err != nil || string(out) != ` + "`" + `{"id":"u","note":null}` + "`" + ` {
		t.Errorf("update encodes to %s (error %v), want only id and the null note", out, err)
	}
}
`

func TestWriteTypesJen_ReadsNullAsUnsetWhereTheSchemaDefinesNoClear(t *testing.T) {
	checkGenerated(t, nullAsAbsentSchema, map[string]string{"null_as_absent_test.go": nullAsAbsentTest})
}
