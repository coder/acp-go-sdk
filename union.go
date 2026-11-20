package acp

import (
	"encoding/json"
	"errors"
)

// The UnmarshalJSON of a union chooses its variant by the members of an
// object. It reads them with readUnionObject, which scans b in place, instead
// of decoding b into a map[string]json.RawMessage first. The scan does not
// check that b is valid JSON: a variant decodes only valid JSON, so a value
// that is not JSON decodes into no variant, and unionError then reports its
// syntax error, as the map decode did.

// unionsByMap makes readUnionObject decode every value into a map, as the
// unions did before they scanned objects. Tests set it to compare the two.
var unionsByMap bool

// A unionObject holds the members of the JSON object that a union decodes.
type unionObject struct {
	// isObject says that the value is an object, or null, which
	// encoding/json decodes into an empty map.
	isObject bool
	// b is the object, when get scans its members. No key in it has an
	// escape, so its raw keys are the keys that encoding/json decodes.
	b []byte
	// m holds the members as encoding/json decodes them, when b is nil.
	m map[string]json.RawMessage
}

// readUnionObject reads the members of b, which a union used to decode into a
// map[string]json.RawMessage. It returns the syntax error of b only when it
// decodes b that way, after an escaped key or when unionsByMap is set; it
// scans an object without checking that b is valid JSON.
func readUnionObject(b []byte) (unionObject, error) {
	if !unionsByMap {
		it, isObject := objectMembers(b)
		if !isObject {
			// encoding/json fails to decode any other value into a map.
			return unionObject{isObject: isNull(b)}, nil
		}
		escaped := false
		for !escaped && it.next() {
			escaped = it.escaped
		}
		if !escaped && !it.broken {
			return unionObject{isObject: true, b: b}, nil
		}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		if _, isTypeErr := err.(*json.UnmarshalTypeError); isTypeErr {
			return unionObject{}, nil
		}
		return unionObject{}, err
	}
	return unionObject{isObject: true, m: m}, nil
}

// get returns the value of the member key and whether there is one, as
// m[key] does: of two members with the key, the last wins. key must be valid
// UTF-8 without U+FFFD, as encoding/json changes a key that is not.
func (o *unionObject) get(key string) (json.RawMessage, bool) {
	if o.b == nil {
		value, ok := o.m[key]
		return value, ok
	}
	var value json.RawMessage
	found := false
	it, _ := objectMembers(o.b)
	for it.next() {
		if string(it.key) == key {
			value, found = it.value, true
		}
	}
	return value, found
}

// delete removes the member key, as delete(m, key) does.
func (o *unionObject) delete(key string) {
	if o.b != nil {
		// Rare: only a discriminator that reads as absent is deleted.
		json.Unmarshal(o.b, &o.m)
		o.b = nil
	}
	delete(o.m, key)
}

// tag returns the value of the member key as json.Unmarshal decodes it into a
// string, and nothing when the member is absent or holds no string.
func (o *unionObject) tag(key string) []byte {
	value, ok := o.get(key)
	if !ok {
		return nil
	}
	if o.b != nil {
		if s, ok := plainString(value); ok {
			return s
		}
	}
	var s string
	json.Unmarshal(value, &s)
	return []byte(s)
}

// plainString returns the content of the JSON string value when it holds only
// ASCII characters that need no escape, which json.Unmarshal keeps as they
// are.
func plainString(value []byte) ([]byte, bool) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return nil, false
	}
	content := value[1 : len(value)-1]
	for _, c := range content {
		if c < ' ' || c >= 0x80 || c == '"' || c == '\\' {
			return nil, false
		}
	}
	return content, true
}

// unionError returns the error of a union's UnmarshalJSON that decoded b into
// no variant: the syntax error of b, which readUnionObject may not have
// reported, or else msg.
func unionError(b []byte, msg string) error {
	if !json.Valid(b) {
		var m map[string]json.RawMessage
		return json.Unmarshal(b, &m)
	}
	return errors.New(msg)
}
