package load

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// RefLoop returns an error that names a chain of $defs entries that refers
// back to its first entry in place, such as "A -> B -> A" for
// {"A": {"$ref": "#/$defs/B"}, "B": {"anyOf": [{"$ref": "#/$defs/A"}]}}, and
// nil when the schema has none. $ref, allOf, anyOf, oneOf and not apply their
// schemas to the value itself (JSON Schema 2020-12, sections 8.2.3.1 and
// 10.2), so a walker that follows such a chain to decide whether a value fits
// never stops, and the spec leaves the meaning of such a schema undefined
// (section 9.4.1). A $ref under properties, items or additionalProperties
// applies to a member or an item (section 10.3), so a type that refers to
// itself through them, as a tree does, is no loop.
func (s *Schema) RefLoop() error {
	const (
		walking = 1
		done    = 2
	)
	state := map[string]int{}
	var chain []string
	var visit func(name string) []string
	visit = func(name string) []string {
		switch state[name] {
		case walking:
			return append(slices.Clone(chain[slices.Index(chain, name):]), name)
		case done:
			return nil
		}
		state[name] = walking
		chain = append(chain, name)
		for _, next := range inPlaceRefs(s.Defs[name]) {
			if loop := visit(next); loop != nil {
				return loop
			}
		}
		chain = chain[:len(chain)-1]
		state[name] = done
		return nil
	}
	names := make([]string, 0, len(s.Defs))
	for name := range s.Defs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if loop := visit(name); loop != nil {
			return fmt.Errorf("$ref loop: %s", strings.Join(loop, " -> "))
		}
	}
	return nil
}

// inPlaceRefs returns the $defs entries that d refers to in place: by its own
// $ref, or by the $ref of an allOf, anyOf, oneOf or not schema inside it at
// any depth.
func inPlaceRefs(d *Definition) []string {
	if d == nil {
		return nil
	}
	var refs []string
	if name, ok := strings.CutPrefix(d.Ref, "#/$defs/"); ok {
		refs = append(refs, name)
	}
	for _, sub := range append(append(append([]*Definition{d.Not}, d.AllOf...), d.AnyOf...), d.OneOf...) {
		refs = append(refs, inPlaceRefs(sub)...)
	}
	return refs
}
