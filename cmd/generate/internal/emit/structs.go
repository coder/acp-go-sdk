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

// plansFor plans the fields of the struct owner.
func (b *typesBuilder) plansFor(owner string, fields []ir.Field) []fieldPlan {
	plans := make([]fieldPlan, 0, len(fields))
	for _, field := range fields {
		plans = append(plans, b.planFor(owner, field))
	}
	return plans
}

func (b *typesBuilder) planFor(owner string, field ir.Field) fieldPlan {
	p := fieldPlan{field: field}
	def, lenient := field.Def, field.Lenient
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
		p.shape = b.shapes.site(owner, field)
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
		tag := field.Prop
		if !field.Required && !p.fillNilOnEncode {
			tag += ",omitempty"
		}
		var typ Code
		if nestedTypeName, ok := nestedTypeNames[field.Prop]; ok {
			typ = Id(nestedTypeName)
		} else {
			typ = fieldType(field)
		}
		decls = append(decls, Id(field.Name).Add(typ).Tag(map[string]string{"json": tag}))
	}
	return decls
}

// metaProperty is the property that ACP reserves on its types for
// implementations to attach data to.
const metaProperty = "_meta"

// fieldType returns the Go type of field, a property without a nested struct.
// A _meta whose members the schema leaves free is a Meta.
func fieldType(field ir.Field) Code {
	def := field.Def
	if field.Prop == metaProperty && ir.PrimaryType(def) == "object" && len(def.Properties) == 0 && def.AdditionalProperties.Schema == nil {
		return Id("Meta")
	}
	return jenTypeForOptional(def)
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

	if open || slices.ContainsFunc(plans, fieldPlan.hasFallback) {
		f.Func().Params(Id("v").Id(name)).Id("MarshalJSON").Params().Params(Index().Byte(), Error()).BlockFunc(func(g *Group) {
			g.Type().Id("Alias").Id(name)
			g.Var().Id("a").Id("Alias")
			g.Id("a").Op("=").Id("Alias").Call(Id("v"))
			emitFillNil(g, plans, "a")
			if !open {
				g.Return(Qual("encoding/json", "Marshal").Call(Id("a")))
				return
			}
			g.List(Id("named"), Id("err")).Op(":=").Qual("encoding/json", "Marshal").Call(Id("a"))
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
