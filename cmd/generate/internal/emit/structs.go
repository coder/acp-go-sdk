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
	enumValidator                    Code
}

func (p fieldPlan) hasFallback() bool {
	return p.fallbackJSON != ""
}

func (p fieldPlan) customDecode() bool {
	lenient := p.field.Lenient
	return p.fallbackOnAbsent || p.fallbackOnNull || lenient.DefaultOnError || lenient.SkipInvalidItems
}

func (b *typesBuilder) plansFor(fields []ir.Field) []fieldPlan {
	plans := make([]fieldPlan, 0, len(fields))
	for _, field := range fields {
		plans = append(plans, b.planFor(field))
	}
	return plans
}

func (b *typesBuilder) planFor(field ir.Field) fieldPlan {
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
	switch {
	case lenient.SkipInvalidItems:
		p.enumValidator = b.enumValidator(namedTypeRef(def.Items), false)
	case lenient.DefaultOnError:
		if alt := nonNullAlternative(def); alt != nil {
			p.enumValidator = b.enumValidator(alt.Ref, true)
		} else {
			p.enumValidator = b.enumValidator(namedTypeRef(def), false)
		}
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

// namedTypeRef returns the $ref that jenTypeFor resolves d to a named type
// with: its own, or that of an allOf around one.
func namedTypeRef(d *load.Definition) string {
	if d == nil {
		return ""
	}
	if d.Ref == "" {
		for _, part := range d.AllOf {
			if part != nil && part.Ref != "" {
				return part.Ref
			}
		}
	}
	return d.Ref
}

func (b *typesBuilder) enumValidator(ref string, ptr bool) Code {
	name := strings.TrimPrefix(ref, "#/$defs/")
	if def := b.schema.Defs[name]; def == nil || (len(def.Enum) == 0 && !isStringConstUnion(def)) {
		return nil
	}
	b.validatedEnums[name] = true
	if ptr {
		return Parens(Op("*").Id(name)).Dot("valid")
	}
	return Id(name).Dot("valid")
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
		var fieldType Code
		if nestedTypeName, ok := nestedTypeNames[field.Prop]; ok {
			fieldType = Id(nestedTypeName)
		} else {
			fieldType = jenTypeForOptional(field.Def)
		}
		decls = append(decls, Id(field.Name).Add(fieldType).Tag(map[string]string{"json": tag}))
	}
	return decls
}

func (b *typesBuilder) emitStruct(name string, fields []ir.Field, nestedTypeNames map[string]string) {
	f := b.f
	plans := b.plansFor(fields)
	f.Type().Id(name).Struct(fieldDecls(plans, nestedTypeNames)...)
	f.Line()

	if slices.ContainsFunc(plans, fieldPlan.hasFallback) {
		f.Func().Params(Id("v").Id(name)).Id("MarshalJSON").Params().Params(Index().Byte(), Error()).BlockFunc(func(g *Group) {
			g.Type().Id("Alias").Id(name)
			g.Var().Id("a").Id("Alias")
			g.Id("a").Op("=").Id("Alias").Call(Id("v"))
			emitFillNil(g, plans, "a")
			g.Return(Qual("encoding/json", "Marshal").Call(Id("a")))
		})
		f.Line()
	}

	if decoded := customDecoded(plans); len(decoded) > 0 {
		f.Func().Params(Id("v").Op("*").Id(name)).Id("UnmarshalJSON").Params(Id("b").Index().Byte()).Error().BlockFunc(func(g *Group) {
			b.emitDecodeFields(g, name, "v", decoded)
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
		b.emitStruct(nestedTypeName, ir.Fields(prop.Properties, prop.Required), nil)
	}
	return nestedTypeNames
}
