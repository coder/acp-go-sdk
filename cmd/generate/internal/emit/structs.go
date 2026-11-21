package emit

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/ir"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

type fieldPlan struct {
	field                            ir.Field
	fallbackJSON                     string
	fallbackOnAbsent, fallbackOnNull bool
	fillNilOnEncode                  bool
	// patch says that the field is a patch field (ir.PatchFields), a
	// Patch of patchValueType.
	patch bool
	// nullAsAbsent says that the field is a patch field for which the schema
	// defines no clear (ir.NullAsAbsent): the decoder reads null as unset,
	// MarshalJSON leaves out a null Patch, and its doc says so.
	nullAsAbsent bool
	// keepEmpty says that the field is an optional slice or map whose schema
	// allows null: the Rust SDK reads it as an Option, so it writes an empty
	// value as [] or {} and leaves out only None, which is nil here.
	keepEmpty bool
	// shape is the shapeID constant that a lenient decoder checks the
	// decoded value, or each decoded item, against; nil for none.
	shape Code
	// nonNullItem is the Go type of the items or member values that the
	// decoder rejects null for (typesBuilder.nonNullItem); nil for none.
	nonNullItem Code
	// goType is the Go type of the field when it is not a patch field and
	// has no nested struct (typesBuilder.fieldType).
	goType Code
	// required says that the decoder fails when the JSON object lacks the
	// member, or holds null for it and takesNull is false (planFor).
	// encoding/json leaves such a member at its Go zero value, which
	// Validate cannot tell from a present zero value.
	required, takesNull bool
}

func (p fieldPlan) hasFallback() bool {
	return p.fallbackJSON != ""
}

// customDecode reports whether the generated UnmarshalJSON keeps the field
// raw and decodes it on its own.
func (p fieldPlan) customDecode() bool {
	lenient := p.field.Lenient
	return p.fallbackOnAbsent || p.fallbackOnNull || lenient.DefaultOnError || lenient.SkipInvalidItems || p.nonNullItem != nil || p.nullAsAbsent
}

// decoded reports whether the struct needs a generated UnmarshalJSON for the
// field: to decode it on its own, or to check that it is present.
func (p fieldPlan) decoded() bool {
	return p.customDecode() || p.required
}

// encodesPointer says that MarshalJSON encodes the field through a pointer
// that is nil, and so left out, when the field is unset or nil.
func (p fieldPlan) encodesPointer() bool {
	return p.patch || p.keepEmpty
}

// plansFor plans the fields of the struct owner.
func (b *typesBuilder) plansFor(owner string, fields []ir.Field) []fieldPlan {
	plans := make([]fieldPlan, 0, len(fields))
	for _, field := range fields {
		plans = append(plans, b.planFor(owner, field))
	}
	return plans
}

func (b *typesBuilder) planFor(owner string, field ir.Field) fieldPlan {
	def, lenient := field.Def, field.Lenient
	p := fieldPlan{
		field: field, patch: b.patch[def], nullAsAbsent: b.nullAsAbsent[def], nonNullItem: b.nonNullItem(field), goType: b.fieldType(field),
		// As in the Rust SDK, a required member must be present, and may be
		// null only where its schema accepts null or a hint reads null as
		// the default. A schema default stands in for an absent member.
		required:  field.Required && def.Default == nil,
		takesNull: acceptsNull(b.schema, def) || lenient.DefaultOnNull || lenient.DefaultOnError,
	}
	if p.patch {
		// A patch field reads absent, null, and a value apart, so it has no
		// fallback, and only default-on-error, with or without
		// skip-invalid-items, has a patch decoder.
		if def.Default != nil || lenient.DefaultOnNull || (lenient.SkipInvalidItems && !lenient.DefaultOnError) {
			panic(fmt.Sprintf("patch field %q of %q has a default or a hint that the generator does not support on a patch field", field.Prop, owner))
		}
		b.usesPatch = true
		if lenient.DefaultOnError {
			p.shape = b.shapes.site(owner, field, patchValueType(field))
			b.usesAbsentIfNull = b.usesAbsentIfNull || p.nullAsAbsent
		}
		return p
	}
	switch {
	case def.Default != nil:
		defaultJSON, err := json.Marshal(def.Default)
		if err != nil {
			panic(fmt.Sprintf("encode the default of property %q: %v", field.Prop, err))
		}
		p.fallbackJSON = string(defaultJSON)
		p.fallbackOnAbsent = true
		p.fallbackOnNull = !includesNull(def) || lenient.DefaultOnNull
		p.fillNilOnEncode = mapsToSliceOrMap(def)
	case field.Required && ir.PrimaryType(def) == "array" && !includesNull(def):
		p.fallbackJSON = "[]"
		p.fallbackOnNull = lenient.DefaultOnNull || lenient.DefaultOnError
		p.fillNilOnEncode = true
	}
	p.keepEmpty = !field.Required && includesNull(def) && mapsToSliceOrMap(def) && def.Default == nil && field.Prop != metaProperty
	if lenient.DefaultOnError || lenient.SkipInvalidItems {
		p.shape = b.shapes.site(owner, field, p.goType)
	}
	return p
}

// nonNullItem returns the Go type of the items of field, an array, or of its
// member values, a map, when the decoder must reject a null one: the type is a
// scalar, which encoding/json reads null into as its zero value, the item or
// value schema does not accept null, and the field has no hint that tolerates
// a bad value. It returns nil otherwise.
func (b *typesBuilder) nonNullItem(field ir.Field) Code {
	if field.Lenient.DefaultOnError || field.Lenient.SkipInvalidItems {
		return nil
	}
	def := field.Def
	if nonNull := nonNullAlternative(def); b.patch[def] && nonNull != nil {
		// A patch field holds a value of its schema without null
		// (patchValueType).
		def = nonNull
	}
	var item *load.Definition
	switch ir.PrimaryType(def) {
	case "array":
		item = def.Items
	case "object":
		if len(def.Properties) == 0 && def.Ref == "" && !freeMembers(def) {
			item = def.AdditionalProperties.Schema
		}
	}
	if item == nil || !b.scalar(item) || acceptsNull(b.schema, item) {
		return nil
	}
	return jenTypeFor(item)
}

// scalar reports whether the Go type of d is a string, a number, or a boolean,
// or a type that buildTypesJen defines as one of them.
func (b *typesBuilder) scalar(d *load.Definition) bool {
	if len(d.AllOf) > 0 && d.Ref == "" {
		// The item takes the type of a part of its allOf, the one that
		// jenTypeFor picks: the first $ref, else the first part.
		parts := slices.DeleteFunc(slices.Clone(d.AllOf), func(e *load.Definition) bool { return e == nil })
		if i := slices.IndexFunc(parts, func(e *load.Definition) bool { return e.Ref != "" }); i >= 0 {
			return b.scalar(parts[i])
		}
		if len(parts) > 0 {
			return b.scalar(parts[0])
		}
	}
	if d.Ref != "" {
		def := b.schema.Defs[strings.TrimPrefix(d.Ref, "#/$defs/")]
		switch {
		case def == nil:
			return false
		case len(def.Enum) > 0, isStringConstUnion(def), isOpenStringEnum(def):
			return true
		case len(def.AnyOf) > 0, len(def.OneOf) > 0:
			return false
		}
		d = def
	}
	switch ir.PrimaryType(d) {
	case "string", "integer", "number", "boolean":
		return true
	}
	return len(d.Enum) > 0
}

func mapsToSliceOrMap(d *load.Definition) bool {
	switch ir.PrimaryType(d) {
	case "array":
		return true
	case "object":
		return len(d.Properties) == 0 && d.Ref == ""
	}
	return false
}

// nullAsAbsentNote is the doc of a patch field for which the schema defines
// no clear (ir.NullAsAbsent).
const nullAsAbsentNote = "ACP v1 defines no clear for this field, so null leaves it unchanged, as\n" +
	"absent does: the SDK reads null as unset, and leaves out a null Patch."

func fieldDecls(plans []fieldPlan, nestedTypeNames map[string]string) []Code {
	decls := []Code{}
	for _, p := range plans {
		field := p.field
		if field.Def.Description != "" {
			decls = appendDocComments(decls, field.Def.Description)
		}
		if field.Def.Default != nil {
			if field.Def.Description != "" {
				decls = append(decls, Comment(""))
			}
			decls = append(decls, Comment(fmt.Sprintf("Defaults to %s if unset.", p.fallbackJSON)))
		}
		if p.patch && p.nullAsAbsent {
			if field.Def.Description != "" {
				decls = append(decls, Comment(""))
			}
			decls = appendDocComments(decls, nullAsAbsentNote)
		}
		decls = append(decls, fieldDecl(p, nestedTypeNames, false))
	}
	return decls
}

// fieldDecl declares the struct field of p. With pointers, a field that
// encodesPointer is a pointer with omitempty, as the struct that MarshalJSON
// encodes has it.
func fieldDecl(p fieldPlan, nestedTypeNames map[string]string, pointers bool) Code {
	field := p.field
	tag := field.Prop
	if !field.Required && !p.fillNilOnEncode && (!p.patch || pointers) {
		tag += ",omitempty"
	}
	var typ Code
	nestedTypeName, ok := nestedTypeNames[field.Prop]
	value := patchValueType(field)
	if ok {
		// An inline object with properties has a struct of its own.
		value = Id(nestedTypeName)
	}
	switch {
	case p.patch && pointers:
		typ = Op("*").Id("Patch").Types(value)
	case p.patch:
		typ = Id("Patch").Types(value)
	case ok:
		typ = Id(nestedTypeName)
	case p.keepEmpty && pointers:
		typ = Op("*").Add(p.goType)
	default:
		typ = p.goType
	}
	return Id(field.Name).Add(typ).Tag(map[string]string{"json": tag})
}

// metaProperty is the property that ACP reserves on its types for
// implementations to attach data to.
const metaProperty = "_meta"

// fieldType returns the Go type of field, a property without a nested struct:
// inPlaceType, behind a pointer when the value would hold the struct that
// holds field (indirect).
func (b *typesBuilder) fieldType(field ir.Field) Code {
	if b.indirect[field.Def] {
		return Op("*").Add(inPlaceType(field))
	}
	return inPlaceType(field)
}

// inPlaceType returns the Go type of field, a property without a nested
// struct, when it holds its value in place. A _meta whose members the schema
// leaves free is a Meta.
func inPlaceType(field ir.Field) Code {
	def := field.Def
	if field.Prop == metaProperty && ir.PrimaryType(def) == "object" && len(def.Properties) == 0 && freeMembers(def) {
		return Id("Meta")
	}
	return jenTypeForOptional(def)
}

// patchValueType returns the Go type of the value of field, a patch field:
// the type of its schema without null.
func patchValueType(field ir.Field) Code {
	def := field.Def
	if field.Prop == metaProperty {
		return inPlaceType(field)
	}
	if nonNull := nonNullAlternative(def); nonNull != nil {
		return jenTypeFor(nonNull)
	}
	if types, ok := def.Type.([]any); ok {
		withoutNull := *def
		for _, t := range types {
			if t != "null" {
				withoutNull.Type = t
			}
		}
		return jenTypeFor(&withoutNull)
	}
	return jenTypeFor(def)
}

// additionalPropertiesField is the field of a struct whose schema says
// "additionalProperties": true. It holds the members the schema does not name,
// each as the JSON that was read, so that a number keeps every digit.
const additionalPropertiesField = "AdditionalProperties"

// emitStruct declares the struct name with one field per entry of fields. open
// says that the schema has "additionalProperties": true. plain names the
// properties whose required plain strings must not be empty (checkPlan). The
// struct gets a Validate when its check plan is not empty, and always when it
// is a message type, whose Validate the dispatch calls. emitStruct reports
// whether that Validate checks something.
func (b *typesBuilder) emitStruct(name string, fields []ir.Field, nestedTypeNames map[string]string, open bool, plain map[string]bool) bool {
	f := b.f
	plans := b.plansFor(name, fields)
	decls := fieldDecls(plans, nestedTypeNames)
	if open {
		for _, field := range fields {
			if field.Name == additionalPropertiesField {
				panic(fmt.Sprintf("struct %q has additionalProperties and a property that maps to the field %q", name, field.Name))
			}
		}
		b.usesAdditionalProperties = true
		decls = append(decls,
			Comment("AdditionalProperties holds the members that the schema does not name."),
			Id(additionalPropertiesField).Map(String()).Qual("encoding/json", "RawMessage").Tag(map[string]string{"json": "-"}),
		)
	}
	f.Type().Id(name).Struct(decls...)
	f.Line()

	pointers := slices.ContainsFunc(plans, fieldPlan.encodesPointer)
	if open || pointers || slices.ContainsFunc(plans, fieldPlan.hasFallback) {
		f.Func().Params(Id("v").Id(name)).Id("MarshalJSON").Params().Params(Index().Byte(), Error()).BlockFunc(func(g *Group) {
			g.Type().Id("Alias").Id(name)
			g.Var().Id("a").Id("Alias")
			g.Id("a").Op("=").Id("Alias").Call(Id("v"))
			emitFillNil(g, plans, "a")
			encoded := Id("a")
			if pointers {
				// The same fields in the same order, but a field that
				// encodesPointer is a pointer that is nil, and so left out,
				// when the field is unset or nil.
				var shadow []Code
				values := Dict{}
				for _, p := range plans {
					shadow = append(shadow, fieldDecl(p, nestedTypeNames, true))
					value := Id("a").Dot(p.field.Name)
					switch {
					case p.patch && p.nullAsAbsent:
						value = value.Dot("setOrNil").Call()
					case p.patch:
						value = value.Dot("orNil").Call()
					case p.keepEmpty:
						b.usesNilIf = true
						value = Id("nilIf").Call(Id("a").Dot(p.field.Name).Op("==").Nil(), Op("&").Add(value))
					}
					values[Id(p.field.Name)] = value
				}
				g.Type().Id("wire").Struct(shadow...)
				encoded = Id("wire").Values(values)
			}
			if !open {
				g.Return(Qual("encoding/json", "Marshal").Call(encoded))
				return
			}
			g.List(Id("named"), Id("err")).Op(":=").Qual("encoding/json", "Marshal").Call(encoded)
			g.If(Id("err").Op("!=").Nil()).Block(Return(Nil(), Id("err")))
			g.Return(Id("withAdditionalProperties").Call(Id("named"), Id("v").Dot(additionalPropertiesField)))
		})
		f.Line()
	}

	if open || slices.ContainsFunc(plans, fieldPlan.decoded) {
		f.Func().Params(Id("v").Op("*").Id(name)).Id("UnmarshalJSON").Params(Id("b").Index().Byte()).Error().BlockFunc(func(g *Group) {
			// emitRequireMembers reads a zero required field as a member that
			// the JSON may lack, so each one starts at zero. The JSON must set
			// it anyway; the other fields keep what a reused value holds.
			var reset []Code
			for _, p := range plans {
				if p.required {
					reset = append(reset, Id("v").Dot(p.field.Name).Op("=").Id("fresh").Dot(p.field.Name))
				}
			}
			if len(reset) > 0 {
				g.Var().Id("fresh").Id(name)
				for _, c := range reset {
					g.Add(c)
				}
			}
			b.emitDecodeFields(g, name, "v", plans)
			if open {
				named := []Code{Id("b")}
				for _, field := range fields {
					named = append(named, Lit(field.Prop))
				}
				g.Id("v").Dot(additionalPropertiesField).Op("=").Id("additionalProperties").Call(named...)
			}
			g.Return(Nil())
		})
		f.Line()
	}

	checks := b.checkPlan(fields, nestedTypeNames != nil, plain)
	if len(checks) > 0 || isMessage(name) {
		emitValidate(f, "v", name, func(g *Group) { emitChecks(g, "v", checks) })
	}
	return len(checks) > 0
}

// emitEncodeSupport declares the helpers that the MarshalJSON methods call.
func (b *typesBuilder) emitEncodeSupport() {
	if !b.usesNilIf {
		return
	}
	f := b.f
	f.Comment("nilIf returns nil when isNil holds, and p otherwise. MarshalJSON passes it")
	f.Comment("a list or map that keeps an empty value, so that omitempty leaves out only a")
	f.Comment("nil one.")
	f.Func().Id("nilIf").Types(Id("T").Any()).Params(Id("isNil").Bool(), Id("p").Op("*").Id("T")).Op("*").Id("T").Block(
		If(Id("isNil")).Block(Return(Nil())),
		Return(Id("p")),
	)
	f.Line()
}

func customDecoded(plans []fieldPlan) []fieldPlan {
	var decoded []fieldPlan
	for _, p := range plans {
		if p.customDecode() {
			decoded = append(decoded, p)
		}
	}
	return decoded
}

func emitFillNil(g *Group, plans []fieldPlan, target string) {
	for _, p := range plans {
		if p.fillNilOnEncode {
			g.If(Id(target).Dot(p.field.Name).Op("==").Nil()).Block(decodeFallback(p, Id(target)))
		}
	}
}

func (b *typesBuilder) emitNestedStructs(parent string, fields []ir.Field) map[string]string {
	f, usedTypeNames := b.f, b.usedTypeNames
	nestedTypeNames := map[string]string{}
	for _, field := range fields {
		prop := field.Def
		if !inlineStruct(prop) {
			continue
		}
		nestedTypeName := generateNestedTypeName(parent, field.Prop, usedTypeNames)
		nestedTypeNames[field.Prop] = nestedTypeName

		if usedTypeNames[nestedTypeName] {
			panic(fmt.Sprintf("BUG: nested type name collision: %q already registered", nestedTypeName))
		}
		usedTypeNames[nestedTypeName] = true

		if prop.Description != "" {
			emitDocComment(f, prop.Description)
		}
		b.emitStruct(nestedTypeName, ir.Fields(prop.Properties, prop.Required), nil, opensMembers(prop), nil)
	}
	return nestedTypeNames
}

// inlineStruct reports whether prop, a property of a $defs entry, is an
// inline object with properties, which gets a struct of its own.
func inlineStruct(prop *load.Definition) bool {
	return prop.Ref == "" && ir.PrimaryType(prop) == "object" && len(prop.Properties) > 0
}
