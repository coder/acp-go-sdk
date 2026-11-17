package emit

func decodeFallback(p fieldPlan, target Code) Code {
	return Qual("encoding/json", "Unmarshal").Call(Index().Byte().Parens(Lit(p.fallbackJSON)), Op("&").Add(target).Dot(p.field.Name))
}

func (b *typesBuilder) emitDecodeFields(g *Group, name, recv string, plans []fieldPlan) {
	b.usesDecodeHelpers = true
	shadow := []Code{Op("*").Id("plain")}
	for _, p := range plans {
		shadow = append(shadow, Id(p.field.Name).Qual("encoding/json", "RawMessage").Tag(map[string]string{"json": p.field.Prop}))
	}
	g.Type().Id("plain").Id(name)
	// encoding/json names this local type in decode errors, so it takes name.
	g.Type().Id(name).Struct(shadow...)
	g.Id("raw").Op(":=").Id(name).Values(Dict{Id("plain"): Parens(Op("*").Id("plain")).Call(Id(recv))})
	g.If(Id("err").Op(":=").Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("raw")), Id("err").Op("!=").Nil()).Block(
		Return(Id("plainFieldError").Call(Id("err"))),
	)
	for _, p := range plans {
		g.Add(decodeStmt(p, name, recv))
	}
}

func decodeStmt(p fieldPlan, structName, recv string) Code {
	raw := func() *Statement { return Id("raw").Dot(p.field.Name) }
	needsFallback := Op("!").Id("decoded")
	if !p.fallbackOnNull {
		notNull := Id("string").Call(raw()).Op("!=").Lit("null")
		needsFallback = needsFallback.Op("&&").Add(notNull)
	}
	return If(List(Id("decoded"), Id("err")).Op(":=").Id("decodeField").Call(raw(), Op("&").Id(recv).Dot(p.field.Name)), Id("err").Op("!=").Nil()).Block(
		Return(Id("propertyError").Call(Id("err"), Lit(structName), Lit(p.field.Prop))),
	).Else().If(needsFallback).Block(Id("setDefault").Call(Op("&").Id(recv).Dot(p.field.Name), Lit(p.fallbackJSON)))
}

func (b *typesBuilder) emitSupport() {
	if !b.usesDecodeHelpers {
		return
	}
	f := b.f
	typeErr := func() Code {
		return List(Id("typeErr"), Id("ok")).Op(":=").Id("err").Assert(Op("*").Qual("encoding/json", "UnmarshalTypeError"))
	}

	f.Comment("decodeField decodes the raw JSON of a property into dst. An absent property")
	f.Comment("(a nil raw) leaves dst alone. A null property, or one that is not a valid T,")
	f.Comment("resets dst to its zero value.")
	f.Func().Id("decodeField").Types(Id("T").Any()).Params(Id("raw").Qual("encoding/json", "RawMessage"), Id("dst").Op("*").Id("T")).Params(Id("decoded").Bool(), Id("err").Error()).Block(
		If(Id("raw").Op("==").Nil()).Block(Return(Lit(false), Nil())),
		Var().Id("v").Id("T"),
		If(Id("string").Call(Id("raw")).Op("!=").Lit("null")).Block(
			If(Id("err").Op("=").Qual("encoding/json", "Unmarshal").Call(Id("raw"), Op("&").Id("v")), Id("err").Op("!=").Nil()).Block(
				Id("v").Op("=").Op("*").Id("new").Call(Id("T")),
			),
			Id("decoded").Op("=").Id("err").Op("==").Nil(),
		),
		Op("*").Id("dst").Op("=").Id("v"),
		Return(Id("decoded"), Id("err")),
	)
	f.Line()

	f.Comment("setDefault replaces dst with the schema default that fallback encodes.")
	f.Comment("Decoding the default into dst would keep what a reused value holds.")
	f.Func().Id("setDefault").Types(Id("T").Any()).Params(Id("dst").Op("*").Id("T"), Id("fallback").String()).Block(
		Var().Id("v").Id("T"),
		Qual("encoding/json", "Unmarshal").Call(Index().Byte().Parens(Id("fallback")), Op("&").Id("v")),
		Op("*").Id("dst").Op("=").Id("v"),
	)
	f.Line()

	f.Comment("plainFieldError names the field in err, from a decode through an embedded")
	f.Comment("pointer named plain, as encoding/json names a field of the struct itself. Go")
	f.Comment("1.24 and later put the name of the embedded field first.")
	f.Func().Id("plainFieldError").Params(Id("err").Error()).Error().Block(
		If(typeErr(), Id("ok")).Block(
			Id("typeErr").Dot("Field").Op("=").Qual("strings", "TrimPrefix").Call(Id("typeErr").Dot("Field"), Lit("plain.")),
		),
		Return(Id("err")),
	)
	f.Line()

	f.Comment("propertyError names the struct and the field in err, from decoding the raw")
	f.Comment("property prop on its own, as encoding/json names a field of a struct.")
	f.Func().Id("propertyError").Params(Id("err").Error(), List(Id("structName"), Id("prop")).String()).Error().Block(
		If(typeErr(), Id("ok")).Block(
			Id("typeErr").Dot("Struct").Op("=").Id("structName"),
			If(Id("typeErr").Dot("Field").Op("==").Lit("")).Block(
				Id("typeErr").Dot("Field").Op("=").Id("prop"),
			).Else().Block(
				Id("typeErr").Dot("Field").Op("=").Id("prop").Op("+").Lit(".").Op("+").Id("typeErr").Dot("Field"),
			),
		),
		Return(Id("err")),
	)
	f.Line()
}
