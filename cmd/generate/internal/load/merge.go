package load

import (
	"fmt"
	"reflect"
	"strings"
)

// MergeStableAndUnstable combines stable and unstable schema inputs without mutating either.
//
// The merged output keeps all stable definitions and method maps unchanged, while adding:
//   - Unstable-only methods (present only in unstable meta)
//   - Unstable-only $defs copied from the unstable schema under an "Unstable" prefix
//
// Unstable-only RPC root types (Request/Response/Notification) are always copied with the
// Unstable prefix to avoid changing the stable API surface.
func MergeStableAndUnstable(stableMeta *Meta, stableSchema *Schema, unstableMeta *Meta, unstableSchema *Schema) (*Meta, *Schema, error) {
	if stableMeta == nil {
		return nil, nil, fmt.Errorf("stable meta is nil")
	}
	if stableSchema == nil {
		return nil, nil, fmt.Errorf("stable schema is nil")
	}
	if unstableMeta == nil {
		return nil, nil, fmt.Errorf("unstable meta is nil")
	}
	if unstableSchema == nil {
		return nil, nil, fmt.Errorf("unstable schema is nil")
	}

	// Defensive: schema versions should match.
	if stableMeta.Version != 0 && unstableMeta.Version != 0 && stableMeta.Version != unstableMeta.Version {
		return nil, nil, fmt.Errorf("stable meta version (%d) differs from unstable meta version (%d)", stableMeta.Version, unstableMeta.Version)
	}

	stableWires := map[string]struct{}{}
	addWireMethods(stableWires, stableMeta.AgentMethods)
	addWireMethods(stableWires, stableMeta.ClientMethods)
	addWireMethods(stableWires, stableMeta.ProtocolMethods)

	combinedMeta := &Meta{
		Version:         stableMeta.Version,
		AgentMethods:    cloneStringMap(stableMeta.AgentMethods),
		ClientMethods:   cloneStringMap(stableMeta.ClientMethods),
		ProtocolMethods: cloneStringMap(stableMeta.ProtocolMethods),
	}

	unstableOnlyWires := map[string]struct{}{}
	if err := mergeUnstableOnlyMethods(&combinedMeta.AgentMethods, unstableMeta.AgentMethods, stableWires, unstableOnlyWires); err != nil {
		return nil, nil, err
	}
	if err := mergeUnstableOnlyMethods(&combinedMeta.ClientMethods, unstableMeta.ClientMethods, stableWires, unstableOnlyWires); err != nil {
		return nil, nil, err
	}
	if err := mergeUnstableOnlyMethods(&combinedMeta.ProtocolMethods, unstableMeta.ProtocolMethods, stableWires, unstableOnlyWires); err != nil {
		return nil, nil, err
	}

	dupSet, err := buildUnstableDuplicateSet(stableSchema, unstableSchema, unstableOnlyWires)
	if err != nil {
		return nil, nil, err
	}

	dupMap := make(map[string]string, len(dupSet))
	for name := range dupSet {
		dupMap[name] = "Unstable" + name
	}

	combinedSchema := &Schema{Defs: make(map[string]*Definition, len(stableSchema.Defs)+len(dupSet))}
	for name, def := range stableSchema.Defs {
		combinedSchema.Defs[name] = def
	}

	for oldName, newName := range dupMap {
		if _, exists := combinedSchema.Defs[newName]; exists {
			return nil, nil, fmt.Errorf("cannot merge unstable schema: %q already exists in stable defs", newName)
		}
		unstableDef := unstableSchema.Defs[oldName]
		if unstableDef == nil {
			return nil, nil, fmt.Errorf("cannot merge unstable schema: missing definition %q", oldName)
		}
		copyDef := deepCopyDefinition(unstableDef)
		rewriteDefinitionRefs(copyDef, dupMap)
		// Prevent unstable copies from being treated as stable RPC root types.
		if copyDef.XMethod != "" {
			if _, ok := stableWires[copyDef.XMethod]; ok {
				copyDef.XMethod = ""
				copyDef.XSide = ""
			}
		}
		combinedSchema.Defs[newName] = copyDef
	}

	return combinedMeta, combinedSchema, nil
}

func addWireMethods(dst map[string]struct{}, methods map[string]string) {
	for _, wire := range methods {
		if wire == "" {
			continue
		}
		dst[wire] = struct{}{}
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeUnstableOnlyMethods(dst *map[string]string, src map[string]string, stableWires map[string]struct{}, unstableOnlyWires map[string]struct{}) error {
	if len(src) == 0 {
		return nil
	}
	if *dst == nil {
		*dst = make(map[string]string)
	}
	for key, wire := range src {
		if wire == "" {
			continue
		}
		if _, ok := stableWires[wire]; ok {
			// Stable already contains this wire method; don't change stable mapping.
			continue
		}
		if existing, ok := (*dst)[key]; ok && existing != wire {
			return fmt.Errorf("cannot merge unstable meta: method key %q maps to both %q and %q", key, existing, wire)
		}
		(*dst)[key] = wire
		unstableOnlyWires[wire] = struct{}{}
	}
	return nil
}

func buildUnstableDuplicateSet(stableSchema *Schema, unstableSchema *Schema, unstableOnlyWires map[string]struct{}) (map[string]struct{}, error) {
	if stableSchema == nil {
		return nil, fmt.Errorf("stable schema is nil")
	}
	if unstableSchema == nil {
		return nil, fmt.Errorf("unstable schema is nil")
	}

	// newOrChanged tracks types that are new in unstable, or differ from stable.
	newOrChanged := map[string]bool{}
	for name, udef := range unstableSchema.Defs {
		sdef := stableSchema.Defs[name]
		if sdef == nil || !reflect.DeepEqual(sdef, udef) {
			newOrChanged[name] = true
		}
	}

	dupSet := map[string]struct{}{}
	queue := []string{}

	// Seed with request/response/notification types for unstable-only methods.
	for name, def := range unstableSchema.Defs {
		if def == nil {
			continue
		}
		if def.XMethod == "" || def.XSide == "" {
			continue
		}
		if _, ok := unstableOnlyWires[def.XMethod]; !ok {
			continue
		}
		if !strings.HasSuffix(name, "Request") && !strings.HasSuffix(name, "Response") && !strings.HasSuffix(name, "Notification") {
			continue
		}
		if _, ok := dupSet[name]; ok {
			continue
		}
		dupSet[name] = struct{}{}
		queue = append(queue, name)
	}

	// Expand transitive closure across $ref edges, but only for types that are new/changed.
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		def := unstableSchema.Defs[name]
		if def == nil {
			return nil, fmt.Errorf("unstable schema missing definition %q", name)
		}
		refs := collectDefinitionRefs(def)
		for refName := range refs {
			if !newOrChanged[refName] {
				continue
			}
			if _, ok := dupSet[refName]; ok {
				continue
			}
			dupSet[refName] = struct{}{}
			queue = append(queue, refName)
		}
	}

	return dupSet, nil
}

func collectDefinitionRefs(root *Definition) map[string]struct{} {
	refs := map[string]struct{}{}
	visitDefinition(root, func(d *Definition) {
		if d == nil {
			return
		}
		if strings.HasPrefix(d.Ref, "#/$defs/") {
			name := strings.TrimPrefix(d.Ref, "#/$defs/")
			if name != "" {
				refs[name] = struct{}{}
			}
		}
	})
	return refs
}

func rewriteDefinitionRefs(root *Definition, dupMap map[string]string) {
	visitDefinition(root, func(d *Definition) {
		if d == nil {
			return
		}
		if !strings.HasPrefix(d.Ref, "#/$defs/") {
			return
		}
		name := strings.TrimPrefix(d.Ref, "#/$defs/")
		if name == "" {
			return
		}
		if newName, ok := dupMap[name]; ok {
			d.Ref = "#/$defs/" + newName
		}
	})
}

func visitDefinition(root *Definition, fn func(*Definition)) {
	seen := map[*Definition]struct{}{}
	var walk func(*Definition)
	walk = func(d *Definition) {
		if d == nil {
			return
		}
		if _, ok := seen[d]; ok {
			return
		}
		seen[d] = struct{}{}
		fn(d)
		if d.Items != nil {
			walk(d.Items)
		}
		for _, v := range d.Properties {
			walk(v)
		}
		for _, v := range d.AnyOf {
			walk(v)
		}
		for _, v := range d.OneOf {
			walk(v)
		}
		for _, v := range d.AllOf {
			walk(v)
		}
	}
	walk(root)
}

func deepCopyDefinition(d *Definition) *Definition {
	if d == nil {
		return nil
	}
	copyDef := *d

	if d.boolSchema != nil {
		v := *d.boolSchema
		copyDef.boolSchema = &v
	}
	if d.Discriminator != nil {
		copyDef.Discriminator = &Discriminator{PropertyName: d.Discriminator.PropertyName}
	}
	if d.Required != nil {
		copyDef.Required = append([]string(nil), d.Required...)
	}
	if d.Enum != nil {
		copyDef.Enum = append([]any(nil), d.Enum...)
	}
	if d.Properties != nil {
		copyDef.Properties = make(map[string]*Definition, len(d.Properties))
		for k, v := range d.Properties {
			copyDef.Properties[k] = deepCopyDefinition(v)
		}
	}
	copyDef.Items = deepCopyDefinition(d.Items)
	if d.AnyOf != nil {
		copyDef.AnyOf = make([]*Definition, len(d.AnyOf))
		for i, v := range d.AnyOf {
			copyDef.AnyOf[i] = deepCopyDefinition(v)
		}
	}
	if d.OneOf != nil {
		copyDef.OneOf = make([]*Definition, len(d.OneOf))
		for i, v := range d.OneOf {
			copyDef.OneOf[i] = deepCopyDefinition(v)
		}
	}
	if d.AllOf != nil {
		copyDef.AllOf = make([]*Definition, len(d.AllOf))
		for i, v := range d.AllOf {
			copyDef.AllOf[i] = deepCopyDefinition(v)
		}
	}

	return &copyDef
}
