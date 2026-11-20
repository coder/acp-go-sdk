package acp

import (
	"encoding/json"
	"reflect"
	"strings"
)

// rejectDuplicateMembers returns an error when the JSON object b holds a
// member named one of props twice, as the Rust SDK rejects a struct field
// that repeats. A key counts by its name without escapes, and matches a
// name only exactly. A member that props does not name may repeat, and
// encoding/json keeps its last value, as the Rust SDK keeps the last key of
// a map. v points to the struct that b decodes into, whose field for the
// member the error names:
//
//	json: cannot unmarshal duplicate member into Go struct field SessionNotification.sessionId of type acp.SessionId
//
// A generated UnmarshalJSON calls it once encoding/json has decoded b, so b
// is an object or null. props holds at most 64 names, which the generator
// checks.
func rejectDuplicateMembers(b []byte, v any, props ...string) error {
	var seen uint64
	it, _ := objectMembers(b)
	for it.next() {
		key := it.key
		if it.escaped {
			key, _ = unquoteBytes(key)
		}
		for i, prop := range props {
			if string(key) != prop {
				continue
			}
			if seen&(1<<i) != 0 {
				return duplicateMemberError(v, prop)
			}
			seen |= 1 << i
			break
		}
	}
	return nil
}

// duplicateMemberError returns the error for the member prop of the struct
// that v points to: its field is the one that json tags with prop.
func duplicateMemberError(v any, prop string) error {
	t := reflect.TypeOf(v).Elem()
	err := &json.UnmarshalTypeError{Value: "duplicate member", Struct: t.Name(), Field: prop}
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name == prop {
			err.Type = t.Field(i).Type
			return err
		}
	}
	panic("acp: " + t.Name() + " has no field for the property " + prop)
}
