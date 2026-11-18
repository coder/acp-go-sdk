package acp

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// A shape holds the rules of a JSON Schema node that the Go types do not
// check: required members, closed enums and consts, where null is allowed,
// and which union variant a value can be. A property with an
// x-deserialize-default-on-error or x-deserialize-skip-invalid-items hint
// checks its value against the shape of its schema after the value decodes.
// A value that does not fit reads as the property's default, or its array
// item is dropped, as the Rust types do: there such a value fails to
// deserialize, and the hint catches the error.
//
// The generator writes one shape per $defs entry that a hinted property
// reaches, in the table shapes in types_gen.go, and names its index
// shape<Def>. A shape ignores formats, numeric bounds, string lengths, and
// the rule that a oneOf value fits only one variant. A member of an object
// whose own hint tolerates a bad value has no rules in the shape, because
// it reads as its default and the object around it stays.
type shape struct {
	ref      shapeID           // a shapes entry that the value fits too
	kinds    jsonKind          // the kinds of value that fit; 0 for every kind
	values   []any             // the values that fit (an enum or a const); nil for any
	required []string          // the members that an object must have
	props    map[string]*shape // the members that an object may have
	rest     *shape            // each member that props does not name
	items    *shape            // each item of an array
	anyOf    []*shape          // the value fits one of them
	not      *shape            // the value does not fit it
}

// shapeID is an index into shapes. The zero value names no shape.
type shapeID int

// jsonKind is a set of the kinds of JSON value. A number is an integer when
// its text has no fraction and no exponent, as for serde_json.
type jsonKind uint8

const (
	kindNull jsonKind = 1 << iota
	kindBoolean
	kindInteger
	kindFraction
	kindString
	kindArray
	kindObject
)

// accepts reports whether raw, the JSON of a value that decoded into its Go
// type, fits the shape id.
func (id shapeID) accepts(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	return dec.Decode(&v) == nil && shapes[id].fits(v)
}

// fits reports whether v, a value that encoding/json decoded into an any
// with UseNumber, fits s. A nil shape fits every value.
func (s *shape) fits(v any) bool {
	if s == nil {
		return true
	}
	if s.ref != 0 && !shapes[s.ref].fits(v) {
		return false
	}
	if s.kinds != 0 && s.kinds&kindOf(v) == 0 {
		return false
	}
	if s.values != nil && !slices.Contains(s.values, v) {
		return false
	}
	switch v := v.(type) {
	case map[string]any:
		for _, k := range s.required {
			if _, ok := v[k]; !ok {
				return false
			}
		}
		for k, member := range v {
			p, named := s.props[k]
			if !named {
				p = s.rest
			}
			if !p.fits(member) {
				return false
			}
		}
	case []any:
		for _, item := range v {
			if !s.items.fits(item) {
				return false
			}
		}
	}
	if s.anyOf != nil && !slices.ContainsFunc(s.anyOf, func(alt *shape) bool { return alt.fits(v) }) {
		return false
	}
	return s.not == nil || !s.not.fits(v)
}

func kindOf(v any) jsonKind {
	switch v := v.(type) {
	case nil:
		return kindNull
	case bool:
		return kindBoolean
	case json.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return kindFraction
		}
		return kindInteger
	case string:
		return kindString
	case []any:
		return kindArray
	case map[string]any:
		return kindObject
	}
	return 0
}
