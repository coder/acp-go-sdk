package ir

import (
	"sort"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
	"github.com/coder/acp-go-sdk/cmd/generate/internal/util"
)

// Field is one property of a schema object, as the generator emits it in a Go
// struct.
type Field struct {
	Prop     string           // property name on the wire
	Name     string           // exported Go field name
	Def      *load.Definition // the property's schema
	Required bool
}

// Fields returns one Field per property of an object schema, sorted by
// property name.
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
		fields = append(fields, Field{
			Prop:     name,
			Name:     util.ToExportedField(name),
			Def:      props[name],
			Required: isRequired[name],
		})
	}
	return fields
}
