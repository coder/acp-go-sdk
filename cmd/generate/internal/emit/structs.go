package emit

import (
	"encoding/json"
	"fmt"
	"slices"

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
	// defines no clear (ir.NullAsAbsent), so its doc says how to read null.
	nullAsAbsent bool
	// shape is the shapeID constant that a lenient decoder checks the
	// decoded value, or each decoded item, against; nil for none.
	shape Code
}

func (p fieldPlan) hasFallback() bool {
	return p.fallbackJSON != ""
}

func (p fieldPlan) customDecode() bool {
	lenient := p.field.Lenient
	return p.fallbackOnAbsent || p.fallbackOnNull || lenient.DefaultOnError || lenient.SkipInvalidItems
}

// patches returns the patch fields of plans.
func patches(plans []fieldPlan) []fieldPlan {
	var out []fieldPlan
	for _, p := range plans {
		if p.patch {
			out = append(out, p)
		}
	}
	return out
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
	p := fieldPlan{field: field, patch: b.patch[field.Def], nullAsAbsent: b.nullAsAbsent[field.Def]}
	def, lenient := field.Def, field.Lenient
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
	if lenient.DefaultOnError || lenient.SkipInvalidItems {
		p.shape = b.shapes.site(owner, field, fieldType(field))
	}
	return p
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
const nullAsAbsentNote = "ACP v1 does not say that null clears this field, and the Rust SDK\n" +
	"reads null here as absent: treat IsNull like IsUnset."

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

// fieldDecl declares the struct field of p. With patchPointers, a patch field
// is a pointer to its Patch with omitempty, as the struct that MarshalJSON
// encodes has it.
func fieldDecl(p fieldPlan, nestedTypeNames map[string]string, patchPointers bool) Code {
	field := p.field
	tag := field.Prop
	if !field.Required && !p.fillNilOnEncode && (!p.patch || patchPointers) {
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
	case p.patch && patchPointers:
		typ = Op("*").Id("Patch").Types(value)
	case p.patch:
		typ = Id("Patch").Types(value)
	case ok:
		typ = Id(nestedTypeName)
	default:
		typ = fieldType(field)
	}
	return Id(field.Name).Add(typ).Tag(map[string]string{"json": tag})
}

// metaProperty is the property that ACP reserves on its types for
// implementations to attach data to.
const metaProperty = "_meta"

// fieldType returns the Go type of field, a property without a nested struct.
// A _meta whose members the schema leaves free is a Meta.
func fieldType(field ir.Field) Code {
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
		return fieldType(field)
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
// says that the schema has "additionalProperties": true.
func (b *typesBuilder) emitStruct(name string, fields []ir.Field, nestedTypeNames map[string]string, open bool) {
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

	patched := patches(plans)
	if open || len(patched) > 0 || slices.ContainsFunc(plans, fieldPlan.hasFallback) {
		f.Func().Params(Id("v").Id(name)).Id("MarshalJSON").Params().Params(Index().Byte(), Error()).BlockFunc(func(g *Group) {
			g.Type().Id("Alias").Id(name)
			g.Var().Id("a").Id("Alias")
			g.Id("a").Op("=").Id("Alias").Call(Id("v"))
			emitFillNil(g, plans, "a")
			encoded := Id("a")
			if len(patched) > 0 {
				// The same fields in the same order, but a patch field is a
				// pointer that is nil, and so left out, when it is unset.
				var shadow []Code
				values := Dict{}
				for _, p := range plans {
					shadow = append(shadow, fieldDecl(p, nestedTypeNames, true))
					value := Id("a").Dot(p.field.Name)
					if p.patch {
						value = value.Dot("orNil").Call()
					}
					values[Id(p.field.Name)] = value
				}
				g.Type().Id("patched").Struct(shadow...)
				encoded = Id("patched").Values(values)
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

	if decoded := customDecoded(plans); open || len(decoded) > 0 {
		f.Func().Params(Id("v").Op("*").Id(name)).Id("UnmarshalJSON").Params(Id("b").Index().Byte()).Error().BlockFunc(func(g *Group) {
			b.emitDecodeFields(g, name, "v", decoded)
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
		if prop.Ref != "" || ir.PrimaryType(prop) != "object" || len(prop.Properties) == 0 {
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
		b.emitStruct(nestedTypeName, ir.Fields(prop.Properties, prop.Required), nil, opensMembers(prop))
	}
	return nestedTypeNames
}
