package ir

import (
	"fmt"
	"sort"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/util"
)

// Lenient is the decode leniency that the schema's x-deserialize-* hints ask
// of one property. The zero value is strict: a bad value fails the message.
type Lenient struct {
	// DefaultOnError reads a value of the wrong shape as the property's
	// default (x-deserialize-default-on-error).
	DefaultOnError bool
	// DefaultOnNull reads null as the property's default
	// (x-deserialize-default-on-null).
	DefaultOnNull bool
	// SkipInvalidItems drops the array items of the wrong shape and keeps the
	// rest (x-deserialize-skip-invalid-items).
	SkipInvalidItems bool
}

// Field is one property of a schema object, as the generator emits it in a Go
// struct.
type Field struct {
	Prop     string           // property name on the wire
	Name     string           // exported Go field name
	Def      *load.Definition // the property's schema
	Required bool
	Lenient  Lenient
}

// Fields returns one Field per property of an object schema, sorted by
// property name. An optional property whose type is "null" alone gets none: it
// can hold no value. A required one keeps its field, so that the struct writes
// the member. It panics when a property that is not an array has
// x-deserialize-skip-invalid-items.
func Fields(props map[string]*load.Definition, required []string) []Field {
	isRequired := make(map[string]bool, len(required))
	for _, r := range required {
		isRequired[r] = true
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	fields := make([]Field, 0, len(names))
	for _, name := range names {
		def := props[name]
		if t, ok := def.Type.(string); ok && t == "null" && !isRequired[name] {
			continue
		}
		if def.SkipInvalidItems && PrimaryType(def) != "array" {
			panic(fmt.Sprintf("property %q has x-deserialize-skip-invalid-items but is not an array", name))
		}
		fields = append(fields, Field{
			Prop:     name,
			Name:     util.ToExportedField(name),
			Def:      def,
			Required: isRequired[name],
			Lenient: Lenient{
				DefaultOnError:   def.DefaultOnError,
				DefaultOnNull:    def.DefaultOnNull,
				SkipInvalidItems: def.SkipInvalidItems,
			},
		})
	}
	return fields
}
