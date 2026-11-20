package emit

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// notSubschemaSchema has hinted properties whose schemas use not with a
// subschema that the shapes cannot say all of: a keyword that they do not
// read (maxLength, pattern, minProperties, additionalProperties: false,
// const: null, minimum on a number), an allOf part besides a $ref, a const
// that is an object, a oneOf, a hint, and the enum member "other". A value
// fits not when it does not fit the subschema (JSON Schema 2020-12, section
// 10.2.1.4), so each value in the first part of the test fits its property:
// a shape must keep it. Pair refers to itself through a not. Chain refers to
// itself through a property, and the shapes can say all of it, so a not of
// Chain still rejects a Chain. Partial loses one alternative of its anyOf
// and keeps the other. A subschema with only annotations fits every value
// (section 4.3.2), as {} does, so not rejects every value.
const notSubschemaSchema = `{"$defs": {
  "Tagged": {"type": "object", "required": ["tag"], "properties": {"tag": {"type": "string", "const": "t"}}},
  "Chain": {"type": "object", "required": ["tag"], "properties": {
    "tag": {"type": "string", "enum": ["x", "y"]},
    "next": {"anyOf": [{"$ref": "#/$defs/Chain"}, {"type": "null"}]}}},
  "Pair": {"type": "object", "required": ["tag"], "properties": {
    "tag": {"type": "string", "minLength": 2},
    "other": {"type": "object", "not": {"$ref": "#/$defs/Pair"}}}},
  "Holder": {"type": "object", "properties": {
    "maxLength": {"type": "string", "not": {"type": "string", "maxLength": 3}, "x-deserialize-default-on-error": true},
    "propPattern": {"type": "object", "not": {"required": ["a"], "properties": {"a": {"pattern": "^z"}}}, "x-deserialize-default-on-error": true},
    "minProperties": {"type": "object", "not": {"required": ["a"], "minProperties": 2}, "x-deserialize-default-on-error": true},
    "closed": {"type": "object", "not": {"type": "object", "properties": {"a": {}}, "additionalProperties": false}, "x-deserialize-default-on-error": true},
    "constNull": {"type": "string", "not": {"type": ["string", "null"], "const": null}, "x-deserialize-default-on-error": true},
    "numberMinimum": {"type": "number", "not": {"type": "number", "minimum": 10}, "x-deserialize-default-on-error": true},
    "allOfPart": {"type": "object", "not": {"allOf": [{"$ref": "#/$defs/Tagged"}, {"required": ["b"]}]}, "x-deserialize-default-on-error": true},
    "objectConst": {"type": "object", "not": {"type": "object", "const": {"a": 1}}, "x-deserialize-default-on-error": true},
    "oneOf": {"type": "string", "not": {"oneOf": [{"type": "string", "const": "a"}, {"type": "string"}]}, "x-deserialize-default-on-error": true},
    "hinted": {"type": "object", "not": {"type": "object", "required": ["a"], "properties": {
      "a": {"type": "integer", "x-deserialize-default-on-error": true}}}, "x-deserialize-default-on-error": true},
    "otherMember": {"type": "string", "not": {"type": "string", "enum": ["a", "other"]}, "x-deserialize-default-on-error": true},
    "pair": {"anyOf": [{"$ref": "#/$defs/Pair"}, {"type": "null"}], "x-deserialize-default-on-error": true},
    "notChain": {"type": "object", "not": {"$ref": "#/$defs/Chain"}, "x-deserialize-default-on-error": true},
    "partial": {"type": "string", "not": {"anyOf": [{"const": "a"}, {"type": "string", "maxLength": 3}]}, "x-deserialize-default-on-error": true},
    "annotated": {"type": "string", "not": {"description": "d", "$comment": "c"}, "x-deserialize-default-on-error": true}}}
}}`

const notSubschemaTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestHolder_UnmarshalJSON_KeepsWhatTheNotSubschemaDoesNotMatch(t *testing.T) {
	tests := []struct{ in, want string }{
		{` + "`" + `{"maxLength":"hello"}` + "`" + `, ` + "`" + `{"maxLength":"hello"}` + "`" + `},
		{` + "`" + `{"propPattern":{"a":"b"}}` + "`" + `, ` + "`" + `{"propPattern":{"a":"b"}}` + "`" + `},
		{` + "`" + `{"minProperties":{"a":"b"}}` + "`" + `, ` + "`" + `{"minProperties":{"a":"b"}}` + "`" + `},
		{` + "`" + `{"closed":{"a":1,"b":2}}` + "`" + `, ` + "`" + `{"closed":{"a":1,"b":2}}` + "`" + `},
		{` + "`" + `{"constNull":"x"}` + "`" + `, ` + "`" + `{"constNull":"x"}` + "`" + `},
		{` + "`" + `{"numberMinimum":5}` + "`" + `, ` + "`" + `{"numberMinimum":5}` + "`" + `},
		{` + "`" + `{"allOfPart":{"tag":"t"}}` + "`" + `, ` + "`" + `{"allOfPart":{"tag":"t"}}` + "`" + `},
		{` + "`" + `{"objectConst":{"a":2}}` + "`" + `, ` + "`" + `{"objectConst":{"a":2}}` + "`" + `},
		{` + "`" + `{"oneOf":"a"}` + "`" + `, ` + "`" + `{"oneOf":"a"}` + "`" + `},
		{` + "`" + `{"hinted":{"a":"s"}}` + "`" + `, ` + "`" + `{"hinted":{"a":"s"}}` + "`" + `},
		{` + "`" + `{"otherMember":"b"}` + "`" + `, ` + "`" + `{"otherMember":"b"}` + "`" + `},
		{` + "`" + `{"pair":{"tag":"ab","other":{"tag":"b"}}}` + "`" + `, ` + "`" + `{"pair":{"other":{"tag":"b"},"tag":"ab"}}` + "`" + `},
		{` + "`" + `{"notChain":{"tag":"x","next":{"tag":"z"}}}` + "`" + `, ` + "`" + `{"notChain":{"next":{"tag":"z"},"tag":"x"}}` + "`" + `},
		{` + "`" + `{"partial":"hello"}` + "`" + `, ` + "`" + `{"partial":"hello"}` + "`" + `},
		// The subschema matches these values, so the shapes reject them.
		{` + "`" + `{"notChain":{"tag":"x","next":{"tag":"y"}}}` + "`" + `, ` + "`" + `{}` + "`" + `},
		{` + "`" + `{"partial":"a"}` + "`" + `, ` + "`" + `{}` + "`" + `},
		{` + "`" + `{"annotated":"x"}` + "`" + `, ` + "`" + `{}` + "`" + `},
	}
	for _, tt := range tests {
		var got Holder
		if err := json.Unmarshal([]byte(tt.in), &got); err != nil {
			t.Errorf("unmarshal %s: %v", tt.in, err)
			continue
		}
		out, _ := json.Marshal(got)
		if string(out) != tt.want {
			t.Errorf("%s decoded to %s, want %s", tt.in, out, tt.want)
		}
	}
}
`

func TestWriteTypesJen_NotKeepsWhatItsSubschemaDoesNotMatch(t *testing.T) {
	checkGenerated(t, notSubschemaSchema, map[string]string{"not_subschema_test.go": notSubschemaTest})
}

// TestShapeTable_NodeReadsEachKeywordThatDefinitionHolds fails when
// load.Definition gains a field for a keyword that node neither translates
// nor knows to constrain no value. Definition.Unmodeled leaves out each
// keyword that a field holds, so an under-approximation would fit values
// that such a keyword rejects, and a not would reject values that its
// subschema does not match.
func TestShapeTable_NodeReadsEachKeywordThatDefinitionHolds(t *testing.T) {
	translated := []string{"$ref", "allOf", "type", "const", "enum", "format", "minimum", "maximum", "required",
		"additionalProperties", "properties", "items", "anyOf", "oneOf", "not"}
	// Definition holds these only in a form that constrains no value: the
	// annotations, the OpenAPI discriminator, and unevaluatedProperties,
	// which it holds only when it is true. The x- hints only widen a value.
	free := []string{"description", "title", "default", "discriminator", "unevaluatedProperties"}
	typ := reflect.TypeOf(load.Definition{})
	for i := 0; i < typ.NumField(); i++ {
		k, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if k == "" || k == "-" || strings.HasPrefix(k, "x-") || slices.Contains(translated, k) || slices.Contains(free, k) {
			continue
		}
		t.Errorf("load.Definition holds %q, which node does not translate: translate it, or list it in free if it constrains no value", k)
	}
}
