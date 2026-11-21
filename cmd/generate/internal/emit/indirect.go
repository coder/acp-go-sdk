package emit

import (
	"github.com/coder/acp-go-sdk/cmd/generate/internal/ir"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// indirectMembers returns the members that hold their value through a
// pointer, keyed by the property's definition, as ir.PatchFields is: each
// member that holds its value in place, as a $defs entry whose Go type holds
// the member's own struct in place again, through any chain of such members.
// Go rejects such a struct as an invalid recursive type, while JSON Schema
// allows it: a property applies its schema to a child instance (2020-12,
// section 10.3.2.1), so a tree is a valid schema. A member gets the pointer
// whether its schema is a $ref or an allOf of one, because the check reads
// the Go type that the member is declared with (inPlaceType).
//
// Every member on such a cycle gets a pointer, so the result does not depend
// on the order of the $defs entries. A patch field gets none: Patch holds its
// value in place, so a cycle of patch fields alone still fails to compile.
func (b *typesBuilder) indirectMembers() map[*load.Definition]bool {
	type member struct {
		prop  *load.Definition
		holds string // the $defs entry that the member holds in place
	}
	held := map[string][]member{}
	for name, def := range b.schema.Defs {
		for _, field := range b.inPlaceFields(def) {
			typ := inPlaceType(field)
			if b.patch[field.Def] {
				typ = patchValueType(field)
			}
			if via, leaf := peelContainers(typ); len(via) == 0 && b.schema.Defs[leaf] != nil {
				held[name] = append(held[name], member{field.Def, leaf})
			}
		}
	}
	// reaches reports whether the Go type of the $defs entry from holds the
	// one of to in place.
	reaches := func(from, to string) bool {
		seen := map[string]bool{}
		for stack := []string{from}; len(stack) > 0; {
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if name == to {
				return true
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			for _, m := range held[name] {
				stack = append(stack, m.holds)
			}
		}
		return false
	}
	indirect := map[*load.Definition]bool{}
	for owner, members := range held {
		for _, m := range members {
			if !b.patch[m.prop] && reaches(m.holds, owner) {
				indirect[m.prop] = true
			}
		}
	}
	return indirect
}

// inPlaceFields returns the members that the Go type of def, a $defs entry,
// holds in place: those of its struct and of the structs of its inline object
// properties (emitNestedStructs), or the own properties of a union wrapper
// (emitUnion). A union wrapper holds each variant through a pointer.
func (b *typesBuilder) inPlaceFields(def *load.Definition) []ir.Field {
	if def == nil {
		return nil
	}
	switch classify(def) {
	case structKind:
		var fields []ir.Field
		for _, field := range ir.Fields(def.Properties, def.Required) {
			if inlineStruct(field.Def) {
				fields = append(fields, ir.Fields(field.Def.Properties, field.Def.Required)...)
			} else {
				fields = append(fields, field)
			}
		}
		return fields
	case unionKind:
		alts := def.AnyOf
		if len(alts) == 0 {
			alts = def.OneOf
		}
		if keepsOwnProperties(def, unionAlternatives(b.schema, alts)) {
			return ir.Fields(def.Properties, def.Required)
		}
	}
	return nil
}

// keepsOwnProperties reports whether the wrapper of a union with alternatives
// alts declares the union's own properties as its fields. When every
// alternative is an inline object, each variant struct declares them instead.
func keepsOwnProperties(union *load.Definition, alts []unionAlternative) bool {
	return union != nil && len(union.Properties) > 0 && !allInlineObjects(alts)
}
