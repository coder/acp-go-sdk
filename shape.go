package acp

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
)

// A shape holds the rules of a JSON Schema node that the Go types do not
// check: required members, closed enums and consts, the bounds of an integer
// within its Go type, where null is allowed, and which union variant a value can be. A property with an
// x-deserialize-default-on-error or x-deserialize-skip-invalid-items hint
// checks its value against the shape of its schema after the value decodes.
// A value that does not fit reads as the property's default, or its array
// item is dropped, as the Rust types do: there such a value fails to
// deserialize, and the hint catches the error.
//
// The generator writes one shape per $defs entry that a hinted property
// reaches, in the table shapes in types_gen.go, and names its index
// shape<Def>. A shape ignores string formats, string lengths, and the rule
// that a oneOf value fits only one variant, as the Rust types do. The Go type
// of an integer holds the range of its format, as the Rust type that the
// format names does, so a shape holds only a minimum or maximum that is
// narrower. A member of an object
// whose own hint tolerates a bad value has no rules in the shape, because
// it reads as its default and the object around it stays.
type shape struct {
	ref      shapeID           // a shapes entry that the value fits too
	kinds    jsonKind          // the kinds of value that fit; 0 for every kind
	values   []any             // the values that fit (an enum or a const); nil for any
	ints     *intRange         // the integers that fit; nil for any
	required []string          // the members that an object must have
	props    map[string]*shape // the members that an object may have
	rest     *shape            // each member that props does not name
	items    *shape            // each item of an array
	anyOf    []*shape          // the value fits one of them
	not      *shape            // the value does not fit it
}

// intRange is the closed range of integers from min to max: the bounds that
// the Go type of the value does not enforce. math.MinInt64 as min and
// math.MaxInt64 as max stand for no bound on that side.
type intRange struct{ min, max int64 }

// holds reports whether n, an integer that decoded into its Go type, is in r.
// A uint64 above math.MaxInt64 is in r when r has no upper bound.
func (r *intRange) holds(n json.Number) bool {
	if i, err := n.Int64(); err == nil {
		return i >= r.min && i <= r.max
	}
	_, err := strconv.ParseUint(string(n), 10, 64)
	return err == nil && r.max == math.MaxInt64
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
	if n, ok := v.(json.Number); ok && s.ints != nil && !s.ints.holds(n) {
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
