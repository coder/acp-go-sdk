package emit

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/ir"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// intRange is a closed range of integers: the bounds of an integer schema
// that the Go type of its value (integerType) does not enforce.
// math.MinInt64 as min and math.MaxInt64 as max stand for no bound on that
// side. The type intRange of the root package's shape.go has the same
// fields, so the table shapes holds it as it is.
type intRange struct{ min, max int64 }

var noBounds = intRange{math.MinInt64, math.MaxInt64}

// intType is a Go integer type and the integers that it holds.
type intType struct {
	name string
	min  int64
	max  uint64
}

// formatTypes holds the Go type of each integer format of the schema: the Go
// type of the Rust type that the format names. encoding/json fails to read a
// value out of its range, as serde_json fails one out of the Rust type, so no
// check repeats the range of a format.
var formatTypes = map[string]intType{
	"int32":  {"int32", math.MinInt32, math.MaxInt32},
	"int64":  {"int64", math.MinInt64, math.MaxInt64},
	"uint16": {"uint16", 0, math.MaxUint16},
	"uint32": {"uint32", 0, math.MaxUint32},
	"uint64": {"uint64", 0, math.MaxUint64},
}

// formatType returns the Go type of the integer format format, and int for
// "", whose bounds a check compares as an int64 (fieldCheck.boundsInInt64).
// It panics on a format that formatTypes does not know, so that a new one
// gets a Go type before a peer can send a value out of its range.
func formatType(format string) intType {
	if format == "" {
		return intType{"int", math.MinInt64, math.MaxInt64}
	}
	t, ok := formatTypes[format]
	if !ok {
		panic(fmt.Sprintf("integer format %q has no Go type in formatTypes", format))
	}
	return t
}

// holdsInteger reports whether the Go type of the integer format format
// (formatType) holds the integer value on every target, so that a literal of
// it compiles: an int holds only the values of an int32, since it is 32 bits
// wide for GOARCH=386.
func holdsInteger(format string, value float64) bool {
	t := formatType(format)
	least, greatest := float64(t.min), float64(t.max)
	if t.name == "int" {
		least, greatest = math.MinInt32, math.MaxInt32
	}
	return value >= least && value <= greatest
}

// ownRange returns the bounds that the keywords of d itself set, when d is an
// integer schema (integer), and that the Go type of its value does not
// enforce: minimum above the least value of that type, and maximum below the
// greatest. The Go type is that of d's format, or of the schema that d stands
// for when d has no type, such as a bound next to a $ref. It returns nil when
// the Go type enforces every bound, as it does for each one of schema 1.18.0
// through 1.33.0. It panics on a bound that no value of the Go type meets,
// since a check against it would not compile.
func ownRange(schema *load.Schema, d *load.Definition) *intRange {
	if d == nil || !integer(schema, d) {
		return nil
	}
	t := formatType(typed(schema, d).Format)
	least, greatest := big.NewInt(t.min), new(big.Int).SetUint64(t.max)
	r := noBounds
	if d.Minimum != "" {
		lo := bound(d.Minimum, true)
		if lo.Cmp(greatest) > 0 {
			panic(fmt.Sprintf("integer minimum %s is above every value of %s", d.Minimum, t.name))
		}
		if lo.Cmp(least) > 0 {
			r.min = boundInt64(lo, d.Minimum)
		}
	}
	if d.Maximum != "" {
		hi := bound(d.Maximum, false)
		if hi.Cmp(least) < 0 {
			panic(fmt.Sprintf("integer maximum %s is below every value of %s", d.Maximum, t.name))
		}
		if hi.Cmp(greatest) < 0 {
			r.max = boundInt64(hi, d.Maximum)
		}
	}
	if r == noBounds {
		return nil
	}
	return &r
}

// bound returns the integer bound that n, the JSON number of a minimum (up)
// or a maximum, sets on an integer: the least integer at or above n, or the
// greatest at or below it, so that 0.5 and 1e1 work.
func bound(n json.Number, up bool) *big.Int {
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
	return q
}

// boundInt64 returns b, a bound from the keyword text n that the Go type does
// not enforce, as an int64. It panics when b does not fit an int64, or is
// math.MinInt64 or math.MaxInt64, which stand for no bound; no schema has
// such a bound.
func boundInt64(b *big.Int, n json.Number) int64 {
	if !b.IsInt64() || b.Int64() == math.MinInt64 || b.Int64() == math.MaxInt64 {
		panic(fmt.Sprintf("integer bound %s does not fit intRange", n))
	}
	return b.Int64()
}

// intersect returns the values in both r and o, where nil stands for no
// bound.
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
// it has none, by the schema that it stands for (typed), so that a minimum or
// maximum next to a $ref, or next to an allOf of one, counts.
func integer(schema *load.Schema, d *load.Definition) bool {
	d = typed(schema, d)
	if d == nil {
		return false
	}
	kinds := kindsOf(d.Type)
	return slices.Contains(kinds, "kindInteger") && !slices.Contains(kinds, "kindFraction")
}

// typed returns d when it has a type, and otherwise the first schema with a
// type that d stands for through referredDef, or nil when there is none.
func typed(schema *load.Schema, d *load.Definition) *load.Definition {
	for seen := map[*load.Definition]bool{}; d != nil && d.Type == nil && !seen[d]; {
		seen[d] = true
		d = referredDef(schema, d)
	}
	return d
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
		// A bound can sit next to the null alternative or on it.
		r = intersect(r, ownRange(b.schema, def))
		if inner := withoutNull(def); inner != def {
			r = intersect(r, ownRange(b.schema, inner))
		}
	}
	return r
}

// mustHoldDefault panics when the default of field, an integer whose Go type
// is goType, is not an integer within that type and within bounds, the range
// that Validate checks (nil for none). The default fills an absent member, so
// Validate would reject a message that leaves the member out, or setDefault
// would fail to decode the default and leave 0. Such a schema contradicts
// itself, and none from 1.18.0 through 1.33.0 has one. It panics on a default
// of 2^53 or beyond too, which load may have rounded.
func mustHoldDefault(field ir.Field, goType string, bounds *intRange) {
	// planFor calls it only for a default that is not null.
	n, ok := field.Def.Default.(float64)
	if !ok {
		panic(fmt.Sprintf("default %v of integer property %q is not a number", field.Def.Default, field.Prop))
	}
	t := formatType("")
	for _, ft := range formatTypes {
		if ft.name == goType {
			t = ft
		}
	}
	lo, hi := big.NewInt(t.min), new(big.Int).SetUint64(t.max)
	if bounds != nil {
		if bounds.min != math.MinInt64 {
			lo = big.NewInt(bounds.min)
		}
		if bounds.max != math.MaxInt64 {
			hi = big.NewInt(bounds.max)
		}
	}
	var v big.Rat
	v.SetFloat64(n)
	if math.Abs(n) >= 1<<53 {
		// load reads a default as a float64, and planFor writes the fallback
		// from it, so the generated default would differ from the schema's.
		panic(fmt.Sprintf("default %s of integer property %q is 2^53 or beyond, so the float64 that load reads it as may have rounded it", strconv.FormatFloat(n, 'f', -1, 64), field.Prop))
	}
	if !v.IsInt() || v.Num().Cmp(lo) < 0 || v.Num().Cmp(hi) > 0 {
		panic(fmt.Sprintf("default %s of integer property %q is not an integer between %s and %s", strconv.FormatFloat(n, 'g', -1, 64), field.Prop, lo, hi))
	}
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
