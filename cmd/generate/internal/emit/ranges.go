package emit

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
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
// an integer schema (integer): its format intersected with minimum and
// maximum. It returns nil when they allow every int64.
func ownRange(schema *load.Schema, d *load.Definition) *intRange {
	if d == nil || !integer(schema, d) {
		return nil
	}
	r := formatRange(d.Format)
	if d.Minimum != "" {
		r.min = max(r.min, bound(d.Minimum, true))
	}
	if d.Maximum != "" {
		r.max = min(r.max, bound(d.Maximum, false))
	}
	if r == int64Range {
		return nil
	}
	return &r
}

// bound returns the integer bound that n, the JSON number of a minimum (up)
// or a maximum, sets on an integer: the least integer at or above n, or the
// greatest at or below it, so that 0.5 and 1e1 work. It clamps to int64.
func bound(n json.Number, up bool) int64 {
	var r big.Rat
	if _, ok := r.SetString(string(n)); !ok {
		panic(fmt.Sprintf("integer bound %s is not a number", n))
	}
	// QuoRem truncates toward zero, which rounds the wrong way for a
	// positive minimum and a negative maximum that are not integers.
	q, rem := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	switch {
	case rem.Sign() > 0 && up:
		q.Add(q, big.NewInt(1))
	case rem.Sign() < 0 && !up:
		q.Sub(q, big.NewInt(1))
	}
	switch {
	case q.IsInt64():
		return q.Int64()
	case q.Sign() > 0:
		return math.MaxInt64
	default:
		return math.MinInt64
	}
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
	return referredDef(b.schema, d)
}

// referredDef is referred for the $defs of schema.
func referredDef(schema *load.Schema, d *load.Definition) *load.Definition {
	d = withoutNull(d)
	ref := d.Ref
	if ref == "" && len(d.AllOf) == 1 && d.AllOf[0] != nil {
		ref = d.AllOf[0].Ref
	}
	if ref == "" {
		return nil
	}
	return schema.Defs[strings.TrimPrefix(ref, "#/$defs/")]
}

// integer reports whether d is an integer schema: by its own type, or, when
// it has none, by the schema that it stands for (referredDef), so that a
// minimum or maximum next to a $ref, or next to an allOf of one, counts.
func integer(schema *load.Schema, d *load.Definition) bool {
	for seen := map[*load.Definition]bool{}; d != nil && d.Type == nil && !seen[d]; {
		seen[d] = true
		d = referredDef(schema, d)
	}
	if d == nil {
		return false
	}
	kinds := kindsOf(d.Type)
	return slices.Contains(kinds, "kindInteger") && !slices.Contains(kinds, "kindFraction")
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
		r = intersect(r, ownRange(b.schema, withoutNull(def)))
	}
	return r
}

// checkRange returns the statement that returns an error when the integer v,
// or the integer that v points to when ptr is set, is out of r. path and args
// name it as in validateHeld. With asInt64 it compares v as an int64.
func checkRange(r intRange, asInt64 bool) heldCheck {
	return func(v *Statement, ptr bool, path string, args []Code) Code {
		if ptr {
			v = Op("*").Add(v)
		}
		if asInt64 {
			v = Int64().Call(v)
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
