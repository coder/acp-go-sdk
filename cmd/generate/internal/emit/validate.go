package emit

import (
	"fmt"
	"math"
	"strings"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/ir"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// container is one layer of a Go field type around a value whose type can
// have a Validate method.
type container uint8

const (
	pointer container = iota // *T: the value when the pointer is not nil
	items                    // []T: each item
	members                  // map[string]T: each member
	patch                    // Patch[T]: the value when it is set
	// nonZero is an optional member whose Go type is not a pointer: the
	// value when it is not 0, which an absent member reads as. Only an
	// integer is held this way.
	nonZero
)

// fieldCheck is the check plan of one struct field: what the Validate method
// of the struct does with it.
type fieldCheck struct {
	field ir.Field
	// unset is the value that a required field holds when the member is
	// missing: "" for a string type, nil for a slice. nil means no check.
	unset Code
	// descends says that Validate calls the Validate method of each value
	// that the field holds through via, the containers from the field
	// inward. Empty via means the field holds the value itself.
	descends bool
	// bounds is the range that each integer that the field holds through
	// via must be in; nil means no check.
	bounds *intRange
	// boundsInInt64 says that a bound does not fit an int32 and the Go type
	// of the integer is int, which is 32 bits wide on some targets, so the
	// check compares it as an int64.
	boundsInInt64 bool
	via           []container
}

// checkPlan returns the check plan of a struct with fields: one fieldCheck
// per field that its Validate checks, in field order. nests says that the
// struct gives an inline object property a struct of its own
// (emitNestedStructs). plain names the properties whose required plain
// strings must not be empty: every property of a message type, and the own
// properties of a message-level union.
//
// The rules follow what the Rust SDK rejects. A required member whose type
// is a string type of the schema ($ref or allOf to a named string, a closed
// enum or an open one) must not be "", and a required array must not be
// nil, at every depth. A plain inline string may be "", except where plain
// names it. A field whose value type has a Validate method (validates)
// descends: a pointer when it is not nil, each item and member, a Patch when
// it is set, and a struct or union itself only when it is required, because
// an absent optional one looks like its zero value. An integer that the field
// holds the same way, or holds itself while optional and not 0 (nonZero),
// must be within the minimum and maximum that its Go type does not enforce
// (heldRange). The Go type of an integer holds the range of its format, as
// the Rust type that the format names does.
// A lenient field (x-deserialize-default-on-error or
// x-deserialize-skip-invalid-items) neither descends nor checks a range: Rust
// reads a bad value there as the default, and the shape of the field holds
// the same rules.
func (b *typesBuilder) checkPlan(fields []ir.Field, nests bool, plain map[string]bool) []fieldCheck {
	var plan []fieldCheck
	for _, field := range fields {
		if c := b.fieldCheckFor(field, nests, plain[field.Prop]); c.unset != nil || c.descends || c.bounds != nil {
			plan = append(plan, c)
		}
	}
	return plan
}

// fieldCheckFor returns the check plan of field, a struct field that
// checkPlan describes. plain says that a required plain string must not be
// empty.
func (b *typesBuilder) fieldCheckFor(field ir.Field, nests, plain bool) fieldCheck {
	c := fieldCheck{field: field}
	def := field.Def
	if def.Const != nil {
		// The const of a variant is its discriminator, which the union
		// decoder has matched.
		return c
	}
	isPatch := b.patch[def]
	nested := nests && inlineStruct(def)
	var leaf string
	switch {
	case nested:
	case isPatch:
		c.via, leaf = peelContainers(patchValueType(field))
	default:
		c.via, leaf = peelContainers(fieldType(field))
	}
	if isPatch {
		c.via = append([]container{patch}, c.via...)
	}

	if field.Required && !isPatch && def.Default == nil && !acceptsNull(b.schema, def) {
		switch {
		case len(c.via) > 0 && c.via[0] == items:
			c.unset = Nil()
		case len(c.via) == 0 && !field.Lenient.DefaultOnError && (b.stringType(leaf) || (plain && leaf == "string")):
			c.unset = Lit("")
		}
	}

	optional := len(c.via) == 0 && !field.Required
	if field.Lenient.DefaultOnError || field.Lenient.SkipInvalidItems || (optional && nested) {
		return c
	}
	if nested {
		c.descends = len(b.checkPlan(ir.Fields(def.Properties, def.Required), false, nil)) > 0
		return c
	}
	c.descends = b.validates(leaf) && !optional
	goType := b.integerGoType(leaf)
	if goType != "" {
		c.bounds = b.heldRange(def, c.via)
	}
	c.boundsInInt64 = c.bounds != nil && goType == "int" && (c.bounds.min < math.MinInt32 || c.bounds.max > math.MaxInt32)
	if optional && c.bounds != nil {
		c.via = []container{nonZero}
	}
	return c
}

// peelContainers splits goType, the Go type of a field as fieldDecl declares
// it, into its containers and the name of the type inside them, such as
// "SessionId", "string", or "json.RawMessage". It reads the rendered type,
// as shapeTable.site does, so the plan follows the declaration exactly.
func peelContainers(goType Code) ([]container, string) {
	var via []container
	s := fmt.Sprintf("%#v", goType)
	for {
		switch {
		case strings.HasPrefix(s, "*"):
			via, s = append(via, pointer), s[len("*"):]
		case strings.HasPrefix(s, "[]"):
			via, s = append(via, items), s[len("[]"):]
		case strings.HasPrefix(s, "map[string]"):
			via, s = append(via, members), s[len("map[string]"):]
		default:
			return via, s
		}
	}
}

// integerGoType returns the Go integer type of name, the Go type that a field
// holds: name itself when it is int or a type of formatTypes, the underlying
// type when name is a $defs entry that becomes a named integer type, and ""
// otherwise. A range check compares only such a value; raw JSON, such as a
// $defs entry with a $ref and a bound but no type, gets none.
func (b *typesBuilder) integerGoType(name string) string {
	if name == "int" {
		return name
	}
	for _, t := range formatTypes {
		if t.name == name {
			return name
		}
	}
	if def := b.schema.Defs[name]; def != nil && classify(def) == primitiveKind && ir.PrimaryType(def) == "integer" {
		return formatType(def.Format).name
	}
	return ""
}

// stringType reports whether name is a $defs entry that becomes a named
// string type: a closed or open enum, or a string.
func (b *typesBuilder) stringType(name string) bool {
	def := b.schema.Defs[name]
	if def == nil {
		return false
	}
	switch classify(def) {
	case enumKind, openEnumKind:
		return true
	case primitiveKind:
		return ir.PrimaryType(def) == "string"
	}
	return false
}

// validates reports whether the Go type of the $defs entry name has a
// Validate method that checks something: a union wrapper always (it checks
// that a variant is set), and a struct when its check plan is not empty. It
// is the one owner of that decision: a Validate method calls the Validate of
// a value only when validates says that it has one. A message type with an
// empty plan has a Validate that returns nil, which no one calls. A $ref
// cycle reads as false while it is being decided; schema 1.32.0 has none.
func (b *typesBuilder) validates(name string) bool {
	if v, ok := b.validated[name]; ok {
		return v
	}
	def := b.schema.Defs[name]
	if def == nil {
		return false
	}
	b.validated[name] = false
	v := false
	switch classify(def) {
	case unionKind:
		v = true
	case structKind:
		v = len(b.checkPlan(ir.Fields(def.Properties, def.Required), true, messageProps(name, def))) > 0
	}
	b.validated[name] = v
	return v
}

// isMessage reports whether the $defs entry name is a request, response, or
// notification. The generated dispatch calls the Validate method of every
// request and notification it reads.
func isMessage(name string) bool {
	return strings.HasSuffix(name, "Request") || strings.HasSuffix(name, "Response") || strings.HasSuffix(name, "Notification")
}

// messageProps returns the properties of the $defs entry name, a struct or a
// union, whose required plain strings must not be empty: all of its own
// properties when it is a message type, and none otherwise.
func messageProps(name string, def *load.Definition) map[string]bool {
	if !isMessage(name) {
		return nil
	}
	props := map[string]bool{}
	for prop := range def.Properties {
		props[prop] = true
	}
	return props
}

// emitChecks adds the statements of plan, the check plan of the struct or
// union wrapper recv, to the body of its Validate method.
func emitChecks(g *Group, recv string, plan []fieldCheck) {
	for _, c := range plan {
		value := Id(recv).Dot(c.field.Name)
		if c.unset != nil {
			g.If(value.Clone().Op("==").Add(c.unset)).Block(
				Return(Qual("fmt", "Errorf").Call(Lit(c.field.Prop + " is required"))),
			)
		}
		if c.bounds != nil {
			g.Add(eachHeld(value, c.via, c.field.Prop, nil, 0, checkRange(*c.bounds, c.boundsInInt64)))
		}
		if c.descends {
			g.Add(validateHeld(value, c.via, c.field.Prop, nil, 0))
		}
	}
}

// heldCheck returns the statement that checks v, one value that a field
// holds, or the value that v points to when ptr is set. path and args name
// the value as in validateHeld.
type heldCheck func(v *Statement, ptr bool, path string, args []Code) Code

// validateHeld returns the statement that calls Validate on each value that
// value holds through via. It returns the first error with path, the
// location of the value as a fmt format with args, in front; an empty path
// returns the error as it is. depth counts the variables that the enclosing
// statements declare, so that a nested one gets a name of its own.
func validateHeld(value *Statement, via []container, path string, args []Code, depth int) Code {
	return eachHeld(value, via, path, args, depth, func(v *Statement, _ bool, path string, args []Code) Code {
		err := Id("err")
		if path != "" {
			err = Qual("fmt", "Errorf").Call(append(append([]Code{Lit(path + ": %w")}, args...), Id("err"))...)
		}
		// Validate takes a pointer, so v can be one.
		return If(Id("err").Op(":=").Add(v.Clone()).Dot("Validate").Call(), Id("err").Op("!=").Nil()).Block(Return(err))
	})
}

// eachHeld returns the statement that runs check on each value that value
// holds through via, with path and args, a fmt format and its args, that
// name the value. depth is as in validateHeld.
func eachHeld(value *Statement, via []container, path string, args []Code, depth int, check heldCheck) Code {
	if len(via) == 0 {
		return check(value, false, path, args)
	}
	name := func(base string) *Statement {
		if depth == 0 {
			return Id(base)
		}
		return Id(fmt.Sprintf("%s%d", base, depth))
	}
	rest := via[1:]
	// at checks the value that ptr, a pointer that is not nil, points to.
	// The containers after it need the value itself, and check takes the
	// pointer.
	at := func(ptr *Statement, depth int) Code {
		if len(rest) == 0 {
			return check(ptr, true, path, args)
		}
		return eachHeld(Parens(Op("*").Add(ptr)), rest, path, args, depth, check)
	}
	switch via[0] {
	case pointer:
		return If(value.Clone().Op("!=").Nil()).Block(at(value.Clone(), depth))
	case nonZero:
		return If(value.Clone().Op("!=").Lit(0)).Block(check(value.Clone(), false, path, args))
	case patch:
		p := name("p")
		return If(p.Clone().Op(":=").Add(value.Clone()).Dot("Ptr").Call(), p.Clone().Op("!=").Nil()).Block(at(p.Clone(), depth+1))
	case items:
		i := name("i")
		return For(i.Clone().Op(":=").Range().Add(value.Clone())).Block(
			eachHeld(value.Clone().Index(i.Clone()), rest, path+"[%d]", append(append([]Code{}, args...), i.Clone()), depth+1, check),
		)
	default: // members
		k, member := name("k"), name("member")
		return For(List(k.Clone(), member.Clone()).Op(":=").Range().Add(value.Clone())).Block(
			eachHeld(member.Clone(), rest, path+"[%q]", append(append([]Code{}, args...), k.Clone()), depth+1, check),
		)
	}
}
