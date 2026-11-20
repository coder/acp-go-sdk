package emit

import "testing"

// requiredMembersSchema has required members of every kind at the top of a
// request and below it: a $ref string ID, a closed enum, a plain string, and
// arrays, under array items, map members, an optional pointer, a patch field
// and the variants of an anyOf union. loose is a lenient array of the same
// items.
const requiredMembersSchema = `{"$defs": {
  "ThingId": {"type": "string"},
  "Kind": {"type": "string", "enum": ["a", "b"]},
  "Item": {"type": "object", "required": ["id", "label"], "properties": {
    "id": {"allOf": [{"$ref": "#/$defs/ThingId"}]},
    "label": {"type": "string"}}},
  "IdServer": {"type": "object", "required": ["serverId", "tags"], "properties": {
    "serverId": {"$ref": "#/$defs/ThingId"},
    "tags": {"type": "array", "items": {"type": "string"}}}},
  "NamedServer": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
  "Server": {"anyOf": [
    {"type": "object", "required": ["type"], "properties": {"type": {"type": "string", "const": "x"}},
      "allOf": [{"$ref": "#/$defs/IdServer"}]},
    {"title": "y", "allOf": [{"$ref": "#/$defs/NamedServer"}]}]},
  "PingRequest": {"type": "object", "required": ["thingId", "kind", "note", "items", "servers"],
    "properties": {
      "thingId": {"$ref": "#/$defs/ThingId"},
      "kind": {"$ref": "#/$defs/Kind"},
      "note": {"type": "string"},
      "items": {"type": "array", "items": {"$ref": "#/$defs/Item"}},
      "servers": {"type": "array", "items": {"$ref": "#/$defs/Server"}},
      "best": {"anyOf": [{"$ref": "#/$defs/Item"}, {"type": "null"}]},
      "latest": {"anyOf": [{"$ref": "#/$defs/Item"}, {"type": "null"}], "description": "Omitted means unchanged. ` + "`null`" + ` clears it."},
      "byName": {"type": "object", "additionalProperties": {"$ref": "#/$defs/Item"}},
      "loose": {"type": "array", "items": {"$ref": "#/$defs/Item"},
        "x-deserialize-default-on-error": true, "x-deserialize-skip-invalid-items": true}}}
}}`

const requiredMembersTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestPingRequest_Validate_ChecksRequiredMembersAtEveryDepth(t *testing.T) {
	const valid = ` + "`" + `"thingId":"t","kind":"a","note":"n","items":[{"id":"i","label":"l"}],"servers":[{"type":"x","serverId":"s","tags":[]},{"name":"y"}]` + "`" + `
	tests := []struct {
		name, payload, wantErr string
	}{
		{"valid", "{" + valid + "}", ""},
		{"required ID empty", ` + "`" + `{"thingId":"","kind":"a","note":"n","items":[],"servers":[]}` + "`" + `, "thingId is required"},
		{"closed enum empty", ` + "`" + `{"thingId":"t","kind":"","note":"n","items":[],"servers":[]}` + "`" + `, "kind is required"},
		{"plain string of a message empty", ` + "`" + `{"thingId":"t","kind":"a","note":"","items":[],"servers":[]}` + "`" + `, "note is required"},
		{"ID of an array item empty", ` + "`" + `{"thingId":"t","kind":"a","note":"n","items":[{"id":"","label":"l"}],"servers":[]}` + "`" + `, "items[0]: id is required"},
		{"nested plain string empty", ` + "`" + `{"thingId":"t","kind":"a","note":"n","items":[{"id":"i","label":""}],"servers":[{"name":""}]}` + "`" + `, ""},
		{"anyOf variant with an empty ID", ` + "`" + `{"thingId":"t","kind":"a","note":"n","items":[],"servers":[{"type":"x","serverId":"","tags":[]}]}` + "`" + `, "servers[0]: serverId is required"},
		{"optional pointer null", "{" + valid + ` + "`" + `,"best":null}` + "`" + `, ""},
		{"optional pointer with an empty ID", "{" + valid + ` + "`" + `,"best":{"id":"","label":"l"}}` + "`" + `, "best: id is required"},
		{"patch null", "{" + valid + ` + "`" + `,"latest":null}` + "`" + `, ""},
		{"patch with an empty ID", "{" + valid + ` + "`" + `,"latest":{"id":"","label":"l"}}` + "`" + `, "latest: id is required"},
		{"map member with an empty ID", "{" + valid + ` + "`" + `,"byName":{"k":{"id":"","label":"l"}}}` + "`" + `, ` + "`" + `byName["k"]: id is required` + "`" + `},
	}
	for _, tt := range tests {
		var req PingRequest
		if err := json.Unmarshal([]byte(tt.payload), &req); err != nil {
			t.Fatalf("%s: unmarshal %s: %v", tt.name, tt.payload, err)
		}
		err := req.Validate()
		if got := errorText(err); got != tt.wantErr {
			t.Errorf("%s: Validate() on %s = %q, want %q", tt.name, tt.payload, got, tt.wantErr)
		}
	}
}

// A decode fails without a required member, so only a value built in Go
// holds a nil required array.
func TestPingRequest_Validate_RejectsANilRequiredArray(t *testing.T) {
	var req PingRequest
	if err := json.Unmarshal([]byte(` + "`" + `{"thingId":"t","kind":"a","note":"n","items":[],"servers":[]}` + "`" + `), &req); err != nil {
		t.Fatal(err)
	}
	req.Items = nil
	if err := req.Validate(); errorText(err) != "items is required" {
		t.Errorf("Validate() with nil items = %v, want items is required", err)
	}
}

// A lenient field keeps an item with an empty ID, as Rust does, and Validate
// does not look into it.
func TestPingRequest_Validate_SkipsTheItemsOfALenientField(t *testing.T) {
	var req PingRequest
	in := ` + "`" + `{"thingId":"t","kind":"a","note":"n","items":[],"servers":[],"loose":[{"id":"","label":"l"}]}` + "`" + `
	if err := json.Unmarshal([]byte(in), &req); err != nil {
		t.Fatal(err)
	}
	if err := req.Validate(); err != nil || len(req.Loose) != 1 {
		t.Errorf("Validate() = %v with %d loose items, want nil with 1", err, len(req.Loose))
	}
}

func TestServer_Validate_RequiresAVariant(t *testing.T) {
	var s Server
	if err := s.Validate(); errorText(err) != "Server has no variant set" {
		t.Errorf("Validate() on a zero Server = %v, want Server has no variant set", err)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
`

func TestWriteTypesJen_ValidateChecksRequiredMembersAtEveryDepth(t *testing.T) {
	checkGenerated(t, requiredMembersSchema, map[string]string{"validate_test.go": requiredMembersTest})
}
