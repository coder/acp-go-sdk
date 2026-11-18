package emit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/ir"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/util"
)

// splitCamelCase splits a CamelCase string into words.
// Example: "RequestPermissionRequest" -> ["Request", "Permission", "Request"]
func splitCamelCase(s string) []string {
	if s == "" {
		return nil
	}
	var words []string
	var current strings.Builder

	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			// Start new word on uppercase letter
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

// generateNestedTypeName applies multiple heuristics to create idiomatic nested type names
// while guaranteeing stability (name depends only on parent, field, and collision check).
//
// If a collision is detected (e.g. the schema already defines a type with the same name we
// would otherwise generate), fall back to a deterministic suffixed name. Panics only if it
// cannot find a unique name (which would indicate a codegen bug).
func generateNestedTypeName(parentName, rawFieldName string, usedNames map[string]bool) string {
	// Heuristic 1: Ensure field name is properly capitalized first
	fieldName := util.ToExportedField(rawFieldName)

	// Heuristic 2: Try RPC suffix stripping for Request/Response/Notification parents
	// This handles cases like RequestPermissionRequest + ToolCall → RequestPermissionToolCall
	for _, suffix := range []string{"Request", "Response", "Notification"} {
		if strings.HasSuffix(parentName, suffix) {
			baseName := strings.TrimSuffix(parentName, suffix)
			if len(baseName) > 0 {
				candidate := baseName + fieldName
				if !usedNames[candidate] {
					return candidate
				}
			}
		}
	}

	// Heuristic 3: Try word-boundary deduplication
	// This handles cases like AgentNotification + ExtNotification → AgentExtNotification
	parentWords := splitCamelCase(parentName)
	fieldWords := splitCamelCase(fieldName)

	// Need at least 2 words in parent to consider deduplication
	if len(parentWords) >= 2 && len(fieldWords) > 0 {
		lastParentWord := parentWords[len(parentWords)-1]
		firstFieldWord := fieldWords[0]
		lastFieldWord := fieldWords[len(fieldWords)-1]

		if strings.EqualFold(lastParentWord, firstFieldWord) || strings.EqualFold(lastParentWord, lastFieldWord) {
			// Remove last word from parent, then concatenate
			parentWithoutLast := strings.Join(parentWords[:len(parentWords)-1], "")
			if len(parentWithoutLast) > 0 {
				candidate := parentWithoutLast + fieldName
				if !usedNames[candidate] {
					return candidate
				}
			}
		}
	}

	// Heuristic 4: Fall back to full concatenation
	fullName := parentName + fieldName
	if !usedNames[fullName] {
		return fullName
	}

	// Collision: this can happen when the schema already defines a top-level type with the
	// same name as an inline property we'd otherwise generate.
	base := fullName + "Inline"
	if !usedNames[base] {
		return base
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s%d", base, i)
		if !usedNames[candidate] {
			return candidate
		}
	}

	// DEFENSIVE PROGRAMMING: If we get here, something is deeply wrong with name tracking.
	panic(fmt.Sprintf("unable to generate unique nested type name (parent: %q, field: %q)", parentName, rawFieldName))
}

// emitDocComment emits a multi-line doc comment for the given description.
// Properly handles newlines and paragraph breaks from JSON schema descriptions.
func emitDocComment(f *File, desc string) {
	lines := util.FormatDocComment(desc)
	for _, line := range lines {
		f.Comment(line)
	}
}

// appendDocComments appends doc comment Code elements to a slice for struct field comments.
// Used when building struct field definitions with proper multi-line comment support.
// constDocs maps the const of each alternative in alts to its description,
// the doc comment of its Go constant.
func constDocs(alts []*load.Definition) map[string]string {
	docs := map[string]string{}
	for _, alt := range alts {
		if alt != nil && alt.Const != nil {
			docs[fmt.Sprint(alt.Const)] = alt.Description
		}
	}
	return docs
}

func appendDocComments(codeSlice []Code, desc string) []Code {
	lines := util.FormatDocComment(desc)
	for _, line := range lines {
		codeSlice = append(codeSlice, Comment(line))
	}
	return codeSlice
}

// WriteTypesJen emits go/types_gen.go with all types and the Agent/Client interfaces.
func WriteTypesJen(outDir string, schema *load.Schema, meta *load.Meta) error {
	f, _ := buildTypesJen(schema, meta)
	var buf bytes.Buffer
	if err := f.Render(&buf); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "types_gen.go"), buf.Bytes(), 0o644)
}

type typesBuilder struct {
	f                        *File
	schema                   *load.Schema
	usedTypeNames            map[string]bool
	variants                 map[string][]unionVariant
	shapes                   *shapeTable
	usesDecodeHelpers        bool
	usesAdditionalProperties bool
	// patch holds the patch fields of the schema (ir.PatchFields), and
	// nullAsAbsent those of them that have no clear (ir.NullAsAbsent).
	patch, nullAsAbsent map[*load.Definition]bool
	usesPatch           bool
}

// buildTypesJen builds the types_gen.go file. It also returns, per oneOf union,
// the variants that emitUnion emitted, with the names it chose, so that
// WriteHelpersJen builds constructors for exactly those types.
func buildTypesJen(schema *load.Schema, meta *load.Meta) (*File, map[string][]unionVariant) {
	f := NewFile("acp")
	f.HeaderComment("Code generated by acp-go-generator; DO NOT EDIT.")
	emitted := map[string][]unionVariant{}

	// Track all type names to avoid collisions when generating nested types
	usedTypeNames := make(map[string]bool)
	for k := range schema.Defs {
		usedTypeNames[k] = true
	}
	b := &typesBuilder{
		f: f, schema: schema, usedTypeNames: usedTypeNames, variants: emitted,
		shapes: newShapeTable(schema), patch: ir.PatchFields(schema), nullAsAbsent: ir.NullAsAbsent(schema),
	}

	// Deterministic order
	keys := make([]string, 0, len(schema.Defs))
	for k := range schema.Defs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, name := range keys {
		def := schema.Defs[name]
		if def == nil {
			continue
		}

		// DEFENSIVE PROGRAMMING: Ensure we're not about to emit a type that conflicts
		// with a previously generated nested type. This should never happen since we
		// pre-populate usedTypeNames with all schema definitions.
		if usedTypeNames[name] && name != "" {
			// This is expected for schema-defined types, but verify it's in the schema
			if _, inSchema := schema.Defs[name]; !inSchema {
				panic(fmt.Sprintf("BUG: attempting to emit type %q that was already generated as a nested type", name))
			}
		}

		if def.Description != "" {
			emitDocComment(f, def.Description)
		}

		// Requests, responses, notifications, and ToolCallUpdate get Validate.
		// The generated dispatch calls it on every request and notification.
		validated := strings.HasSuffix(name, "Request") || strings.HasSuffix(name, "Response") || strings.HasSuffix(name, "Notification") || name == "ToolCallUpdate"
		// A union wrapper holds variant pointers, not the schema's properties,
		// so emitUnion writes its Validate.
		unionWrapper := false
		switch {
		case len(def.Enum) > 0, isStringConstUnion(def):
			b.emitEnum(name, def)
		case len(def.AnyOf) > 0 && isOpenStringEnum(def):
			// "Open" string enum: `anyOf` of string consts plus (typically) a
			// free-form string catch-all. Emit as a named string type with
			// constants for the known values. Unknown values are still
			// representable as strings, matching the schema's extensibility.
			f.Type().Id(name).String()
			defs := []Code{}
			for _, v := range def.AnyOf {
				if v == nil || v.Const == nil {
					continue
				}
				s, ok := v.Const.(string)
				if !ok {
					continue
				}
				defs = appendDocComments(defs, v.Description)
				defs = append(defs, Id(util.ToEnumConst(name, s)).Id(name).Op("=").Lit(s))
			}
			if len(defs) > 0 {
				f.Const().Defs(defs...)
			}
			f.Line()
		case len(def.AnyOf) > 0:
			unionWrapper = true
			b.emitUnion(name, def, def.AnyOf, false, validated && ir.PrimaryType(def) == "object")
		case len(def.OneOf) > 0 && !isStringConstUnion(def):
			// Generic union generation for non-enum oneOf
			// Use the same implementation, but require exactly one variant
			unionWrapper = true
			b.emitUnion(name, def, def.OneOf, true, true)
		case ir.PrimaryType(def) == "object" && (len(def.Properties) > 0 || opensMembers(def)):
			fields := ir.Fields(def.Properties, def.Required)
			b.emitStruct(name, fields, b.emitNestedStructs(name, fields), opensMembers(def))
		case ir.PrimaryType(def) == "string" || ir.PrimaryType(def) == "integer" || ir.PrimaryType(def) == "number" || ir.PrimaryType(def) == "boolean":
			f.Type().Id(name).Add(primitiveJenType(def))
			f.Line()
		case ir.PrimaryType(def) == "object" && len(def.Properties) == 0:
			// Empty object shape: emit a concrete empty struct so methods can be defined
			// and the wire encoding is consistently {} rather than null.
			f.Type().Id(name).Struct()
			f.Line()
		default:
			// An alias keeps the methods of json.RawMessage, which a defined
			// type would drop.
			f.Comment(fmt.Sprintf("%s is a union or complex schema; it holds the JSON as it was read.", name))
			f.Type().Id(name).Op("=").Add(rawJSON())
			f.Line()
		}

		if validated && !unionWrapper {
			emitValidateJen(f, name, def)
		}
	}

	// Append Agent & Client interfaces from method groups
	groups := ir.BuildMethodGroups(schema, meta)
	b.emitInterfaces("agent", meta.AgentMethods, groups)
	b.emitInterfaces("client", meta.ClientMethods, groups)
	b.emitSupport()
	return f, emitted
}

// emitInterfaces declares the interface that every agent or client implements,
// Agent or Client, and then one optional interface for each method outside it.
func (b *typesBuilder) emitInterfaces(side string, methods map[string]string, groups ir.Groups) {
	iface := util.TitleWord(side)
	required := []Code{}
	optional := []*ir.MethodInfo{}
	for _, k := range ir.SortedKeys(methods) {
		mi := groups[side+"|"+methods[k]]
		if mi == nil || (mi.Notif == "" && mi.Req == "") {
			continue
		}
		if mi.Optional == "" {
			required = append(required, b.interfaceMethod(mi)...)
		} else {
			optional = append(optional, mi)
		}
	}
	b.f.Comment(fmt.Sprintf("%s is the interface that every %s implements.", iface, side))
	b.f.Comment(fmt.Sprintf("Its methods are the stable %s methods of ACP schema 1.25.0.", side))
	b.f.Comment(fmt.Sprintf("Every other %s method has its own optional interface, so a schema release never adds a method to %s.", side, iface))
	b.f.Type().Id(iface).Interface(required...)
	for _, mi := range optional {
		if b.usedTypeNames[mi.Optional] {
			panic(fmt.Sprintf("BUG: optional interface %s of %s %q collides with a generated type", mi.Optional, side, mi.Method))
		}
		b.usedTypeNames[mi.Optional] = true
		reply := fmt.Sprintf("answers %q with -32601 (method not found)", mi.Method)
		if mi.Notif != "" {
			reply = fmt.Sprintf("drops %q and logs a -32601 (method not found) error", mi.Method)
		}
		b.f.Comment(fmt.Sprintf("%s is the optional interface for the %s method %q.", mi.Optional, side, mi.Method))
		b.f.Comment(fmt.Sprintf("When the %s does not implement it, [%sSideConnection] %s.", side, iface, reply))
		b.f.Type().Id(mi.Optional).Interface(b.interfaceMethod(mi)...)
	}
}

// interfaceMethod returns the doc comment and the signature of the Go method
// that handles mi.
func (b *typesBuilder) interfaceMethod(mi *ir.MethodInfo) []Code {
	code := appendDocComments(nil, methodDescription(b.schema, mi))
	ctx := Id("ctx").Qual("context", "Context")
	if mi.Notif != "" {
		return append(code, Id(mi.Name).Params(ctx, Id("params").Id(mi.Notif)).Error())
	}
	respName := strings.TrimSuffix(mi.Req, "Request") + "Response"
	if ir.IsNullResponse(b.schema.Defs[respName]) {
		return append(code, Id(mi.Name).Params(ctx, Id("params").Id(mi.Req)).Error())
	}
	return append(code, Id(mi.Name).Params(ctx, Id("params").Id(mi.Req)).Params(Id(respName), Error()))
}

func isStringConstUnion(def *load.Definition) bool {
	if def == nil || len(def.OneOf) == 0 {
		return false
	}
	for _, v := range def.OneOf {
		if v == nil || v.Const == nil {
			return false
		}
		if _, ok := v.Const.(string); !ok {
			return false
		}
	}
	return true
}

// otherEnumValue is the member of a closed enum that stands for every value the
// enum does not list. The Rust schema crate marks that member #[serde(other)].
const otherEnumValue = "other"

func hasOtherMember(def *load.Definition) bool {
	return slices.Contains(enumValues(def), otherEnumValue)
}

// emitEnum declares the closed string enum name with one constant per member.
// An enum with the member "other" also gets an UnmarshalJSON that reads every
// string the enum does not list as that member.
func (b *typesBuilder) emitEnum(name string, def *load.Definition) {
	f := b.f
	f.Type().Id(name).String()
	members := []Code{}
	defs := []Code{}
	docs := constDocs(def.OneOf)
	for value, note := range def.EnumNotes {
		if docs[value] == "" {
			docs[value] = note
		}
	}
	for _, value := range enumValues(def) {
		members = append(members, Id(util.ToEnumConst(name, value)))
		defs = appendDocComments(defs, docs[value])
		defs = append(defs, Id(util.ToEnumConst(name, value)).Id(name).Op("=").Lit(value))
	}
	if len(defs) > 0 {
		f.Const().Defs(defs...)
	}
	f.Line()
	if !hasOtherMember(def) {
		return
	}

	other := util.ToEnumConst(name, otherEnumValue)
	f.Comment(fmt.Sprintf("UnmarshalJSON reads a string that %s does not list as %s.", name, other))
	f.Func().Params(Id("v").Op("*").Id(name)).Id("UnmarshalJSON").Params(Id("b").Index().Byte()).Error().Block(
		If(Id("string").Call(Id("b")).Op("==").Lit("null")).Block(Return(Nil())),
		Var().Id("s").String(),
		If(Id("err").Op(":=").Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("s")), Id("err").Op("!=").Nil()).Block(
			Return(Id("err")),
		),
		Switch(Id("member").Op(":=").Id(name).Call(Id("s")), Id("member")).Block(
			Case(members...).Block(Op("*").Id("v").Op("=").Id("member")),
			Default().Block(Op("*").Id("v").Op("=").Id(other)),
		),
		Return(Nil()),
	)
	f.Line()
}

// isOpenStringEnum reports whether def is an "open" string enum: an `anyOf`
// where every variant is a plain string schema (type: "string") and at least
// one variant has a string `const`. Such schemas describe a string value with
// a recommended set of known values while still permitting arbitrary strings
// for extensibility (see e.g. SessionConfigOptionCategory).
//
// These are emitted as a Go string-derived type with named constants for each
// known value, rather than as a discriminated union wrapper.
func isOpenStringEnum(def *load.Definition) bool {
	if def == nil || len(def.AnyOf) == 0 {
		return false
	}
	sawConst := false
	for _, v := range def.AnyOf {
		if v == nil {
			return false
		}
		// Each variant must be a plain string schema: type "string", no nested
		// structure (properties, items, refs, nested unions, allOf, etc.).
		if ir.PrimaryType(v) != "string" {
			return false
		}
		if v.Ref != "" || len(v.Properties) > 0 || v.Items != nil ||
			len(v.AnyOf) > 0 || len(v.OneOf) > 0 || len(v.AllOf) > 0 ||
			len(v.Enum) > 0 {
			return false
		}
		if v.Const != nil {
			if _, ok := v.Const.(string); !ok {
				return false
			}
			sawConst = true
		}
	}
	return sawConst
}

// emitValidateJen generates validators for selected types that are not union
// wrappers. It checks the required properties of def, which are struct fields.
func emitValidateJen(f *File, name string, def *load.Definition) {
	switch name {
	case "ToolCallUpdate":
		f.Func().Params(Id("t").Op("*").Id("ToolCallUpdate")).Id("Validate").Params().Params(Error()).Block(
			If(Id("t").Dot("ToolCallId").Op("==").Lit("")).Block(Return(Qual("fmt", "Errorf").Call(Lit("toolCallId is required")))),
			Return(Nil()),
		)
		return
	}
	if def != nil && ir.PrimaryType(def) == "object" {
		if !strings.HasSuffix(name, "Request") && !strings.HasSuffix(name, "Response") && !strings.HasSuffix(name, "Notification") {
			return
		}
		f.Func().Params(Id("v").Op("*").Id(name)).Id("Validate").Params().Params(Error()).BlockFunc(func(g *Group) {
			for _, propName := range sortedPropertyKeys(def.Properties) {
				if !slices.Contains(def.Required, propName) {
					continue
				}
				if check := requiredCheck(propName, def.Properties[propName], "v"); check != nil {
					g.Add(check)
				}
			}
			g.Return(Nil())
		})
	}
}

// requiredCheck returns the Validate statement that rejects an unset required
// property propName of the value at path, for example "v" or "u", "Form". Unset
// means an empty string or a nil array. Other property types get no check, so
// it returns nil.
func requiredCheck(propName string, pDef *load.Definition, path ...string) Code {
	var unset Code
	switch ir.PrimaryType(pDef) {
	case "string":
		unset = Lit("")
	case "array":
		unset = Nil()
	default:
		return nil
	}
	value := Id(path[0])
	for _, p := range path[1:] {
		value = value.Dot(p)
	}
	return If(value.Dot(util.ToExportedField(propName)).Op("==").Add(unset)).Block(
		Return(Qual("fmt", "Errorf").Call(Lit(propName + " is required"))),
	)
}

func methodDescription(schema *load.Schema, mi *ir.MethodInfo) string {
	if mi == nil || mi.DocsIgnored {
		return ""
	}
	// Prefer notification descriptions when present, otherwise fall back to request/response types.
	ordered := []string{mi.Notif, mi.Req, mi.Resp}
	for _, name := range ordered {
		if name == "" {
			continue
		}
		if def := schema.Defs[name]; def != nil && def.Description != "" {
			return def.Description
		}
	}
	return ""
}

// integerType is the Go type of an integer schema: int64 for the formats
// int64, uint32, and uint64, so that a 32-bit target holds every value that a
// peer sends (such as the exit code 3221225477 of a Windows process, or a
// token count above 2^31), and int otherwise. A uint64 above 2^63 does not fit;
// no ACP value comes near it.
func integerType(d *load.Definition) *Statement {
	if d != nil {
		switch d.Format {
		case "int64", "uint32", "uint64":
			return Int64()
		}
	}
	return Int()
}

// Type mapping helpers (unchanged behavior vs original)
func primitiveJenType(d *load.Definition) Code {
	switch ir.PrimaryType(d) {
	case "string":
		return String()
	case "integer":
		return integerType(d)
	case "number":
		return Float64()
	case "boolean":
		return Bool()
	default:
		return Any()
	}
}

// includesNull reports whether the property's type union contains null.
func includesNull(d *load.Definition) bool {
	if d == nil || d.Type == nil {
		return false
	}
	if arr, ok := d.Type.([]any); ok {
		for _, v := range arr {
			if s, ok2 := v.(string); ok2 && s == "null" {
				return true
			}
		}
	}
	return false
}

// acceptsNull reports whether JSON null is a valid value of d. A nil d stands
// for a property without a schema, which accepts every value.
func acceptsNull(schema *load.Schema, d *load.Definition) bool {
	switch {
	case d == nil:
		return true
	case d.Ref != "":
		return acceptsNull(schema, schema.Defs[strings.TrimPrefix(d.Ref, "#/$defs/")])
	case d.Type != nil:
		return d.Type == "null" || includesNull(d)
	case d.Const != nil || len(d.Enum) > 0:
		return false
	}
	alternatives := append(append([]*load.Definition{}, d.AnyOf...), d.OneOf...)
	if slices.ContainsFunc(alternatives, func(alt *load.Definition) bool { return acceptsNull(schema, alt) }) {
		return true
	}
	rejects := func(part *load.Definition) bool { return !acceptsNull(schema, part) }
	return len(alternatives) == 0 && !slices.ContainsFunc(d.AllOf, rejects)
}

// expandAllOf merges JSON Schema allOf nodes into a shallow composite definition.
//
// ACP's schema frequently uses `allOf: [{"$ref": "#/$defs/Type"}]` so a property or union
// variant can attach local metadata (description/default/const constraints) while reusing
// a referenced shape.
//
// For codegen purposes we approximate allOf by merging object properties + required lists.
// We intentionally do not attempt to resolve semantic constraints beyond that.
func expandAllOf(schema *load.Schema, d *load.Definition) *load.Definition {
	if d == nil || len(d.AllOf) == 0 {
		return d
	}

	merged := *d
	// Avoid re-processing in recursive calls.
	merged.AllOf = nil

	if d.Properties != nil {
		merged.Properties = make(map[string]*load.Definition, len(d.Properties))
		for k, v := range d.Properties {
			merged.Properties[k] = v
		}
	}
	if d.Required != nil {
		merged.Required = append([]string(nil), d.Required...)
	}

	reqSet := map[string]struct{}{}
	for _, r := range merged.Required {
		reqSet[r] = struct{}{}
	}

	for _, part := range d.AllOf {
		if part == nil {
			continue
		}
		resolved := part
		if part.Ref != "" && schema != nil && strings.HasPrefix(part.Ref, "#/$defs/") {
			if def := schema.Defs[part.Ref[len("#/$defs/"):]]; def != nil {
				resolved = def
			}
		}
		resolved = expandAllOf(schema, resolved)

		if merged.Type == nil && resolved.Type != nil {
			merged.Type = resolved.Type
		}
		if merged.Items == nil && resolved.Items != nil {
			merged.Items = resolved.Items
		}
		if merged.Ref == "" && resolved.Ref != "" {
			merged.Ref = resolved.Ref
		}
		if len(merged.Enum) == 0 && len(resolved.Enum) > 0 {
			merged.Enum = resolved.Enum
		}
		if len(merged.AnyOf) == 0 && len(resolved.AnyOf) > 0 {
			merged.AnyOf = resolved.AnyOf
		}
		if len(merged.OneOf) == 0 && len(resolved.OneOf) > 0 {
			merged.OneOf = resolved.OneOf
		}
		// The alternatives above take their unnamed members along.
		merged.UnevaluatedProperties = merged.UnevaluatedProperties || resolved.UnevaluatedProperties

		if len(resolved.Properties) > 0 {
			if merged.Properties == nil {
				merged.Properties = make(map[string]*load.Definition)
			}
			for k, v := range resolved.Properties {
				if _, ok := merged.Properties[k]; ok {
					continue
				}
				merged.Properties[k] = v
			}
		}
		for _, r := range resolved.Required {
			if _, ok := reqSet[r]; ok {
				continue
			}
			merged.Required = append(merged.Required, r)
			reqSet[r] = struct{}{}
		}
	}

	return &merged
}

// rawJSON is the Go type of a value that the schema leaves free. It holds the
// JSON as it was read, so a number keeps every digit.
func rawJSON() *Statement { return Qual("encoding/json", "RawMessage") }

// memberType is the Go type of each member of d, an object schema without
// properties: the type of the additionalProperties schema, or raw JSON when
// it leaves the members free.
func memberType(d *load.Definition) Code {
	if freeMembers(d) {
		return rawJSON()
	}
	return jenTypeFor(d.AdditionalProperties.Schema)
}

// freeMembers reports whether d takes every value for a member that its
// properties do not name: additionalProperties is absent, true, or a schema
// that leaves the value free.
func freeMembers(d *load.Definition) bool {
	s := d.AdditionalProperties.Schema
	return s == nil || leavesFree(s)
}

// leavesFree reports whether d accepts every JSON value: it has no keyword
// that constrains a value, as {} or a schema with only a description.
func leavesFree(d *load.Definition) bool {
	return d.Type == nil && d.Ref == "" && len(d.AnyOf) == 0 && len(d.OneOf) == 0 && len(d.AllOf) == 0 &&
		len(d.Enum) == 0 && d.Const == nil && len(d.Properties) == 0 && len(d.Required) == 0 && d.Items == nil &&
		d.Not == nil && d.AdditionalProperties.Schema == nil
}

func jenTypeFor(d *load.Definition) Code {
	if d == nil {
		return rawJSON()
	}

	// JSON Schema 2020-12 commonly uses allOf wrappers around a single $ref so a property can
	// attach local metadata like descriptions and defaults.
	if len(d.AllOf) > 0 && d.Ref == "" {
		// Prefer a $ref element when present.
		for _, e := range d.AllOf {
			if e != nil && e.Ref != "" {
				return jenTypeFor(e)
			}
		}
		// Otherwise fall back to the first non-nil schema.
		for _, e := range d.AllOf {
			if e != nil {
				return jenTypeFor(e)
			}
		}
	}

	if d.Ref != "" {
		if strings.HasPrefix(d.Ref, "#/$defs/") {
			return Id(d.Ref[len("#/$defs/"):])
		}
		return rawJSON()
	}
	if len(d.Enum) > 0 {
		return String()
	}
	switch ir.PrimaryType(d) {
	case "string":
		return String()
	case "integer":
		return integerType(d)
	case "number":
		return Float64()
	case "boolean":
		return Bool()
	case "array":
		return Index().Add(jenTypeFor(d.Items))
	case "object":
		// An object property with properties gets a struct of its own before
		// it gets here, so this object is a map.
		return Map(String()).Add(memberType(d))
	default:
		// A union that is not a named type, or a value of any kind.
		return rawJSON()
	}
}

func nonNullAlternative(d *load.Definition) *load.Definition {
	alts := d.AnyOf
	if len(alts) == 0 {
		alts = d.OneOf
	}
	if len(alts) != 2 {
		return nil
	}
	var nonNull *load.Definition
	for _, alt := range alts {
		if alt == nil {
			continue
		}
		if s, ok := alt.Type.(string); ok && s == "null" {
			continue
		}
		nonNull = alt
	}
	return nonNull
}

// jenTypeForOptional maps unions that include null to pointer types where applicable.
func jenTypeForOptional(d *load.Definition) Code {
	if d == nil {
		return rawJSON()
	}
	// Case: property type is a union like ["string","null"]
	if arr, ok := d.Type.([]any); ok && len(arr) == 2 {
		var other string
		for _, v := range arr {
			if s, ok2 := v.(string); ok2 {
				if s == "null" {
					continue
				}
				other = s
			}
		}
		switch other {
		case "string":
			return Op("*").String()
		case "integer":
			return Op("*").Add(integerType(d))
		case "number":
			return Op("*").Float64()
		case "boolean":
			return Op("*").Bool()
		}
	}
	if nonNull := nonNullAlternative(d); nonNull != nil {
		if nonNull.Const != nil {
			nn := *nonNull
			nn.Type = "string"
			nonNull = &nn
		}
		if nonNull.Ref != "" && strings.HasPrefix(nonNull.Ref, "#/$defs/") {
			return Op("*").Id(nonNull.Ref[len("#/$defs/"):])
		}
		switch ir.PrimaryType(nonNull) {
		case "string":
			return Op("*").String()
		case "integer":
			return Op("*").Add(integerType(nonNull))
		case "number":
			return Op("*").Float64()
		case "boolean":
			return Op("*").Bool()
		}
	}
	return jenTypeFor(d)
}

// unionVariant is one variant of a union as emitUnion emitted it: the wrapper
// field, the variant type, the discriminator, and the variant's own required
// keys and properties. nested holds the alternatives of an inline variant that
// is a union in turn, which encodes only with one of them set.
type unionVariant struct {
	fieldName string
	typeName  string
	discKey   string
	discValue string
	required  []string
	props     map[string]*load.Definition
	nested    []unionVariant
}

// sortedPropertyKeys returns the keys of props in sorted order.
func sortedPropertyKeys(props map[string]*load.Definition) []string {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// firstConstPropertyKey returns the first property of v, in sorted key order,
// that has a const value, or "" if none has one. emitUnion uses it to find a
// fallback discriminator, so it picks the same key on every run.
func firstConstPropertyKey(v *load.Definition) string {
	for _, k := range sortedPropertyKeys(v.Properties) {
		if pd := v.Properties[k]; pd != nil && pd.Const != nil {
			return k
		}
	}
	return ""
}

// isPhantomVariant reports whether the expanded union variant v has no $ref
// (ref), no properties, no null type, and no title. emitUnion emits no type for
// such a variant and records none, so WriteHelpersJen emits no constructor for
// it. Title-only variants such as ExtMethodRequest are not phantom.
func isPhantomVariant(v *load.Definition, ref string) bool {
	isNull := false
	if s, ok := v.Type.(string); ok && s == "null" {
		isNull = true
	}
	return len(v.Properties) == 0 && ref == "" && !isNull && v.Title == ""
}

func variantRef(v *load.Definition) string {
	// If this is an allOf wrapper around a single $ref with no additional structure, treat it
	// as a $ref variant (keeps types stable and avoids duplicate structs).
	if v.Ref == "" &&
		v.Type == nil &&
		len(v.Properties) == 0 &&
		len(v.Required) == 0 &&
		len(v.Enum) == 0 &&
		v.Items == nil &&
		len(v.AnyOf) == 0 &&
		len(v.OneOf) == 0 &&
		len(v.AllOf) == 1 &&
		v.AllOf[0] != nil &&
		v.AllOf[0].Ref != "" {
		return v.AllOf[0].Ref
	}
	return v.Ref
}

type unionAlternative struct {
	index    int
	ref      string
	expanded *load.Definition
}

func unionAlternatives(schema *load.Schema, defs []*load.Definition) []unionAlternative {
	var alts []unionAlternative
	for index, v := range defs {
		if v == nil {
			continue
		}
		ref := variantRef(v)
		v = expandAllOf(schema, v)
		if !isPhantomVariant(v, ref) {
			alts = append(alts, unionAlternative{index: index, ref: ref, expanded: v})
		}
	}
	return alts
}

func (a unionAlternative) isNull() bool {
	s, ok := a.expanded.Type.(string)
	return ok && s == "null"
}

func (a unionAlternative) isInlineObject() bool {
	return a.ref == "" && len(a.expanded.Properties) > 0 && !a.isNull()
}

func allInlineObjects(alts []unionAlternative) bool {
	for _, alt := range alts {
		if !alt.isInlineObject() {
			return false
		}
	}
	return true
}

func scalarConstOf(v *load.Definition) Code {
	switch value := v.Const.(type) {
	case float64:
		if ir.PrimaryType(v) == "integer" {
			return Lit(int(value))
		}
		if ir.PrimaryType(v) == "number" {
			return Lit(value)
		}
	case string:
		if ir.PrimaryType(v) == "string" {
			return Lit(value)
		}
	case bool:
		if ir.PrimaryType(v) == "boolean" {
			return Lit(value)
		}
	}
	return nil
}

// opensMembers reports whether d takes members that it does not name:
// "additionalProperties": true or "unevaluatedProperties": true.
func opensMembers(d *load.Definition) bool {
	return d.AdditionalProperties.Open || bool(d.UnevaluatedProperties)
}

// namedProperties returns the properties that d names: its own, those of
// its $ref and allOf, and those of each of its anyOf and oneOf
// alternatives, at any depth.
func namedProperties(schema *load.Schema, d *load.Definition, seen map[*load.Definition]bool) []string {
	if d == nil || seen[d] {
		return nil
	}
	seen[d] = true
	d = expandAllOf(schema, d)
	keys := sortedPropertyKeys(d.Properties)
	if d.Ref != "" {
		keys = append(keys, namedProperties(schema, schema.Defs[strings.TrimPrefix(d.Ref, "#/$defs/")], seen)...)
	}
	for _, alt := range append(append([]*load.Definition{}, d.AnyOf...), d.OneOf...) {
		keys = append(keys, namedProperties(schema, alt, seen)...)
	}
	return keys
}

// emitUnion emits the union wrapper type name and its variants. With exactlyOne
// (a oneOf), the wrapper's Validate requires exactly one variant. Validate is
// emitted when exactlyOne or validate is set. It also checks the union's own
// required properties on the set variant. It returns the variants, and records
// those of a oneOf for WriteHelpersJen.
func (b *typesBuilder) emitUnion(name string, parentDef *load.Definition, defs []*load.Definition, exactlyOne, validate bool) []unionVariant {
	f, schema, usedTypeNames := b.f, b.schema, b.usedTypeNames
	var recorded []unionVariant
	type variantInfo struct {
		fieldName         string
		typeName          string
		required          []string
		props             map[string]*load.Definition
		isObject          bool
		isArray           bool
		arrayItemRequired []string
		arrayItemProps    map[string]*load.Definition
		discValue         string
		constPairs        [][2]string
		// openDiscriminator says that the discriminator of this variant is a
		// string without a const, as in a catch-all variant.
		openDiscriminator bool
		// nullKeys are the properties whose type is "null" alone, as in a
		// "none" variant. The variant matches when they are absent or null.
		nullKeys    []string
		scalarConst Code
		isNull      bool
		description string
		// sharedChecks hold Validate statements for the union's own required
		// properties, which an inline object variant copies into its struct.
		sharedChecks []Code
	}
	// matchesByKeys says that an object selects the variant by its keys, and
	// never by the order of the variants.
	matchesByKeys := func(vi variantInfo) bool {
		return vi.isObject && (len(vi.required) > 0 || len(vi.constPairs) > 0 || len(vi.nullKeys) > 0)
	}
	// requireKey returns the statement that clears match unless the JSON object
	// obj has the required key. A null value counts only when def, the schema
	// of the key, accepts null or has a hint that reads null as the default.
	requireKey := func(obj, key string, def *load.Definition) Code {
		noMatch := Id("match").Op("=").Lit(false)
		if acceptsNull(schema, def) || def.DefaultOnNull || def.DefaultOnError {
			return If(List(Id("_"), Id("ok")).Op(":=").Id(obj).Index(Lit(key)), Op("!").Id("ok")).Block(noMatch)
		}
		return If(Id("raw").Op(":=").Id("string").Call(Id(obj).Index(Lit(key))), Id("raw").Op("==").Lit("").Op("||").Id("raw").Op("==").Lit("null")).Block(noMatch)
	}
	variants := []variantInfo{}
	discKey := ""
	// Use schema's explicit discriminator if available
	if parentDef != nil && parentDef.Discriminator != nil {
		discKey = parentDef.Discriminator.PropertyName
	}
	// Fallback: discover discriminator key by scanning for const properties
	// (for backward compatibility with older schemas without discriminator metadata)
	if discKey == "" {
		for _, v := range defs {
			if v == nil {
				continue
			}
			discKey = firstConstPropertyKey(expandAllOf(schema, v))
			if discKey != "" {
				break
			}
		}
	}
	sharedProps := map[string]*load.Definition{}
	sharedRequired := map[string]struct{}{}
	if parentDef != nil {
		for k, p := range parentDef.Properties {
			sharedProps[k] = p
		}
		for _, r := range parentDef.Required {
			sharedRequired[r] = struct{}{}
		}
	}
	// open says that the union takes members that no part of it names. The
	// wrapper keeps them in AdditionalProperties.
	open := parentDef != nil && opensMembers(parentDef)
	alts := unionAlternatives(schema, defs)
	var wrapperFields []ir.Field
	if len(sharedProps) > 0 && !allInlineObjects(alts) {
		wrapperFields = ir.Fields(parentDef.Properties, parentDef.Required)
		sharedProps = map[string]*load.Definition{}
		sharedRequired = map[string]struct{}{}
	}

	for _, alt := range alts {
		idx, ref, v, isNull := alt.index, alt.ref, alt.expanded, alt.isNull()
		// Determine type name: prefer $ref target name when present; do not treat Title as a rename for $ref.
		tname := ""
		if ref != "" && strings.HasPrefix(ref, "#/$defs/") {
			tname = ref[len("#/$defs/"):]
		} else if v.Title != "" {
			// Scope inline variant titles to parent union name with smart naming
			tname = generateNestedTypeName(name, v.Title, usedTypeNames)
			// DEFENSIVE PROGRAMMING: Assert no collision before registering
			if usedTypeNames[tname] {
				panic(fmt.Sprintf("BUG: variant type name collision: %q already registered (parent: %q, title: %q)",
					tname, name, v.Title))
			}
			usedTypeNames[tname] = true
		} else {
			if discKey != "" {
				if pd := v.Properties[discKey]; pd != nil && pd.Const != nil {
					s := fmt.Sprint(pd.Const)
					tname = generateNestedTypeName(name, s, usedTypeNames)
					// DEFENSIVE PROGRAMMING: Assert no collision before registering
					if usedTypeNames[tname] {
						panic(fmt.Sprintf("BUG: variant type name collision: %q already registered (parent: %q, discriminator: %q)",
							tname, name, s))
					}
					usedTypeNames[tname] = true
				}
			}
			if tname == "" {
				tname = name + fmt.Sprintf("Variant%d", idx+1)
				// DEFENSIVE PROGRAMMING: Assert no collision before registering
				if usedTypeNames[tname] {
					panic(fmt.Sprintf("BUG: variant type name collision: %q already registered (parent: %q)",
						tname, name))
				}
				usedTypeNames[tname] = true
			}
		}
		// Ensure Title-derived names are exported (e.g., "stdio" -> "Stdio").
		tname = util.ToExportedField(tname)

		// Derive field name with priority: discriminator value > title > type name
		fieldName := ""
		dv := ""

		// Priority 1: Use discriminator const value (most ergonomic)
		if discKey != "" {
			if pd := v.Properties[discKey]; pd != nil && pd.Const != nil {
				s := fmt.Sprint(pd.Const)
				fieldName = util.ToExportedField(s)
				dv = s
			}
		}

		// Priority 2: Use original title if no discriminator (e.g., "stdio")
		if fieldName == "" && v.Title != "" {
			fieldName = util.ToExportedField(v.Title)
		}

		// Priority 3: Fall back to type name
		if fieldName == "" {
			fieldName = util.ToExportedField(tname)
		}
		isObj := len(v.Properties) > 0
		isArray := ir.PrimaryType(v) == "array"
		arrayItemRequired := []string{}
		var arrayItemProps map[string]*load.Definition
		if isArray && v.Items != nil {
			item := v.Items
			if item.Ref != "" && strings.HasPrefix(item.Ref, "#/$defs/") {
				if d := schema.Defs[item.Ref[len("#/$defs/"):]]; d != nil {
					item = d
				}
			}
			item = expandAllOf(schema, item)
			if ir.PrimaryType(item) == "object" && len(item.Required) > 0 {
				arrayItemRequired = append(arrayItemRequired, item.Required...)
				sort.Strings(arrayItemRequired)
				arrayItemProps = item.Properties
			}
		}
		// collect const properties (e.g., type, outcome), in sorted key order so
		// that the MarshalJSON injections do not depend on map iteration order
		consts := [][2]string{}
		for _, pk := range sortedPropertyKeys(v.Properties) {
			if pd := v.Properties[pk]; pd != nil && pd.Const != nil {
				if s, ok := pd.Const.(string); ok {
					consts = append(consts, [2]string{pk, s})
				}
			}
		}
		openDiscriminator := false
		if pd := v.Properties[discKey]; pd != nil && pd.Const == nil {
			openDiscriminator = pd.Type == "string"
		}
		var nullKeys []string
		for _, pk := range sortedPropertyKeys(v.Properties) {
			if pd := v.Properties[pk]; pd != nil && pd.Type == "null" {
				nullKeys = append(nullKeys, pk)
			}
		}
		var sharedChecks []Code
		var nestedVariants []unionVariant
		// Emit struct for inline variants (non-$ref)
		if (isObj || isNull || v.Title != "") && ref == "" {
			// DEFENSIVE PROGRAMMING: Verify tname is registered before emitting
			if !usedTypeNames[tname] {
				panic(fmt.Sprintf("BUG: attempting to emit unregistered type: %q (parent union: %q)", tname, name))
			}

			switch {
			case isNull:
				f.Type().Id(tname).Struct()
				f.Line()
			case isObj:
				mergedProps := make(map[string]*load.Definition, len(sharedProps)+len(v.Properties))
				for pk, pDef := range sharedProps {
					mergedProps[pk] = pDef
				}
				for pk, pDef := range v.Properties {
					mergedProps[pk] = pDef
				}
				required := append([]string(nil), v.Required...)
				for r := range sharedRequired {
					required = append(required, r)
				}
				fields := ir.Fields(mergedProps, required)
				for _, field := range fields {
					if _, shared := sharedRequired[field.Prop]; shared && field.Def.Const == nil {
						if check := requiredCheck(field.Prop, field.Def, "u", fieldName); check != nil {
							sharedChecks = append(sharedChecks, check)
						}
					}
				}
				if v.Description != "" {
					emitDocComment(f, v.Description)
				}
				nested := v.AnyOf
				if len(nested) == 0 {
					nested = v.OneOf
				}
				if len(nested) > 0 {
					nestedVariants = b.emitUnion(tname, &load.Definition{
						Properties: mergedProps, Required: required, Discriminator: v.Discriminator,
						AdditionalProperties: v.AdditionalProperties, UnevaluatedProperties: v.UnevaluatedProperties,
					}, nested, len(v.OneOf) > 0, false)
				} else {
					b.emitStruct(tname, fields, nil, opensMembers(v))
				}
			default:
				// Title-only variants: check if they're extension types
				// Extension types should preserve the raw payload, not drop it
				isExtension := strings.Contains(strings.ToLower(v.Description), "extension") ||
					strings.Contains(v.Title, "Ext")

				if v.Description != "" {
					emitDocComment(f, v.Description)
				}

				if isExtension {
					// Emit as type alias (=) to json.RawMessage to preserve marshal/unmarshal methods.
					// Using type alias ensures the RawMessage methods are inherited, unlike a defined type.
					f.Type().Id(tname).Op("=").Qual("encoding/json", "RawMessage")
				} else if ir.PrimaryType(v) != "" {
					// Non-object title-only variants can still carry payloads (e.g. arrays).
					// Emit a concrete type so sequential decode can succeed for valid payloads.
					f.Type().Id(tname).Add(jenTypeFor(v))
				} else {
					// Emit as empty struct only for truly empty variants.
					f.Type().Id(tname).Struct()
				}
				f.Line()
			}
		}
		variants = append(variants, variantInfo{
			fieldName:         fieldName,
			typeName:          tname,
			required:          v.Required,
			props:             v.Properties,
			isObject:          isObj,
			isArray:           isArray,
			arrayItemRequired: arrayItemRequired,
			arrayItemProps:    arrayItemProps,
			discValue:         dv,
			constPairs:        consts,
			openDiscriminator: openDiscriminator,
			nullKeys:          nullKeys,
			scalarConst:       scalarConstOf(v),
			isNull:            isNull,
			description:       v.Description,
			sharedChecks:      sharedChecks,
		})
		recorded = append(recorded, unionVariant{
			fieldName: fieldName,
			typeName:  tname,
			discKey:   discKey,
			discValue: dv,
			required:  append([]string(nil), v.Required...),
			props:     v.Properties,
			nested:    nestedVariants,
		})
	}
	if exactlyOne {
		b.variants[name] = recorded
	}
	// wrapper
	wrapperPlans := b.plansFor(name, wrapperFields)
	st := fieldDecls(wrapperPlans, nil)
	wrapperFieldNames := map[string]bool{}
	for _, field := range wrapperFields {
		wrapperFieldNames[field.Name] = true
	}
	for _, vi := range variants {
		if wrapperFieldNames[vi.fieldName] {
			panic(fmt.Sprintf("BUG: union %q has an own property and a variant that both map to the field %q", name, vi.fieldName))
		}
		if vi.description != "" {
			st = appendDocComments(st, vi.description)
		}
		st = append(st, Id(vi.fieldName).Op("*").Id(vi.typeName).Tag(map[string]string{"json": "-"}))
	}
	// named lists the members that the union or one of its variants names.
	var named []Code
	if open {
		if wrapperFieldNames[additionalPropertiesField] || slices.ContainsFunc(variants, func(vi variantInfo) bool { return vi.fieldName == additionalPropertiesField }) {
			panic(fmt.Sprintf("union %q takes unnamed members and has a property or variant that maps to the field %q", name, additionalPropertiesField))
		}
		b.usesAdditionalProperties = true
		st = append(st,
			Comment("AdditionalProperties holds the members that the schema does not name."),
			Id(additionalPropertiesField).Map(String()).Qual("encoding/json", "RawMessage").Tag(map[string]string{"json": "-"}),
		)
		keys := sortedPropertyKeys(parentDef.Properties)
		for _, alt := range alts {
			keys = append(keys, namedProperties(schema, alt.expanded, map[*load.Definition]bool{})...)
		}
		sort.Strings(keys)
		for _, k := range slices.Compact(keys) {
			named = append(named, Lit(k))
		}
	}
	f.Type().Id(name).Struct(st...)
	f.Line()
	// Unmarshal
	f.Func().Params(Id("u").Op("*").Id(name)).Id("UnmarshalJSON").Params(Id("b").Index().Byte()).Error().BlockFunc(func(g *Group) {
		g.Op("*").Id("u").Op("=").Id(name).Values()
		// JSON null selects the null variant and no other, and the null
		// variant takes nothing else.
		g.If(Id("string").Call(Id("b")).Op("==").Lit("null")).BlockFunc(func(null *Group) {
			if i := slices.IndexFunc(variants, func(vi variantInfo) bool { return vi.isNull }); i >= 0 {
				null.Var().Id("v").Id(variants[i].typeName)
				null.Id("u").Dot(variants[i].fieldName).Op("=").Op("&").Id("v")
				null.Return(Nil())
				return
			}
			null.Return(Qual("errors", "New").Call(Lit("no matching variant for union")))
		})
		g.Var().Id("m").Map(String()).Qual("encoding/json", "RawMessage")
		g.If(List(Id("err")).Op(":=").Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("m")), Id("err").Op("==").Nil()).BlockFunc(func(obj *Group) {
			if len(wrapperFields) > 0 {
				obj.BlockFunc(func(own *Group) { b.emitDecodeFields(own, name, "u", customDecoded(wrapperPlans)) })
			}
			if open {
				obj.Id("u").Dot(additionalPropertiesField).Op("=").Id("additionalProperties").Call(append([]Code{Id("b")}, named...)...)
			}
			if parentDef != nil && parentDef.Properties[discKey] != nil && parentDef.Properties[discKey].DefaultOnError {
				// The discriminator has x-deserialize-default-on-error: a value
				// that is neither a string nor null reads as absent.
				obj.If(
					Id("raw").Op(":=").Id("string").Call(Id("m").Index(Lit(discKey))),
					Id("raw").Op("!=").Lit("").Op("&&").Id("raw").Op("!=").Lit("null").Op("&&").Op("!").Qual("strings", "HasPrefix").Call(Id("raw"), Lit("\"")),
				).Block(Id("delete").Call(Id("m"), Lit(discKey)))
			}
			// Prefer discriminator-based dispatch when available (e.g. "type", "outcome")
			if discKey != "" {
				obj.BlockFunc(func(h *Group) {
					h.Var().Id("disc").String()
					h.If(List(Id("v"), Id("ok")).Op(":=").Id("m").Index(Lit(discKey)), Id("ok")).Block(
						Qual("encoding/json", "Unmarshal").Call(Id("v"), Op("&").Id("disc")),
					)
					h.Switch(Id("disc")).BlockFunc(func(sw *Group) {
						for _, vi := range variants {
							if vi.discValue != "" {
								sw.Case(Lit(vi.discValue)).Block(
									Var().Id("v").Id(vi.typeName),
									If(Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("v")).Op("!=").Nil()).Block(Return(Qual("errors", "New").Call(Lit("invalid variant payload")))),
									Id("u").Dot(vi.fieldName).Op("=").Op("&").Id("v"),
									Return(Nil()),
								)
							}
						}
					})
				})
			}
			// A variant matches when the object has its required keys, each
			// const property that is present holds its value, and each
			// null-only property is absent or null. A required key that is
			// null counts as absent unless its schema accepts null or a hint
			// reads null as the default. The switch above settled a variant
			// whose only const is the required discriminator.
			for _, vi := range variants {
				switchSettled := vi.discValue != "" && len(vi.constPairs) == 1 && slices.Contains(vi.required, discKey)
				if matchesByKeys(vi) && !switchSettled {
					obj.BlockFunc(func(h *Group) {
						h.Var().Id("v").Id(vi.typeName)
						h.Var().Id("match").Bool().Op("=").Lit(true)
						for _, rk := range vi.required {
							if rk == discKey && vi.openDiscriminator {
								h.If(Op("!").Qual("strings", "HasPrefix").Call(Id("string").Call(Id("m").Index(Lit(rk))), Lit("\""))).Block(Id("match").Op("=").Lit(false))
								continue
							}
							def, ok := vi.props[rk]
							if !ok && parentDef != nil {
								def = parentDef.Properties[rk]
							}
							h.Add(requireKey("m", rk, def))
						}
						for _, pair := range vi.constPairs {
							h.If(List(Id("raw"), Id("ok")).Op(":=").Id("m").Index(Lit(pair[0])), Id("ok")).Block(
								Var().Id("value").String(),
								If(Qual("encoding/json", "Unmarshal").Call(Id("raw"), Op("&").Id("value")).Op("!=").Nil().Op("||").Id("value").Op("!=").Lit(pair[1])).Block(Id("match").Op("=").Lit(false)),
							)
						}
						for _, nk := range vi.nullKeys {
							h.If(Id("raw").Op(":=").Id("string").Call(Id("m").Index(Lit(nk))), Id("raw").Op("!=").Lit("").Op("&&").Id("raw").Op("!=").Lit("null")).Block(Id("match").Op("=").Lit(false))
						}
						h.If(Id("match")).Block(
							If(Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("v")).Op("!=").Nil()).Block(Return(Qual("errors", "New").Call(Lit("invalid variant payload")))),
							Id("u").Dot(vi.fieldName).Op("=").Op("&").Id("v"),
							Return(Nil()),
						)
					})
				}
			}
		}).Else().Block(
			// Not an object (e.g., primitive union variant) or invalid JSON.
			If(List(Id("_"), Id("ok")).Op(":=").Id("err").Assert(Op("*").Qual("encoding/json", "UnmarshalTypeError")), Op("!").Id("ok")).Block(Return(Id("err"))),
		)
		// For array variants with required keys on object items, try key-based matching first.
		g.Var().Id("arr").Index().Map(String()).Qual("encoding/json", "RawMessage")
		g.If(Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("arr")).Op("==").Nil()).BlockFunc(func(arr *Group) {
			for _, vi := range variants {
				if !vi.isArray || len(vi.arrayItemRequired) == 0 {
					continue
				}
				arr.BlockFunc(func(h *Group) {
					h.Var().Id("v").Id(vi.typeName)
					h.Var().Id("match").Bool().Op("=").Lit(true)
					h.For(List(Id("_"), Id("elem")).Op(":=").Range().Id("arr")).BlockFunc(func(loop *Group) {
						for _, rk := range vi.arrayItemRequired {
							loop.Add(requireKey("elem", rk, vi.arrayItemProps[rk]))
						}
					})
					h.If(Id("match")).Block(
						If(Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("v")).Op("!=").Nil()).Block(Return(Qual("errors", "New").Call(Lit("invalid variant payload")))),
						Id("u").Dot(vi.fieldName).Op("=").Op("&").Id("v"),
						Return(Nil()),
					)
				})
			}
		})
		// A variant that matches by its keys had its turn above. The others
		// are tried in order.
		for _, vi := range variants {
			if vi.isNull || matchesByKeys(vi) || (vi.isArray && len(vi.arrayItemRequired) > 0) {
				continue
			}
			decodes := Qual("encoding/json", "Unmarshal").Call(Id("b"), Op("&").Id("v")).Op("==").Nil()
			if vi.scalarConst != nil {
				decodes = decodes.Op("&&").Id("v").Op("==").Add(vi.scalarConst)
			}
			g.Block(
				Var().Id("v").Id(vi.typeName),
				If(decodes).Block(
					Id("u").Dot(vi.fieldName).Op("=").Op("&").Id("v"),
					Return(Nil()),
				),
			)
		}
		g.Return(Qual("errors", "New").Call(Lit("no matching variant for union")))
	})
	// Marshal
	f.Func().Params(Id("u").Id(name)).Id("MarshalJSON").Params().Params(Index().Byte(), Error()).BlockFunc(func(g *Group) {
		if len(wrapperFields) > 0 {
			g.Type().Id("plain").Id(name)
			g.Id("own").Op(":=").Id("plain").Call(Id("u"))
			emitFillNil(g, wrapperPlans, "own")
			g.List(Id("ownJSON"), Id("err")).Op(":=").Qual("encoding/json", "Marshal").Call(Id("own"))
			g.If(Id("err").Op("!=").Nil()).Block(Return(Nil(), Id("err")))
		}
		for _, vi := range variants {
			g.If(Id("u").Dot(vi.fieldName).Op("!=").Nil()).BlockFunc(func(gg *Group) {
				// Null-only variant encodes to JSON null
				if vi.isNull {
					gg.Return(Qual("encoding/json", "Marshal").Call(Nil()))
				} else {
					gg.List(Id("_b"), Id("_e")).Op(":=").Qual("encoding/json", "Marshal").Call(Op("*").Id("u").Dot(vi.fieldName))
					gg.If(Id("_e").Op("!=").Nil()).Block(Return(Index().Byte().Values(), Id("_e")))
					if !vi.isObject && len(wrapperFields) == 0 && !open {
						// Non-object variants (e.g., arrays/primitives) are already in final wire shape.
						gg.Return(Id("_b"), Nil())
						return
					}
					// The const properties and the union's own fields go into the
					// object of the variant. Its members stay raw JSON, so that a
					// number keeps every digit.
					gg.Var().Id("m").Map(String()).Qual("encoding/json", "RawMessage")
					notAnObject := Return(Index().Byte().Values(), Qual("errors", "New").Call(Lit("invalid variant payload")))
					if !vi.isObject {
						// JSON Schema applies the union's own properties to objects only.
						notAnObject = Return(Id("_b"), Nil())
					}
					gg.If(Qual("encoding/json", "Unmarshal").Call(Id("_b"), Op("&").Id("m")).Op("!=").Nil()).Block(notAnObject)
					for _, kv := range vi.constPairs {
						value, err := json.Marshal(kv[1])
						if err != nil {
							panic(fmt.Sprintf("encode the const %q of union %q: %v", kv[1], name, err))
						}
						gg.Id("m").Index(Lit(kv[0])).Op("=").Qual("encoding/json", "RawMessage").Call(Lit(string(value)))
					}
					if len(wrapperFields) > 0 {
						gg.If(Qual("encoding/json", "Unmarshal").Call(Id("ownJSON"), Op("&").Id("m")).Op("!=").Nil()).Block(Return(Index().Byte().Values(), Qual("errors", "New").Call(Lit("invalid variant payload"))))
					}
					if open {
						// A member that the union names keeps its value.
						gg.For(List(Id("k"), Id("raw")).Op(":=").Range().Id("u").Dot(additionalPropertiesField)).Block(
							If(List(Id("_"), Id("ok")).Op(":=").Id("m").Index(Id("k")), Op("!").Id("ok")).Block(Id("m").Index(Id("k")).Op("=").Id("raw")),
						)
					}
					gg.Return(Qual("encoding/json", "Marshal").Call(Id("m")))
				}
			})
		}
		if len(wrapperFields) > 0 {
			g.Return(Nil(), Qual("errors", "New").Call(Lit(name+" has no variant set")))
		} else {
			g.Return(Index().Byte().Values(), Nil())
		}
	})
	f.Line()

	if exactlyOne || validate {
		f.Func().Params(Id("u").Op("*").Id(name)).Id("Validate").Params().Params(Error()).BlockFunc(func(g *Group) {
			if exactlyOne {
				g.Var().Id("count").Int()
				for _, vi := range variants {
					g.If(Id("u").Dot(vi.fieldName).Op("!=").Nil()).Block(Id("count").Op("++"))
				}
				g.If(Id("count").Op("!=").Lit(1)).Block(
					Return(Qual("errors", "New").Call(Lit(name + " must have exactly one variant set"))),
				)
			}
			for _, field := range wrapperFields {
				if field.Required && field.Def.Const == nil {
					if check := requiredCheck(field.Prop, field.Def, "u"); check != nil {
						g.Add(check)
					}
				}
			}
			for _, vi := range variants {
				if len(vi.sharedChecks) > 0 {
					g.If(Id("u").Dot(vi.fieldName).Op("!=").Nil()).Block(vi.sharedChecks...)
				}
			}
			g.Return(Nil())
		})
		f.Line()
	}
	return recorded
}
