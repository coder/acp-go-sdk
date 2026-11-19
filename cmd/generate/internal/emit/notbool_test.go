package emit

import "testing"

// notSchema has hinted properties whose schemas use not with a boolean
// schema. In JSON Schema 2020-12, true and {} fit every value and false fits
// none (section 4.3.2), and a value fits not when it does not fit its schema
// (section 10.2.1.4). So not: {} and not: true fit no value, not: false fits
// every value, and HasA, whose not holds the property a: false, needs a.
const notSchema = `{"$defs": {
  "HasA": {"type": "object", "properties": {"a": {"type": "integer"}}, "not": {"properties": {"a": false}}},
  "Holder": {"type": "object", "properties": {
    "notEmpty": {"type": "string", "not": {}, "x-deserialize-default-on-error": true},
    "notTrue": {"type": "string", "not": true, "x-deserialize-default-on-error": true},
    "notFalse": {"type": "string", "not": false, "x-deserialize-default-on-error": true},
    "hasA": {"anyOf": [{"$ref": "#/$defs/HasA"}, {"type": "null"}], "x-deserialize-default-on-error": true}}}
}}`

const notTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestHolder_UnmarshalJSON_ReadsNotWithABooleanSchema(t *testing.T) {
	tests := []struct{ in, want string }{
		{` + "`" + `{"notEmpty":"x"}` + "`" + `, ` + "`" + `{}` + "`" + `},
		{` + "`" + `{"notTrue":"x"}` + "`" + `, ` + "`" + `{}` + "`" + `},
		{` + "`" + `{"notFalse":"x"}` + "`" + `, ` + "`" + `{"notFalse":"x"}` + "`" + `},
		{` + "`" + `{"hasA":{"a":1}}` + "`" + `, ` + "`" + `{"hasA":{"a":1}}` + "`" + `},
		{` + "`" + `{"hasA":{}}` + "`" + `, ` + "`" + `{}` + "`" + `},
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

func TestWriteTypesJen_NotWithABooleanSchema(t *testing.T) {
	checkGenerated(t, notSchema, map[string]string{"not_test.go": notTest})
}
