package emit

import (
	"fmt"
	"go/token"
	"strings"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// The fast decoders. Every generated named type gets a decodeFast, and each
// UnmarshalJSON runs it first (tryFastStmt), so that a fast decoder reaches
// the fast decoders of the types it holds and never decodes a value twice.
// The contract that each one keeps, and the runtime helpers that they call,
// are in decode_fast.go of the acp package: when decodeFast returns true, the
// exact decoder, the UnmarshalJSON that the other emitters write or
// encoding/json, would decode the input to the identical value. Each emitter
// here follows the emitter of the exact decoder that it mirrors:
// emitFastFields follows emitDecodeFields, fastDecodeStmt follows
// decodeStmt, and emitFastUnion the UnmarshalJSON of emitUnion.

// tryFastStmt is the first statement of a generated UnmarshalJSON: it
// returns when the fast decoder of recv decoded b.
func tryFastStmt(recv string) Code {
	return If(Id("tryFast").Call(Id(recv), Id("b"))).Block(Return(Nil()))
}

// fastFunc starts the declaration of the decodeFast method of name.
func (b *typesBuilder) fastFunc(recv, name string) *Statement {
	b.fastTypes = append(b.fastTypes, name)
	return b.fast.Func().Params(Id(recv).Op("*").Id(name)).Id("decodeFast").Params(Id("b").Index().Byte()).Bool()
}

func decline() Code { return Return(Lit(false)) }

// goType is the Go type of a field or a variant as the fast decoders read it.
type goType struct {
	kind goKind
	// name is the name of a builtin, named, or raw type.
	name string
	// elem is the element of a pointer, slice, map, or Patch.
	elem *goType
}

type goKind uint8

const (
	goBuiltin goKind = iota // a type of leafHelpers
	goRaw                   // json.RawMessage, or an alias of it
	goNamed                 // a generated type, or Meta: it has a decodeFast
	goPointer
	goSlice
	goMap // with string keys
	goPatch
	goOther // a type without a fast decoder, which fastExact decodes
)

// leafHelpers are the fast decoders of the builtin types, the integer types
// of formatTypes among them. A builtin without one decodes with fastExact.
var leafHelpers = map[string]string{
	"string": "fastString", "float64": "fastFloat", "bool": "fastBool",
	"int": "fastInt", "int32": "fastInt", "int64": "fastInt",
	"uint16": "fastUint", "uint32": "fastUint", "uint64": "fastUint",
}

// fastType reads the Go type that typ, the code of a type, spells.
func (b *typesBuilder) fastType(typ Code) goType {
	return b.parseGoType(fmt.Sprintf("%#v", typ))
}

func (b *typesBuilder) parseGoType(s string) goType {
	elem := func(s string) *goType {
		t := b.parseGoType(s)
		return &t
	}
	switch {
	case strings.HasPrefix(s, "*"):
		return goType{kind: goPointer, elem: elem(s[1:])}
	case strings.HasPrefix(s, "[]"):
		return goType{kind: goSlice, elem: elem(s[2:])}
	case strings.HasPrefix(s, "map[string]"):
		return goType{kind: goMap, elem: elem(strings.TrimPrefix(s, "map[string]"))}
	case strings.HasPrefix(s, "Patch[") && strings.HasSuffix(s, "]"):
		return goType{kind: goPatch, elem: elem(s[len("Patch[") : len(s)-1])}
	case s == "json.RawMessage" || b.rawAliases[s]:
		return goType{kind: goRaw, name: s}
	case leafHelpers[s] != "":
		return goType{kind: goBuiltin, name: s}
	case token.IsIdentifier(s) && token.IsExported(s):
		return goType{kind: goNamed, name: s}
	}
	return goType{kind: goOther, name: s}
}

// code returns the code of t.
func (t goType) code() Code {
	switch t.kind {
	case goPointer:
		return Op("*").Add(t.elem.code())
	case goSlice:
		return Index().Add(t.elem.code())
	case goMap:
		return Map(String()).Add(t.elem.code())
	case goPatch:
		return Id("Patch").Types(t.elem.code())
	case goRaw:
		if t.name == "json.RawMessage" {
			return rawJSON()
		}
	}
	return Id(t.name)
}

// decoder returns the fast decoder of t, a func(*T, []byte) bool.
func (t goType) decoder() Code {
	switch t.kind {
	case goBuiltin:
		return Id(leafHelpers[t.name]).Types(Id(t.name))
	case goRaw:
		return Id("fastRaw")
	case goNamed:
		return Parens(Op("*").Id(t.name)).Dot("decodeFast")
	case goOther:
		return Id("fastExact").Types(t.code())
	}
	// A function literal that captures nothing allocates nothing.
	return Func().Params(Id("d").Op("*").Add(t.code()), Id("b").Index().Byte()).Bool().Block(Return(t.callPtr(Id("d"), Id("b"))))
}

// callPtr returns the call that decodes raw into dst, a pointer to a zero T.
func (t goType) callPtr(dst, raw Code) Code {
	switch t.kind {
	case goBuiltin:
		return Id(leafHelpers[t.name]).Call(dst, raw)
	case goRaw:
		return Id("fastRaw").Call(dst, raw)
	case goNamed:
		return Parens(Op("*").Id(t.name)).Dot("decodeFast").Call(dst, raw)
	case goPointer:
		return Id("fastPtr").Call(dst, raw, t.elem.decoder())
	case goSlice:
		return Id("fastSlice").Call(dst, raw, t.elem.decoder())
	case goMap:
		return Id("fastMap").Call(dst, raw, t.elem.decoder())
	case goPatch:
		return Id("fastPatch").Call(dst, raw, t.elem.decoder())
	}
	return Id("fastExact").Call(dst, raw)
}

// callField returns the call that decodes raw into field, a zero field.
func (t goType) callField(field func() *Statement, raw Code) Code {
	if t.kind == goNamed {
		return field().Dot("decodeFast").Call(raw)
	}
	return t.callPtr(Op("&").Add(field()), raw)
}

// emitFastStruct declares decodeFast for the struct name: it mirrors the
// UnmarshalJSON that emitStruct declares, or encoding/json for a struct
// without one, which has no custom-decoded or required field to mirror.
func (b *typesBuilder) emitFastStruct(name string, plans []fieldPlan, nestedTypeNames map[string]string, open bool) {
	b.fastFunc("v", name).BlockFunc(func(g *Group) {
		if !b.emitFastFields(g, "v", plans, nestedTypeNames, open) {
			g.Comment("The names of its fields fold onto each other, or are not plain ASCII.")
			g.Add(decline())
			return
		}
		g.Return(Lit(true))
	})
	b.fast.Line()
}

// emitFastFields reads the JSON object b into recv, whose fields plans
// describe, as emitDecodeFields does, and keeps the members that no field
// names in its AdditionalProperties when open. It reads each member once: a
// field that the exact decoder decodes in place decodes from the member's
// value at once, and the others after the object, from the value that raw
// keeps, in the order of the exact decoder. It returns false, having emitted
// nothing, for fields whose names it cannot match.
func (b *typesBuilder) emitFastFields(g *Group, recv string, plans []fieldPlan, nestedTypeNames map[string]string, open bool) bool {
	var spans []Code
	folded := map[string]bool{}
	for _, p := range plans {
		// The walk switches on the key in ASCII lower case (fastObject).
		key := strings.ToLower(p.field.Prop)
		if !plainName(p.field.Prop) || folded[key] || len(key) > fastKeyLen {
			return false
		}
		folded[key] = true
		spans = append(spans, Id(p.field.Name).Index().Byte())
	}
	span := func(p fieldPlan) *Statement { return Id("raw").Dot(p.field.Name) }
	if len(plans) > 0 {
		g.Comment("raw holds the value of each member, nil while it is absent.")
		g.Var().Id("raw").Struct(spans...)
	}
	g.Var().Id("w").Id("fastObject")
	g.If(Op("!").Id("w").Dot("start").Call(Id("b"))).Block(decline())
	other := Id("w").Dot("skip").Call()
	if open {
		other = Id("w").Dot("keep").Call(Op("&").Id(recv).Dot(additionalPropertiesField))
	}
	g.For(Id("w").Dot("next").Call()).Block(Switch(String().Call(Id("w").Dot("folded").Call())).BlockFunc(func(sw *Group) {
		for _, p := range plans {
			// The key must match the name exactly, and once declines a key that
			// repeats. (A name as an argument would be lost to stablecheck,
			// which drops the added members' names from calls.)
			read := Id("string").Call(Id("w").Dot("key")).Op("!=").Lit(p.field.Prop).Op("||").Op("!").Id("w").Dot("once").Call(Op("&").Add(span(p)))
			if !p.customDecode() {
				field := func() *Statement { return Id(recv).Dot(p.field.Name) }
				read = read.Op("||").Op("!").Add(b.fastType(declType(p, nestedTypeNames, false)).callField(field, span(p)))
			}
			sw.Case(Lit(strings.ToLower(p.field.Prop))).Block(If(read).Block(decline()))
		}
		sw.Default().Block(If(Op("!").Add(other)).Block(decline()))
	}))
	g.If(Op("!").Id("w").Dot("done").Call()).Block(decline())
	for _, p := range customDecoded(plans) {
		if p.nonNullItem != nil {
			g.If(Id("hasNullItem").Call(span(p))).Block(decline())
		}
		g.Add(b.fastDecodeStmt(p, recv, nestedTypeNames))
	}
	emitFastRequire(g, recv, plans)
	return true
}

// fastKeyLen is the longest key that fastObject folds (fastObject.lower).
const fastKeyLen = 32

// plainName reports whether the JSON name prop is plain ASCII, which a key
// spells without an escape and encoding/json folds in ASCII case only.
func plainName(prop string) bool {
	for _, c := range prop {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '$' || c == '-' || c == '.') {
			return false
		}
	}
	// A tag named "-" leaves the field out.
	return prop != "" && prop != "-"
}

// emitFastRequire declines where emitRequireMembers fails: a required member
// is absent, or null where it takes no null, and a required field is zero.
func emitFastRequire(g *Group, recv string, plans []fieldPlan) {
	var present, anyAbsent *Statement
	for _, p := range plans {
		if !p.required {
			continue
		}
		has := Id("raw").Dot(p.field.Name).Op("!=").Nil()
		if !p.takesNull {
			has = has.Op("&&").String().Call(Id("raw").Dot(p.field.Name)).Op("!=").Lit("null")
		}
		absent := Id("maybeAbsent").Call(Op("&").Id(recv).Dot(p.field.Name))
		if present == nil {
			present, anyAbsent = has, absent
			continue
		}
		present = present.Op("&&").Add(has)
		anyAbsent = anyAbsent.Op("||").Add(absent)
	}
	if present != nil {
		g.If(Op("!").Parens(present).Op("&&").Parens(anyAbsent)).Block(decline())
	}
}

// fastDecodeStmt decodes the custom-decoded field of p from its member's
// value as decodeStmt does, with the twin of each helper that decodeStmt
// calls (decode_fast.go). It declines where decodeStmt fails.
func (b *typesBuilder) fastDecodeStmt(p fieldPlan, recv string, nestedTypeNames map[string]string) Code {
	raw := func() *Statement { return Id("raw").Dot(p.field.Name) }
	dst := func() *Statement { return Op("&").Id(recv).Dot(p.field.Name) }
	typ := b.fastType(declType(p, nestedTypeNames, false))
	needsFallback := func(notDecoded *Statement) *Statement {
		if !p.fallbackOnAbsent {
			notDecoded = notDecoded.Op("&&").Add(raw().Op("!=").Nil())
		}
		if !p.fallbackOnNull {
			notDecoded = notDecoded.Op("&&").Id("string").Call(raw()).Op("!=").Lit("null")
		}
		return notDecoded
	}
	fallback := Id("setDefault").Call(dst(), Lit(p.fallbackJSON))
	lenient, items := p.field.Lenient.DefaultOnError, p.field.Lenient.SkipInvalidItems

	if p.patch && !lenient {
		field := func() *Statement { return Id(recv).Dot(p.field.Name) }
		present := raw().Op("!=").Nil()
		if p.nullAsAbsent {
			// As decodeStmt does, null reads as absent.
			present = present.Op("&&").Id("string").Call(raw()).Op("!=").Lit("null")
		}
		return If(present.Op("&&").Op("!").Add(typ.callField(field, raw()))).Block(decline())
	}
	// value is the type that the helper decodes: the type of the field, of
	// its Patch's value, or of the items of either.
	value := typ
	if p.patch {
		value = *typ.elem
	}
	if items {
		if value.kind != goSlice {
			panic(fmt.Sprintf("field %q has x-deserialize-skip-invalid-items and a Go type that is no slice", p.field.Prop))
		}
		value = *value.elem
	}
	args := []Code{raw(), dst(), value.decoder()}
	if p.shape != nil {
		args = append(args, p.shape)
	}
	pick := func(single, multi string) *Statement {
		if items {
			return Id(multi)
		}
		return Id(single)
	}

	if p.patch {
		if p.nullAsAbsent {
			args[0] = Id("absentIfNull").Call(raw())
		}
		return pick("fastPatchLenient", "fastPatchLenientItems").Call(args...)
	}
	if lenient {
		decoded := pick("fastLenient", "fastLenientItems").Call(args...)
		if !p.hasFallback() {
			return decoded
		}
		return If(needsFallback(Op("!").Add(decoded))).Block(fallback)
	}
	call := pick("fastField", "fastItems").Call(args...)
	if !p.fallbackOnAbsent && !p.fallbackOnNull {
		return If(List(Id("_"), Id("ok")).Op(":=").Add(call), Op("!").Id("ok")).Block(decline())
	}
	return If(List(Id("decoded"), Id("ok")).Op(":=").Add(call), Op("!").Id("ok")).Block(decline()).
		Else().If(needsFallback(Op("!").Id("decoded"))).Block(fallback)
}

// emitFastNamed declares decodeFast for name, a defined type of underlying
// with no method of its own but, with nonNull, an UnmarshalJSON that rejects
// null items (rejectNullItems): encoding/json decodes it as underlying.
func (b *typesBuilder) emitFastNamed(name string, underlying Code, nonNull bool) {
	t := b.fastType(underlying)
	b.fastFunc("v", name).BlockFunc(func(g *Group) {
		if nonNull {
			g.If(Id("hasNullItem").Call(Id("b"))).Block(decline())
		}
		if t.kind == goNamed {
			// encoding/json decodes it as the struct or union underneath,
			// without the UnmarshalJSON of that type: no fast decoder
			// mirrors that.
			g.Add(decline())
			return
		}
		g.Return(t.callPtr(Id("v"), Id("b")))
	})
	b.fast.Line()
}

// emitFastEmpty declares decodeFast for name, a struct without fields.
func (b *typesBuilder) emitFastEmpty(name string) {
	b.fastFunc("v", name).Block(Return(Id("fastEmpty").Call(Id("b"))))
	b.fast.Line()
}

// emitFastEnum declares decodeFast for the enum name with the constants
// members, which reads a string that it does not list as other, as its
// UnmarshalJSON does (emitEnum).
func (b *typesBuilder) emitFastEnum(name string, members []Code, other string) {
	b.fastFunc("v", name).Block(
		If(Id("b").Index(Lit(0)).Op("==").LitRune('n')).Block(Return(Lit(true))),
		Var().Id("s").String(),
		If(Op("!").Id("fastString").Call(Op("&").Id("s"), Id("b"))).Block(decline()),
		Switch(Id("member").Op(":=").Id(name).Call(Id("s")), Id("member")).Block(
			Case(members...).Block(Op("*").Id("v").Op("=").Id("member")),
			Default().Block(Op("*").Id("v").Op("=").Id(other)),
		),
		Return(Lit(true)),
	)
	b.fast.Line()
}

// fastUnion is what the fast decoder of a union needs from emitUnion, which
// fills it from the predicates with which its exact decoder selects a
// variant, so that both select alike.
type fastUnion struct {
	name    string
	discKey string
	// discDefaultOnError says that a discriminator that is neither a string
	// nor null reads as absent.
	discDefaultOnError bool
	// knownTags are the discriminator values of the variants that have one.
	knownTags []string
	variants  []fastVariant
	// wrapper are the union's own fields, and open says that it keeps the
	// members that no part of it names, named lists the others.
	wrapper []fieldPlan
	open    bool
	named   []Code
}

// fastVariant is a variant as the exact decoder of its union selects it, in
// the order of its tries: the null variant on null, the switch on the
// discriminator (discValue), the variants that an object selects by its keys
// (byKeys), those that an array selects by the keys of its items
// (byItemKeys), and then the others in order (ordered).
type fastVariant struct {
	fieldName, typeName string
	isNull              bool
	discValue           string
	byKeys              bool
	required            []fastKey
	refusesKnownTag     bool
	constPairs          [][2]string
	nullKeys            []string
	byItemKeys          bool
	itemRequired        []fastKey
	ordered             bool
	// values are the values that a value variant takes, when not every
	// value of its type (unionAlternative.values).
	values []Code
}

// fastKey is a key that a variant requires (requireKey in emitUnion).
type fastKey struct {
	key string
	// takesNull says that a null value counts as present, and openDisc that
	// the key is the discriminator of a catch-all, which must be a string.
	takesNull, openDisc bool
}

// keyTakesNull says whether a required key of a union variant counts when it
// is null: its schema def accepts null, or a hint reads null as the default.
// It mirrors requireKey in emitUnion.
func keyTakesNull(schema *load.Schema, def *load.Definition) bool {
	return acceptsNull(schema, def) || def.DefaultOnNull || def.DefaultOnError
}

// emitFastUnion declares decodeFast for the union u. It tries the variants in
// the order and with the checks of the exact decoder, on the members that
// readUnionObject reads for both, and decodes the variant it tries with
// fastOrExact, whose answer is exact: so a variant that does not decode
// passes the turn to the next, as in the exact decoder.
func (b *typesBuilder) emitFastUnion(u fastUnion) {
	// try decodes the variant v, and with values checks that it is one of
	// v.values, as the exact decoder does in its ordered tries alone.
	try := func(v fastVariant, values bool) []Code {
		decodes := Id("fastOrExact").Call(Op("&").Id("v"), Id("b"), b.fastType(Id(v.typeName)).decoder())
		if values && len(v.values) > 0 {
			decodes = decodes.Op("&&").Add(takesValue(v.values))
		}
		return []Code{
			Var().Id("v").Id(v.typeName),
			If(decodes).Block(Id("u").Dot(v.fieldName).Op("=").Op("&").Id("v"), Return(Lit(true))),
		}
	}
	noMatch := Id("match").Op("=").Lit(false)
	// value looks up the member key of obj, a unionObject, into raw.
	value := func(obj, key string) Code {
		return List(Id("raw"), Id("_")).Op(":=").Id(obj).Dot("get").Call(Lit(key))
	}
	requireKey := func(obj string, k fastKey) Code {
		switch {
		case k.openDisc:
			return If(value(obj, k.key), Op("!").Id("isJSONString").Call(Id("raw"))).Block(noMatch)
		case k.takesNull:
			return If(List(Id("_"), Id("ok")).Op(":=").Id(obj).Dot("get").Call(Lit(k.key)), Op("!").Id("ok")).Block(noMatch)
		}
		return If(value(obj, k.key), Id("absentOrNull").Call(Id("raw"))).Block(noMatch)
	}
	var tags []Code
	for _, tag := range u.knownTags {
		tags = append(tags, Lit(tag))
	}
	some := func(f func(fastVariant) bool) bool {
		for _, v := range u.variants {
			if f(v) {
				return true
			}
		}
		return false
	}
	readsObject := len(u.wrapper) > 0 || u.open || u.discKey != "" || some(func(v fastVariant) bool { return v.byKeys })

	b.fastFunc("u", u.name).BlockFunc(func(g *Group) {
		g.Comment("null selects the null variant and no other.")
		g.If(Id("b").Index(Lit(0)).Op("==").LitRune('n')).BlockFunc(func(null *Group) {
			for _, v := range u.variants {
				if v.isNull {
					null.Id("u").Dot(v.fieldName).Op("=").Op("&").Id(v.typeName).Values()
					null.Return(Lit(true))
					return
				}
			}
			null.Add(decline())
		})
		if readsObject {
			g.List(Id("m"), Id("err")).Op(":=").Id("readUnionObject").Call(Id("b"))
			g.If(Id("err").Op("!=").Nil()).Block(decline())
			g.If(Id("m").Dot("isObject")).BlockFunc(func(obj *Group) {
				if len(u.wrapper) > 0 {
					obj.BlockFunc(func(own *Group) {
						if !b.emitFastFields(own, "u", u.wrapper, nil, false) {
							own.Add(decline())
						}
					})
				}
				if u.open {
					// As the exact decoder does: it is rare.
					obj.Id("u").Dot(additionalPropertiesField).Op("=").Id("additionalProperties").Call(append([]Code{Id("b")}, u.named...)...)
				}
				if u.discDefaultOnError {
					obj.If(
						value("m", u.discKey),
						Op("!").Id("absentOrNull").Call(Id("raw")).Op("&&").Op("!").Id("isJSONString").Call(Id("raw")),
					).Block(Id("m").Dot("delete").Call(Lit(u.discKey)))
				}
				if u.discKey != "" {
					// A short string that does not escape needs no allocation.
					obj.Id("disc").Op(":=").String().Call(Id("m").Dot("tag").Call(Lit(u.discKey)))
					obj.Switch(Id("disc")).BlockFunc(func(sw *Group) {
						for _, v := range u.variants {
							if v.discValue != "" {
								sw.Case(Lit(v.discValue)).Block(try(v, false)...)
							}
						}
					})
				}
				for _, v := range u.variants {
					if !v.byKeys {
						continue
					}
					obj.BlockFunc(func(h *Group) {
						h.Id("match").Op(":=").Lit(true)
						for _, k := range v.required {
							h.Add(requireKey("m", k))
						}
						if v.refusesKnownTag {
							h.Switch(Id("disc")).Block(Case(tags...).Block(noMatch))
						}
						for _, pair := range v.constPairs {
							h.If(
								List(Id("raw"), Id("ok")).Op(":=").Id("m").Dot("get").Call(Lit(pair[0])),
								Id("ok").Op("&&").Op("!").Id("isText").Call(Id("raw"), Lit(pair[1])),
							).Block(noMatch)
						}
						for _, key := range v.nullKeys {
							h.If(value("m", key), Op("!").Id("absentOrNull").Call(Id("raw"))).Block(noMatch)
						}
						h.If(Id("match")).Block(try(v, false)...)
					})
				}
			})
		}
		if some(func(v fastVariant) bool { return v.byItemKeys }) {
			g.If(Id("b").Index(Lit(0)).Op("==").LitRune('[').Op("&&").Id("itemsAreObjects").Call(Id("b"))).BlockFunc(func(arr *Group) {
				for _, v := range u.variants {
					if !v.byItemKeys {
						continue
					}
					arr.BlockFunc(func(h *Group) {
						h.Id("match").Op(":=").Lit(true)
						h.List(Id("it"), Id("_")).Op(":=").Id("arrayItems").Call(Id("b"))
						h.For(Id("it").Dot("next").Call()).BlockFunc(func(loop *Group) {
							loop.List(Id("elem"), Id("err")).Op(":=").Id("readUnionObject").Call(Id("it").Dot("item"))
							loop.If(Id("err").Op("!=").Nil()).Block(decline())
							for _, k := range v.itemRequired {
								loop.Add(requireKey("elem", k))
							}
						})
						h.If(Id("match")).Block(try(v, false)...)
					})
				}
			})
		}
		for _, v := range u.variants {
			if v.ordered {
				g.Block(try(v, true)...)
			}
		}
		g.Add(decline())
	})
	b.fast.Line()
}
