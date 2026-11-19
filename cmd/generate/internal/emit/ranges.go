package emit

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// intRange is a closed range of integers: the values that an integer schema
// allows, within the int64 that is the widest Go type of an integer
// (integerType). The type intRange of the root package's shape.go has the
// same fields, so the table shapes holds it as it is.
type intRange struct{ min, max int64 }

var int64Range = intRange{math.MinInt64, math.MaxInt64}

// formatRanges holds the range of each integer format of the schema: the
// Rust type that the format names, which fails to read a value out of its
// range. An int64 holds no uint64 above math.MaxInt64, so the decode rejects
// one before a range check could.
var formatRanges = map[string]intRange{
	"int32":  {math.MinInt32, math.MaxInt32},
	"int64":  int64Range,
	"uint16": {0, math.MaxUint16},
	"uint32": {0, math.MaxUint32},
	"uint64": {0, math.MaxInt64},
}

// formatRange returns the range of the integer format format, or of every
// int64 when format is "". It panics on a format that formatRanges does not
// know, so that a new one gets a Go type and a range before a peer can send
// a value out of it.
func formatRange(format string) intRange {
	if format == "" {
		return int64Range
	}
	r, ok := formatRanges[format]
	if !ok {
		panic(fmt.Sprintf("integer format %q has no range in formatRanges", format))
	}
	return r
}

// ownRange returns the range that the keywords of d itself allow, when d is
// an integer schema: its format intersected with minimum and maximum. It
// returns nil when they allow every int64.
func ownRange(d *load.Definition) *intRange {
	if d == nil {
		return nil
	}
	if kinds := kindsOf(d.Type); !slices.Contains(kinds, "kindInteger") || slices.Contains(kinds, "kindFraction") {
		return nil
	}
	r := formatRange(d.Format)
	if d.Minimum != "" {
		r.min = max(r.min, bound(d.Minimum))
	}
	if d.Maximum != "" {
		r.max = min(r.max, bound(d.Maximum))
	}
	if r == int64Range {
		return nil
	}
	return &r
}

// bound returns the int64 nearest to n, the JSON text of an integer.
func bound(n json.Number) int64 {
	v, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		panic(fmt.Sprintf("integer bound %s is not an integer", n))
	}
	return v
}

// intersect returns the values in both r and o, where nil stands for every
// int64.
func intersect(r, o *intRange) *intRange {
	switch {
	case r == nil:
		return o
	case o == nil:
		return r
	}
	return &intRange{max(r.min, o.min), min(r.max, o.max)}
}

// referred returns the $defs entry that d, or the alternative of d besides
// null, stands for through $ref or an allOf of one $ref, and nil when it
// stands for none.
func (b *typesBuilder) referred(d *load.Definition) *load.Definition {
	d = withoutNull(d)
	ref := d.Ref
	if ref == "" && len(d.AllOf) == 1 && d.AllOf[0] != nil {
		ref = d.AllOf[0].Ref
	}
	if ref == "" {
		return nil
	}
	return b.schema.Defs[strings.TrimPrefix(ref, "#/$defs/")]
}

// target returns the schema at the end of the $refs from d (referred),
// without null.
func (b *typesBuilder) target(d *load.Definition) *load.Definition {
	for next := b.referred(d); next != nil; next = b.referred(d) {
		d = next
	}
	return withoutNull(d)
}

// heldRange returns the range of the integers that a field of schema def
// holds through via, the containers of its Go type (peelContainers): the
// intersection of the ranges of the integer schema and of each schema that
// stands for it, such as a property that refers to ProtocolVersion. It
// returns nil when the field holds no integer, or one that can be any
// int64.
func (b *typesBuilder) heldRange(def *load.Definition, via []container) *intRange {
	for _, c := range via {
		switch c {
		case items:
			def = b.target(def).Items
		case members:
			def = b.target(def).AdditionalProperties.Schema
		}
	}
	var r *intRange
	for ; def != nil; def = b.referred(def) {
		r = intersect(r, ownRange(withoutNull(def)))
	}
	return r
}

// checkRange returns the statement that returns an error when the integer v,
// or the integer that v points to when ptr is set, is out of r. path and args
// name it as in validateHeld.
func checkRange(r intRange) heldCheck {
	return func(v *Statement, ptr bool, path string, args []Code) Code {
		if ptr {
			v = Op("*").Add(v)
		}
		var cond *Statement
		if r.min > math.MinInt64 {
			cond = v.Clone().Op("<").Add(intLit(r.min))
		}
		if r.max < math.MaxInt64 {
			above := v.Clone().Op(">").Add(intLit(r.max))
			if cond == nil {
				cond = above
			} else {
				cond = cond.Op("||").Add(above)
			}
		}
		msg := Lit(path + " must be " + r.String())
		return If(cond).Block(Return(Qual("fmt", "Errorf").Call(append([]Code{msg}, args...)...)))
	}
}

// String says which integers r holds, as the error of a value out of it
// does: "between 0 and 4294967295", "at least 0" or "at most 10".
func (r intRange) String() string {
	switch {
	case r.max == math.MaxInt64:
		return fmt.Sprintf("at least %d", r.min)
	case r.min == math.MinInt64:
		return fmt.Sprintf("at most %d", r.max)
	}
	return fmt.Sprintf("between %d and %d", r.min, r.max)
}

// intLit returns x as an untyped constant, which an int and an int64
// compare with alike. Lit renders an int64 as int64(x).
func intLit(x int64) Code {
	switch x {
	case math.MinInt64:
		return Qual("math", "MinInt64")
	case math.MaxInt64:
		return Qual("math", "MaxInt64")
	}
	return Op(strconv.FormatInt(x, 10))
}
