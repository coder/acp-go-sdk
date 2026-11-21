package acp

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
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
// type, fits the shape id. It reads raw with scan, and decodes raw only when
// scan cannot tell.
func (id shapeID) accepts(raw json.RawMessage) bool {
	switch shapes[id].scan(trimValue(raw), 0) {
	case fitYes:
		return true
	case fitNo:
		return false
	}
	return id.acceptsDecoded(raw)
}

// acceptsDecoded is accepts by way of decoding raw into an any.
func (id shapeID) acceptsDecoded(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	return dec.Decode(&v) == nil && shapes[id].fits(v)
}

// A verdict is what scan can tell about whether a value fits a shape. The
// rules of a shape must all hold, so their verdict is the least of theirs;
// anyOf holds when one alternative fits, so its verdict is the greatest.
type verdict uint8

const (
	fitNo      verdict = iota
	fitUnknown         // scan cannot tell; decode the value to know
	fitYes
)

// maxScanDepth bounds how deep scan follows a value and its shape, and
// maxScanMembers how many members of an object it keeps track of. Past them
// it cannot tell.
const (
	maxScanDepth   = 1000
	maxScanMembers = 16
)

// scan reports whether b, valid JSON without whitespace around it, fits s,
// as fits reports for the value that b decodes to. It cannot tell when a
// values rule compares a string that holds an escape or a byte that is not
// UTF-8, which encoding/json would change, when an object key does, when an
// object has more than maxScanMembers members, and past maxScanDepth.
func (s *shape) scan(b []byte, depth int) verdict {
	if s == nil {
		return fitYes
	}
	kind := kindOfJSON(b)
	if kind == 0 || depth > maxScanDepth {
		return fitUnknown
	}
	v := fitYes
	if s.ref != 0 {
		if v = shapes[s.ref].scan(b, depth+1); v == fitNo {
			return fitNo
		}
	}
	if s.kinds != 0 && s.kinds&kind == 0 {
		return fitNo
	}
	if s.values != nil {
		if v = min(v, s.scanValues(b, kind)); v == fitNo {
			return fitNo
		}
	}
	if s.ints != nil && kind&(kindInteger|kindFraction) != 0 && !s.ints.holds(json.Number(b)) {
		// fits reads the text with holds too.
		return fitNo
	}
	switch {
	case kind == kindObject:
		v = min(v, s.scanMembers(b, depth))
	case kind == kindArray && s.items != nil:
		it, _ := arrayItems(b)
		for it.next() && v != fitNo {
			v = min(v, s.items.scan(it.item, depth+1))
		}
		if it.broken {
			v = min(v, fitUnknown)
		}
	}
	if v == fitNo {
		return fitNo
	}
	if s.anyOf != nil {
		alt := fitNo
		for i := 0; i < len(s.anyOf) && alt != fitYes; i++ {
			alt = max(alt, s.anyOf[i].scan(b, depth+1))
		}
		if v = min(v, alt); v == fitNo {
			return fitNo
		}
	}
	if s.not != nil {
		v = min(v, fitYes-s.not.scan(b, depth+1))
	}
	return v
}

// scanValues reports whether b, a JSON value of the given kind, is one of
// s.values.
func (s *shape) scanValues(b []byte, kind jsonKind) verdict {
	var text []byte
	plain := false // text reads the same after encoding/json unquotes it
	if kind == kindString {
		text = b[1 : len(b)-1]
		plain = bytes.IndexByte(text, '\\') < 0 && utf8.Valid(text)
	}
	for _, want := range s.values {
		switch want := want.(type) {
		case nil:
			if kind == kindNull {
				return fitYes
			}
		case bool:
			if kind == kindBoolean && want == (b[0] == 't') {
				return fitYes
			}
		case json.Number:
			if kind&(kindInteger|kindFraction) != 0 && string(want) == string(b) {
				return fitYes
			}
		case string:
			if kind == kindString && !plain {
				return fitUnknown
			}
			if kind == kindString && want == string(text) {
				return fitYes
			}
		default:
			return fitUnknown
		}
	}
	return fitNo
}

// scanMembers checks the members of b, a JSON object, against s.
func (s *shape) scanMembers(b []byte, depth int) verdict {
	if s.required == nil && s.props == nil && s.rest == nil {
		return fitYes
	}
	type member struct{ key, value []byte }
	var buf [maxScanMembers]member
	members := buf[:0]
	it, _ := objectMembers(b)
	for it.next() {
		// encoding/json would unescape the key or replace its bad bytes.
		if it.escaped || len(members) == len(buf) || !utf8.Valid(it.key) {
			return fitUnknown
		}
		members = append(members, member{it.key, it.value})
	}
	if it.broken {
		return fitUnknown
	}
	for _, k := range s.required {
		if !slices.ContainsFunc(members, func(m member) bool { return string(m.key) == k }) {
			return fitNo
		}
	}
	v := fitYes
	for i, m := range members {
		p, named := s.props[string(m.key)]
		if !named {
			p = s.rest
		}
		// encoding/json keeps the last of the members with one key.
		if p == nil || slices.ContainsFunc(members[i+1:], func(n member) bool { return string(n.key) == string(m.key) }) {
			continue
		}
		if v = min(v, p.scan(m.value, depth+1)); v == fitNo {
			return fitNo
		}
	}
	return v
}

// kindOfJSON returns the kind of b, valid JSON without whitespace around it,
// as kindOf does for the value that b decodes to.
func kindOfJSON(b []byte) jsonKind {
	if len(b) == 0 {
		return 0
	}
	switch c := b[0]; {
	case c == 'n':
		return kindNull
	case c == 't' || c == 'f':
		return kindBoolean
	case c == '"' && len(b) >= 2:
		return kindString
	case c == '[':
		return kindArray
	case c == '{':
		return kindObject
	case c != '-' && (c < '0' || c > '9'):
		return 0 // b is not JSON
	case bytes.ContainsAny(b, ".eE"):
		return kindFraction
	}
	return kindInteger
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
