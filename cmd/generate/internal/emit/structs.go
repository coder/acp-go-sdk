package emit

import (
	"encoding/json"
	"fmt"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/ir"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

type fieldPlan struct {
	field           ir.Field
	fallbackJSON    string
	fallbackOnNull  bool
	fillNilOnEncode bool
}

func (p fieldPlan) hasFallback() bool {
	return p.fallbackJSON != ""
}

func plansFor(fields []ir.Field) []fieldPlan {
	plans := make([]fieldPlan, 0, len(fields))
	for _, field := range fields {
		plans = append(plans, planFor(field))
	}
	return plans
}

func planFor(field ir.Field) fieldPlan {
	p := fieldPlan{field: field}
	def := field.Def
	if def.Default != nil {
		defaultJSON, err := json.Marshal(def.Default)
		if err != nil {
			panic(fmt.Sprintf("encode the default of property %q: %v", field.Prop, err))
		}
		p.fallbackJSON = string(defaultJSON)
		p.fallbackOnNull = !includesNull(def)
		p.fillNilOnEncode = mapsToSliceOrMap(def)
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
	plans := plansFor(fields)
	f.Type().Id(name).Struct(fieldDecls(plans, nestedTypeNames)...)
	f.Line()

	defaulted := withFallback(plans)
	if len(defaulted) == 0 {
		return
	}
	f.Func().Params(Id("v").Id(name)).Id("MarshalJSON").Params().Params(Index().Byte(), Error()).BlockFunc(func(g *Group) {
		g.Type().Id("Alias").Id(name)
		g.Var().Id("a").Id("Alias")
		g.Id("a").Op("=").Id("Alias").Call(Id("v"))
		emitFillNil(g, plans, "a")
		g.Return(Qual("encoding/json", "Marshal").Call(Id("a")))
	})
	f.Line()
	f.Func().Params(Id("v").Op("*").Id(name)).Id("UnmarshalJSON").Params(Id("b").Index().Byte()).Error().BlockFunc(func(g *Group) {
		b.emitDecodeFields(g, name, "v", defaulted)
		g.Return(Nil())
	})
	f.Line()
}

func withFallback(plans []fieldPlan) []fieldPlan {
	var defaulted []fieldPlan
	for _, p := range plans {
		if p.hasFallback() {
			defaulted = append(defaulted, p)
		}
	}
	return defaulted
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
