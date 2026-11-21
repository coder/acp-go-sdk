package emit

import (
	"fmt"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

func decodeFallback(p fieldPlan, target Code) Code {
	return Qual("encoding/json", "Unmarshal").Call(Index().Byte().Parens(Lit(p.fallbackJSON)), Op("&").Add(target).Dot(p.field.Name))
}

// emitDecodeFields decodes the JSON object b into recv, a struct name with the
// fields of plans, and fails when b holds a property twice, apart from the
// discriminator of a catch-all variant (repeatable). A field that customDecode
// keeps raw decodes on its own, and the others in place. Then it checks the
// required members (emitRequireMembers).
func (b *typesBuilder) emitDecodeFields(g *Group, name, recv string, plans []fieldPlan) {
	b.usesDecodeHelpers = true
	rawPlans := customDecoded(plans)
	shadow := []Code{Op("*").Id("plain")}
	for _, p := range rawPlans {
		shadow = append(shadow, Id(p.field.Name).Qual("encoding/json", "RawMessage").Tag(map[string]string{"json": p.field.Prop}))
	}
	g.Type().Id("plain").Id(name)
	// encoding/json names this local type in decode errors, so it takes name.
	g.Type().Id(name).Struct(shadow...)
	g.Id("raw").Op(":=").Id(name).Values(Dict{Id("plain"): Parens(Op("*").Id("plain")).Call(Id(recv))})
	g.If(Id("err").Op(":=").Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("raw")), Id("err").Op("!=").Nil()).Block(
		Return(Id("plainFieldError").Call(Id("err"))),
	)
	// As the Rust SDK does, the object may hold each property once.
	props := []Code{Id("b"), Id(recv)}
	for _, p := range plans {
		if p.field.Prop != b.repeatable[name] {
			props = append(props, Lit(p.field.Prop))
		}
	}
	if len(props) > 2+maxProps {
		panic(fmt.Sprintf("struct %q has more than %d properties, which rejectDuplicateMembers cannot track", name, maxProps))
	}
	// A catch-all whose only property is its discriminator makes the call
	// too, without names, so that a property that the unstable schema adds
	// only adds a name (stablecheck).
	g.If(Id("err").Op(":=").Id("rejectDuplicateMembers").Call(props...), Id("err").Op("!=").Nil()).Block(Return(Id("err")))
	for _, p := range rawPlans {
		if p.nonNullItem != nil {
			check := Id("rejectNullItems").Types(p.nonNullItem).Call(Id("raw").Dot(p.field.Name))
			g.If(Id("err").Op(":=").Add(check), Id("err").Op("!=").Nil()).Block(propertyFail(name, p.field.Prop))
		}
		g.Add(decodeStmt(p, name, recv))
	}
	emitRequireMembers(g, name, recv, plans)
}

// maxProps is the most properties that rejectDuplicateMembers tracks.
const maxProps = 64

// emitRequireMembers fails the decode of the struct name when the JSON object
// b lacks a required member of plans, or holds null for one that does not
// take null. A member that is absent or null holds its Go zero value in a
// fresh value, so the members of b are read only when a required one may be
// absent (maybeAbsent).
// A struct's UnmarshalJSON sets each required field to zero first
// (emitStruct), and a union's starts from an empty wrapper, so a field that a
// reused value held cannot hide an absent member.
func emitRequireMembers(g *Group, name, recv string, plans []fieldPlan) {
	var anyAbsent *Statement
	var checks []Code
	for _, p := range plans {
		if !p.required {
			continue
		}
		field := Op("&").Id(recv).Dot(p.field.Name)
		if anyAbsent == nil {
			anyAbsent = Id("maybeAbsent").Call(field)
		} else {
			anyAbsent = anyAbsent.Op("||").Id("maybeAbsent").Call(field)
		}
		check := Id("requireMember").Call(Id("members"), field, Lit(p.field.Prop), Lit(p.takesNull))
		checks = append(checks, If(Id("err").Op(":=").Add(check), Id("err").Op("!=").Nil()).Block(propertyFail(name, p.field.Prop)))
	}
	if anyAbsent == nil {
		return
	}
	g.If(anyAbsent).Block(append([]Code{Id("members").Op(":=").Id("jsonMembers").Call(Id("b"))}, checks...)...)
}

// propertyFail returns the error err of decoding the raw property prop of
// structName.
func propertyFail(structName, prop string) Code {
	return Return(Id("propertyError").Call(Id("err"), Lit(structName), Lit(prop)))
}

func decodeStmt(p fieldPlan, structName, recv string) Code {
	raw := func() *Statement { return Id("raw").Dot(p.field.Name) }
	args := []Code{raw(), Op("&").Id(recv).Dot(p.field.Name)}
	if p.shape != nil {
		args = append(args, p.shape)
	}
	needsFallback := func(notDecoded *Statement) *Statement {
		if !p.fallbackOnAbsent {
			notAbsent := raw().Op("!=").Nil()
			notDecoded = notDecoded.Op("&&").Add(notAbsent)
		}
		if !p.fallbackOnNull {
			notNull := Id("string").Call(raw()).Op("!=").Lit("null")
			notDecoded = notDecoded.Op("&&").Add(notNull)
		}
		return notDecoded
	}
	fallback := Id("setDefault").Call(Op("&").Id(recv).Dot(p.field.Name), Lit(p.fallbackJSON))
	fail := propertyFail(structName, p.field.Prop)

	if p.patch && !p.field.Lenient.DefaultOnError {
		// A patch field without hints is raw only for its null item check
		// (nonNullItem), or to read null as absent (nullAsAbsent).
		// Patch.UnmarshalJSON reads it as it would in place.
		decode := Qual("encoding/json", "Unmarshal").Call(raw(), Op("&").Id(recv).Dot(p.field.Name))
		present := raw().Op("!=").Nil()
		if p.nullAsAbsent {
			present = present.Op("&&").Id("string").Call(raw()).Op("!=").Lit("null")
		}
		return If(present).Block(If(Id("err").Op(":=").Add(decode), Id("err").Op("!=").Nil()).Block(fail))
	}
	if p.patch {
		// planFor allows only default-on-error on a lenient patch field.
		helper := "decodePatch"
		if p.field.Lenient.SkipInvalidItems {
			helper = "decodePatchItems"
		}
		if p.nullAsAbsent {
			args[0] = Id("absentIfNull").Call(raw())
		}
		return Id(helper).Call(args...)
	}

	if p.field.Lenient.DefaultOnError {
		helper := "decodeLenient"
		if p.field.Lenient.SkipInvalidItems {
			helper = "decodeLenientItems"
		}
		decoded := Id(helper).Call(args...)
		if !p.hasFallback() {
			return decoded
		}
		return If(needsFallback(Op("!").Add(decoded))).Block(fallback)
	}

	helper := "decodeField"
	if p.field.Lenient.SkipInvalidItems {
		helper = "decodeItems"
	}
	if !p.fallbackOnAbsent && !p.fallbackOnNull {
		return If(List(Id("_"), Id("err")).Op(":=").Id(helper).Call(args...), Id("err").Op("!=").Nil()).Block(fail)
	}
	return If(List(Id("decoded"), Id("err")).Op(":=").Id(helper).Call(args...), Id("err").Op("!=").Nil()).Block(fail).
		Else().If(needsFallback(Op("!").Id("decoded"))).Block(fallback)
}

func (b *typesBuilder) emitSupport() {
	f := b.f
	b.shapes.emit(f)
	if !b.usesDecodeHelpers {
		return
	}

	rawParam := func() Code { return Id("raw").Qual("encoding/json", "RawMessage") }
	checkParam := func() Code { return Id("check").Op("...").Id("shapeID") }
	typeErr := func() Code {
		return List(Id("typeErr"), Id("ok")).Op(":=").Id("err").Assert(Op("*").Qual("encoding/json", "UnmarshalTypeError"))
	}

	f.Comment("decodeField decodes the raw JSON of a property into dst. An absent property")
	f.Comment("(a nil raw) leaves dst alone. A null property, or one that is not a valid T,")
	f.Comment("resets dst to its zero value.")
	f.Func().Id("decodeField").Types(Id("T").Any()).Params(rawParam(), Id("dst").Op("*").Id("T")).Params(Id("decoded").Bool(), Id("err").Error()).Block(
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

	f.Comment("decodeLenient decodes a property that has x-deserialize-default-on-error: a")
	f.Comment("value that is not a valid T, or that does not fit the shape check, is not an")
	f.Comment("error. It leaves dst at its zero value and returns false.")
	f.Func().Id("decodeLenient").Types(Id("T").Any()).Params(rawParam(), Id("dst").Op("*").Id("T"), checkParam()).Bool().Block(
		List(Id("decoded"), Id("_")).Op(":=").Id("decodeField").Call(Id("raw"), Id("dst")),
		For(List(Id("_"), Id("id")).Op(":=").Range().Id("check")).Block(
			If(Id("decoded").Op("&&").Op("!").Id("id").Dot("accepts").Call(Id("raw"))).Block(
				Op("*").Id("dst").Op("=").Op("*").Id("new").Call(Id("T")),
				Return(Lit(false)),
			),
		),
		Return(Id("decoded")),
	)
	f.Line()

	f.Comment("decodeItems decodes an array property that has")
	f.Comment("x-deserialize-skip-invalid-items: it drops the items that are null, that are")
	f.Comment("not a valid T, or that do not fit the shape check.")
	f.Func().Id("decodeItems").Types(Id("T").Any()).Params(rawParam(), Id("dst").Op("*").Index().Id("T"), checkParam()).Params(Id("decoded").Bool(), Id("err").Error()).Block(
		Var().Id("items").Index().Qual("encoding/json", "RawMessage"),
		If(List(Id("decoded"), Id("err")).Op("=").Id("decodeField").Call(Id("raw"), Op("&").Id("items")), Op("!").Id("decoded")).Block(
			If(Id("raw").Op("!=").Nil()).Block(Op("*").Id("dst").Op("=").Nil()),
			Return(Lit(false), Id("err")),
		),
		Id("kept").Op(":=").Make(Index().Id("T"), Lit(0), Id("len").Call(Id("items"))),
		For(List(Id("_"), Id("item")).Op(":=").Range().Id("items")).Block(
			Var().Id("v").Id("T"),
			If(Id("decodeLenient").Call(Id("item"), Op("&").Id("v"), Id("check").Op("..."))).Block(
				Id("kept").Op("=").Append(Id("kept"), Id("v")),
			),
		),
		Op("*").Id("dst").Op("=").Id("kept"),
		Return(Lit(true), Nil()),
	)
	f.Line()

	f.Comment("decodeLenientItems decodes an array property that has both hints: a value")
	f.Comment("that is not an array is not an error either.")
	f.Func().Id("decodeLenientItems").Types(Id("T").Any()).Params(rawParam(), Id("dst").Op("*").Index().Id("T"), checkParam()).Bool().Block(
		List(Id("decoded"), Id("_")).Op(":=").Id("decodeItems").Call(Id("raw"), Id("dst"), Id("check").Op("...")),
		Return(Id("decoded")),
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

	f.Comment("propertyError names structName and the path from it in err, which comes from")
	f.Comment("decoding the raw property prop on its own. encoding/json would name the")
	f.Comment("innermost struct.")
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

	f.Comment("rejectNullItems returns an error when raw is an array with a null item or")
	f.Comment("an object with a null member value. encoding/json reads one as the zero value")
	f.Comment("of T, the type of the items or values, and the schema does not allow it there.")
	f.Func().Id("rejectNullItems").Types(Id("T").Any()).Params(rawParam()).Error().Block(
		Id("dec").Op(":=").Qual("encoding/json", "NewDecoder").Call(Qual("bytes", "NewReader").Call(Id("raw"))),
		List(Id("open"), Id("_")).Op(":=").Id("dec").Dot("Token").Call(),
		For(Id("dec").Dot("More").Call()).Block(
			If(Id("open").Op("==").Qual("encoding/json", "Delim").Call(LitRune('{'))).Block(
				Id("dec").Dot("Token").Call(),
			),
			Var().Id("item").Qual("encoding/json", "RawMessage"),
			If(Id("dec").Dot("Decode").Call(Op("&").Id("item")).Op("==").Nil().Op("&&").Id("string").Call(Id("item")).Op("==").Lit("null")).Block(
				Return(Op("&").Qual("encoding/json", "UnmarshalTypeError").Values(Dict{
					Id("Value"): Lit("null"),
					Id("Type"):  Qual("reflect", "TypeOf").Call(Parens(Op("*").Id("T")).Call(Nil())).Dot("Elem").Call(),
				})),
			),
		),
		Return(Nil()),
	)
	f.Line()

	f.Comment("maybeAbsent reports whether *v holds what a required member that the JSON")
	f.Comment("object lacks, or that is null, leaves in a fresh value: the zero value of T,")
	f.Comment("or, for raw JSON, which keeps a null as the bytes null, also null.")
	f.Func().Id("maybeAbsent").Types(Id("T").Any()).Params(Id("v").Op("*").Id("T")).Bool().Block(
		If(List(Id("raw"), Id("ok")).Op(":=").Id("any").Call(Id("v")).Assert(Op("*").Qual("encoding/json", "RawMessage")), Id("ok")).Block(
			Return(Id("len").Call(Op("*").Id("raw")).Op("==").Lit(0).Op("||").Id("string").Call(Op("*").Id("raw")).Op("==").Lit("null")),
		),
		Return(Qual("reflect", "ValueOf").Call(Id("v")).Dot("Elem").Call().Dot("IsZero").Call()),
	)
	f.Line()

	f.Comment("jsonMembers returns the members of the JSON object b, and nil when b is null.")
	f.Func().Id("jsonMembers").Params(Id("b").Index().Byte()).Map(String()).Qual("encoding/json", "RawMessage").Block(
		Var().Id("members").Map(String()).Qual("encoding/json", "RawMessage"),
		Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("members")),
		Return(Id("members")),
	)
	f.Line()

	f.Comment("requireMember returns an error when members has no member prop, or when it")
	f.Comment("is null and takesNull is false. A key matches prop in any case, as it matches")
	f.Comment("a struct field in encoding/json. dst is the field, whose type the error names.")
	f.Func().Id("requireMember").Types(Id("T").Any()).Params(
		Id("members").Map(String()).Qual("encoding/json", "RawMessage"), Id("dst").Op("*").Id("T"), Id("prop").String(), Id("takesNull").Bool(),
	).Error().Block(
		List(Id("value"), Id("ok")).Op(":=").Id("members").Index(Id("prop")),
		For(List(Id("key"), Id("v")).Op(":=").Range().Id("members")).Block(
			If(Op("!").Id("ok").Op("&&").Qual("strings", "EqualFold").Call(Id("key"), Id("prop"))).Block(
				List(Id("value"), Id("ok")).Op("=").List(Id("v"), Lit(true)),
			),
		),
		Id("typ").Op(":=").Qual("reflect", "TypeOf").Call(Id("dst")).Dot("Elem").Call(),
		Switch().Block(
			Case(Op("!").Id("ok")).Block(
				Return(Op("&").Qual("encoding/json", "UnmarshalTypeError").Values(Dict{Id("Value"): Lit("missing member"), Id("Type"): Id("typ")})),
			),
			Case(Op("!").Id("takesNull").Op("&&").Id("string").Call(Id("value")).Op("==").Lit("null")).Block(
				Return(Op("&").Qual("encoding/json", "UnmarshalTypeError").Values(Dict{Id("Value"): Lit("null"), Id("Type"): Id("typ")})),
			),
		),
		Return(Nil()),
	)
	f.Line()

	if b.usesPatch {
		f.Comment("decodePatch decodes a patch field that has x-deserialize-default-on-error.")
		f.Comment("An absent field leaves dst alone, null clears it, and a value that is not a")
		f.Comment("valid T, or that does not fit the shape check, leaves it unset.")
		f.Func().Id("decodePatch").Types(Id("T").Any()).Params(rawParam(), Id("dst").Op("*").Id("Patch").Types(Id("T")), checkParam()).Block(
			Switch().Block(
				Case(Id("raw").Op("==").Nil()),
				Case(Id("string").Call(Id("raw")).Op("==").Lit("null")).Block(
					Op("*").Id("dst").Op("=").Id("PatchNull").Types(Id("T")).Call(),
				),
				Default().Block(
					Var().Id("v").Id("T"),
					Op("*").Id("dst").Op("=").Id("Patch").Types(Id("T")).Values(),
					If(Id("decodeLenient").Call(Id("raw"), Op("&").Id("v"), Id("check").Op("..."))).Block(
						Op("*").Id("dst").Op("=").Id("PatchSet").Call(Id("v")),
					),
				),
			),
		)
		f.Line()

		f.Comment("decodePatchItems decodes a patch field that has both hints: it also drops")
		f.Comment("the items that are null, that are not a valid T, or that do not fit the shape")
		f.Comment("check.")
		f.Func().Id("decodePatchItems").Types(Id("T").Any()).Params(rawParam(), Id("dst").Op("*").Id("Patch").Types(Index().Id("T")), checkParam()).Block(
			Switch().Block(
				Case(Id("raw").Op("==").Nil()),
				Case(Id("string").Call(Id("raw")).Op("==").Lit("null")).Block(
					Op("*").Id("dst").Op("=").Id("PatchNull").Types(Index().Id("T")).Call(),
				),
				Default().Block(
					Var().Id("v").Index().Id("T"),
					Op("*").Id("dst").Op("=").Id("Patch").Types(Index().Id("T")).Values(),
					If(Id("decodeLenientItems").Call(Id("raw"), Op("&").Id("v"), Id("check").Op("..."))).Block(
						Op("*").Id("dst").Op("=").Id("PatchSet").Call(Id("v")),
					),
				),
			),
		)
		f.Line()
	}
	if b.usesAbsentIfNull {
		f.Comment("absentIfNull returns nil for a raw null, and raw otherwise, so that a patch")
		f.Comment("field for which the schema defines no clear reads null as absent, as ACP v1")
		f.Comment("reads it.")
		f.Func().Id("absentIfNull").Params(rawParam()).Qual("encoding/json", "RawMessage").Block(
			If(Id("string").Call(Id("raw")).Op("==").Lit("null")).Block(Return(Nil())),
			Return(Id("raw")),
		)
		f.Line()
	}

	if !b.usesAdditionalProperties {
		return
	}
	f.Comment("additionalProperties returns the members of the JSON object b that named")
	f.Comment("does not list, or nil when there are none. A key matches a name in any case,")
	f.Comment("as it matches a struct field in encoding/json.")
	f.Func().Id("additionalProperties").Params(Id("b").Index().Byte(), Id("named").Op("...").String()).Map(String()).Qual("encoding/json", "RawMessage").Block(
		Var().Id("members").Map(String()).Qual("encoding/json", "RawMessage"),
		Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("members")),
		For(Id("key").Op(":=").Range().Id("members")).Block(
			For(List(Id("_"), Id("name")).Op(":=").Range().Id("named")).Block(
				If(Qual("strings", "EqualFold").Call(Id("key"), Id("name"))).Block(Id("delete").Call(Id("members"), Id("key"))),
			),
		),
		If(Id("len").Call(Id("members")).Op("==").Lit(0)).Block(Return(Nil())),
		Return(Id("members")),
	)
	f.Line()

	f.Comment("withAdditionalProperties adds the members of additional to the JSON object")
	f.Comment("named. A member that named already has keeps its value.")
	f.Func().Id("withAdditionalProperties").Params(Id("named").Index().Byte(), Id("additional").Map(String()).Qual("encoding/json", "RawMessage")).Params(Index().Byte(), Error()).Block(
		If(Id("len").Call(Id("additional")).Op("==").Lit(0)).Block(Return(Id("named"), Nil())),
		Id("members").Op(":=").Qual("maps", "Clone").Call(Id("additional")),
		If(Id("err").Op(":=").Qual("encoding/json", "Unmarshal").Call(Id("named"), Op("&").Id("members")), Id("err").Op("!=").Nil()).Block(Return(Nil(), Id("err"))),
		Return(Qual("encoding/json", "Marshal").Call(Id("members"))),
	)
	f.Line()
}

func enumValues(def *load.Definition) []string {
	values := []string{}
	for _, v := range def.Enum {
		values = append(values, fmt.Sprint(v))
	}
	if len(values) == 0 {
		for _, v := range def.OneOf {
			values = append(values, fmt.Sprint(v.Const))
		}
	}
	return values
}
