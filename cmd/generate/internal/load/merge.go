package load

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// MergeStableAndUnstable combines stable and unstable schema inputs without mutating either.
//
// The merged output has one definition per name, so that a shape that stable
// and unstable methods share has one Go type:
//   - A definition that both schemas have is the unstable version, which
//     carries the unstable additions. Each property and each alternative that
//     only the unstable version has is marked **UNSTABLE** in its description,
//     unless the schema marks it already.
//   - A definition that only the unstable schema has, and that a stable method
//     or a definition above reaches, keeps its name.
//   - Unstable-only methods (present only in unstable meta) are added. Their
//     Request/Response/Notification types, and the definitions that only they
//     reach, are copied under an "Unstable" prefix.
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

	combinedSchema := &Schema{Defs: make(map[string]*Definition, len(stableSchema.Defs))}
	for name, def := range stableSchema.Defs {
		combinedSchema.Defs[name] = def
	}

	wires := map[string]struct{}{}
	for _, set := range []map[string]struct{}{stableWires, unstableOnlyWires} {
		for wire := range set {
			wires[wire] = struct{}{}
		}
	}
	promotedSharedDefs := promoteChangedSharedDefs(combinedSchema, stableSchema, unstableSchema, stableWires, wires)
	ensureRefsAvailable(combinedSchema, unstableSchema, promotedSharedDefs)

	dupSet, err := buildUnstableDuplicateSet(combinedSchema, unstableSchema, unstableOnlyWires)
	if err != nil {
		return nil, nil, err
	}

	dupMap := make(map[string]string, len(dupSet))
	for name := range dupSet {
		dupMap[name] = "Unstable" + name
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

// buildUnstableDuplicateSet returns the definitions that get an "Unstable"
// copy: the Request, Response, and Notification types of the unstable-only
// methods, and each definition that they reach and that the combined schema
// does not have. A definition that the combined schema has is shared: the
// copies refer to it by its own name.
func buildUnstableDuplicateSet(combinedSchema *Schema, unstableSchema *Schema, unstableOnlyWires map[string]struct{}) (map[string]struct{}, error) {
	if combinedSchema == nil {
		return nil, fmt.Errorf("combined schema is nil")
	}
	if unstableSchema == nil {
		return nil, fmt.Errorf("unstable schema is nil")
	}

	dupSet := map[string]struct{}{}
	queue := []string{}
	for name, def := range unstableSchema.Defs {
		if def == nil || def.XMethod == "" || def.XSide == "" {
			continue
		}
		if _, ok := unstableOnlyWires[def.XMethod]; !ok {
			continue
		}
		if !strings.HasSuffix(name, "Request") && !strings.HasSuffix(name, "Response") && !strings.HasSuffix(name, "Notification") {
			continue
		}
		dupSet[name] = struct{}{}
		queue = append(queue, name)
	}

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		def := unstableSchema.Defs[name]
		if def == nil {
			return nil, fmt.Errorf("unstable schema missing definition %q", name)
		}
		for refName := range collectDefinitionRefs(def) {
			if _, ok := unstableSchema.Defs[refName]; !ok {
				continue
			}
			if _, shared := combinedSchema.Defs[refName]; shared {
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

// promoteChangedSharedDefs writes into the combined schema the unstable
// version of each definition that both schemas have, that differs in shape,
// and that the Request, Response, or Notification of a method in wires (the
// methods of the combined metadata) reaches through unstable $refs, and each
// definition that only the unstable schema has and that a stable method
// reaches. It returns their names. A definition with x-method whose method
// the metadata does not list, such as proxy/initialize, gets no Go method, so
// it promotes nothing. The unions of every request, response, or
// notification of a side (AgentRequest and the like) are no method's types,
// so they keep their stable version.
func promoteChangedSharedDefs(combinedSchema *Schema, stableSchema *Schema, unstableSchema *Schema, stableWires, wires map[string]struct{}) map[string]struct{} {
	promoted := map[string]struct{}{}
	var methodRoots []string
	for name, udef := range unstableSchema.Defs {
		if udef == nil || udef.XMethod == "" || udef.XSide == "" {
			continue
		}
		if _, ok := wires[udef.XMethod]; !ok {
			continue
		}
		if strings.HasSuffix(name, "Request") || strings.HasSuffix(name, "Response") || strings.HasSuffix(name, "Notification") {
			methodRoots = append(methodRoots, name)
		}
	}
	for name := range reachableDefs(unstableSchema, methodRoots) {
		udef, sdef := unstableSchema.Defs[name], stableSchema.Defs[name]
		if udef == nil || sdef == nil || sameShape(sdef, udef) {
			continue
		}
		copyDef := deepCopyDefinition(udef)
		markUnstableAdditions(sdef, copyDef, unstableSchema.Defs)
		combinedSchema.Defs[name] = copyDef
		promoted[name] = struct{}{}
	}

	queue := []string{}
	seen := map[string]struct{}{}
	for name, sdef := range stableSchema.Defs {
		if sdef == nil || sdef.XMethod == "" || sdef.XSide == "" {
			continue
		}
		if _, ok := stableWires[sdef.XMethod]; !ok {
			continue
		}
		if !strings.HasSuffix(name, "Request") && !strings.HasSuffix(name, "Response") && !strings.HasSuffix(name, "Notification") {
			continue
		}
		queue = append(queue, name)
		seen[name] = struct{}{}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		udef := unstableSchema.Defs[name]
		if udef == nil {
			continue
		}
		if stableSchema.Defs[name] == nil {
			combinedSchema.Defs[name] = deepCopyDefinition(udef)
			promoted[name] = struct{}{}
		}
		for refName := range collectDefinitionRefs(udef) {
			if _, ok := seen[refName]; ok {
				continue
			}
			if unstableSchema.Defs[refName] == nil {
				continue
			}
			seen[refName] = struct{}{}
			queue = append(queue, refName)
		}
	}

	return promoted
}

// reachableDefs returns the definitions of schema that roots reach through
// $refs, roots included.
func reachableDefs(schema *Schema, roots []string) map[string]struct{} {
	seen := map[string]struct{}{}
	queue := append([]string(nil), roots...)
	for _, name := range roots {
		seen[name] = struct{}{}
	}
	for len(queue) > 0 {
		def := schema.Defs[queue[0]]
		queue = queue[1:]
		if def == nil {
			continue
		}
		for refName := range collectDefinitionRefs(def) {
			if _, ok := seen[refName]; !ok && schema.Defs[refName] != nil {
				seen[refName] = struct{}{}
				queue = append(queue, refName)
			}
		}
	}
	return seen
}

// unstableNote is the text that the unstable schema puts in front of the
// description of each unstable addition.
const unstableNote = "**UNSTABLE**\n\nThis capability is not part of the spec yet, and may be removed or changed at any point."

// markUnstableAdditions marks in u, the unstable version of the stable
// definition s, each property, alternative, allOf part, and enum value that s
// does not have, at any depth, so that its doc comment says that it is
// unstable. A property, alternative, or allOf part that both have is compared
// in turn, and so are their items and additionalProperties. An added enum
// value gets the note in u.EnumNotes, since the schema has no place for its
// text. defs resolves the $refs of the unstable schema (markAddedPart).
func markUnstableAdditions(s, u *Definition, defs map[string]*Definition) {
	if s == nil || u == nil {
		return
	}
	for name, prop := range u.Properties {
		if sprop, ok := s.Properties[name]; ok {
			markUnstableAdditions(sprop, prop, defs)
		} else {
			markUnstable(prop)
		}
	}
	for _, alts := range [][2][]*Definition{{s.AnyOf, u.AnyOf}, {s.OneOf, u.OneOf}} {
		for i, salt := range counterparts(alts[0], alts[1]) {
			if salt != nil {
				markUnstableAdditions(salt, alts[1][i], defs)
			} else {
				markUnstable(alts[1][i])
			}
		}
	}
	markUnstableAdditions(s.Items, u.Items, defs)
	markUnstableAdditions(s.AdditionalProperties.Schema, u.AdditionalProperties.Schema, defs)
	for i, spart := range counterparts(s.AllOf, u.AllOf) {
		if spart != nil {
			markUnstableAdditions(spart, u.AllOf[i], defs)
		} else {
			u.AllOf[i] = markAddedPart(u.AllOf[i], defs, map[string]bool{})
		}
	}
	values := map[string]bool{}
	for _, v := range s.Enum {
		values[fmt.Sprint(v)] = true
	}
	for _, v := range u.Enum {
		if value := fmt.Sprint(v); !values[value] {
			if u.EnumNotes == nil {
				u.EnumNotes = map[string]string{}
			}
			u.EnumNotes[value] = unstableNote
		}
	}
}

// markAddedPart marks each property that part, an allOf part that only the
// unstable version has, brings into the struct that flattens it, and returns
// the part. A part that refers to a definition becomes an inline copy of it,
// so that the note does not reach the other users of that definition; seen
// stops a cycle of such references.
func markAddedPart(part *Definition, defs map[string]*Definition, seen map[string]bool) *Definition {
	if part == nil {
		return nil
	}
	if name := strings.TrimPrefix(part.Ref, "#/$defs/"); part.Ref != "" {
		if seen[name] || defs[name] == nil {
			return part
		}
		seen[name] = true
		part = deepCopyDefinition(defs[name])
	}
	for _, prop := range part.Properties {
		markUnstable(prop)
	}
	for i, sub := range part.AllOf {
		part.AllOf[i] = markAddedPart(sub, defs, seen)
	}
	return part
}

// markUnstable marks d, an addition of the unstable schema, and everything
// inside it: its properties, alternatives, inline allOf parts, items, and
// enum values, which become Go types, fields, and constants too. A $ref is
// not followed. A const inside d, such as the discriminator of a variant,
// says no more than d does, so it is left as it is.
func markUnstable(d *Definition) {
	visitDefinition(d, func(n *Definition) {
		if n != d && n.Const != nil {
			return
		}
		if !strings.Contains(n.Description, "UNSTABLE") {
			n.Description = strings.TrimSpace(unstableNote + "\n\n" + n.Description)
		}
		for _, v := range n.Enum {
			if n.EnumNotes == nil {
				n.EnumNotes = map[string]string{}
			}
			n.EnumNotes[fmt.Sprint(v)] = unstableNote
		}
	})
}

// counterparts returns, for each alternative or allOf part in us, the one in
// ss that is its stable version, or nil for one that only the unstable schema
// has. A part with an identity (alternativeKey) matches the part with the
// same identity. A part without one matches a part of equal shape, else the
// part without an identity whose properties it has, all of them and the most
// of them, so that a property added to an anonymous alternative counts as an
// addition to that alternative.
func counterparts(ss, us []*Definition) []*Definition {
	out := make([]*Definition, len(us))
	used := map[*Definition]bool{}
	byKey := map[string]*Definition{}
	for _, s := range ss {
		key, _ := alternativeKey(s)
		byKey[key] = s
	}
	for i, u := range us {
		key, _ := alternativeKey(u)
		if s := byKey[key]; s != nil && !used[s] {
			out[i], used[s] = s, true
		}
	}
	for i, u := range us {
		if _, identity := alternativeKey(u); out[i] != nil || identity || u == nil {
			continue
		}
		var best *Definition
		for _, s := range ss {
			if _, identity := alternativeKey(s); used[s] || identity || s == nil ||
				fmt.Sprint(s.Type) != fmt.Sprint(u.Type) || !hasAllProperties(u, s) {
				continue
			}
			if best == nil || len(s.Properties) > len(best.Properties) {
				best = s
			}
		}
		if best != nil {
			out[i], used[best] = best, true
		}
	}
	return out
}

// hasAllProperties reports whether u has every property that s has.
func hasAllProperties(u, s *Definition) bool {
	for name := range s.Properties {
		if _, ok := u.Properties[name]; !ok {
			return false
		}
	}
	return true
}

// alternativeKey names an alternative of a union across schema versions: by
// its title, its const, its $ref, the const of one of its properties (the
// discriminator), or the $ref of an allOf part, which identify it, else by
// its whole shape. identity reports which.
func alternativeKey(d *Definition) (key string, identity bool) {
	switch {
	case d == nil:
		return "", false
	case d.Title != "":
		return "title:" + d.Title, true
	case d.Const != nil:
		return fmt.Sprintf("const:%v", d.Const), true
	case d.Ref != "":
		return "ref:" + d.Ref, true
	}
	names := make([]string, 0, len(d.Properties))
	for name := range d.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if p := d.Properties[name]; p != nil && p.Const != nil {
			return fmt.Sprintf("prop:%s=%v", name, p.Const), true
		}
	}
	for _, part := range d.AllOf {
		if part != nil && part.Ref != "" {
			return "ref:" + part.Ref, true
		}
	}
	b, _ := json.Marshal(withoutDescriptions(d))
	return string(b), false
}

// sameShape reports whether two definitions describe the same JSON, that is
// whether they are equal but for their descriptions. From schema 1.27.0 on,
// the unstable schema's descriptions link to the draft docs, so most of its
// definitions differ from their stable twins in text only.
func sameShape(a, b *Definition) bool {
	return reflect.DeepEqual(withoutDescriptions(a), withoutDescriptions(b))
}

func withoutDescriptions(d *Definition) *Definition {
	c := deepCopyDefinition(d)
	visitDefinition(c, func(n *Definition) { n.Description, n.EnumNotes = "", nil })
	return c
}

func ensureRefsAvailable(combinedSchema *Schema, unstableSchema *Schema, roots map[string]struct{}) {
	if len(roots) == 0 {
		return
	}
	queue := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for name := range roots {
		queue = append(queue, name)
		seen[name] = struct{}{}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		def := combinedSchema.Defs[name]
		if def == nil {
			def = unstableSchema.Defs[name]
		}
		if def == nil {
			continue
		}
		refs := collectDefinitionRefs(def)
		for refName := range refs {
			if _, ok := combinedSchema.Defs[refName]; !ok {
				if uref := unstableSchema.Defs[refName]; uref != nil {
					combinedSchema.Defs[refName] = deepCopyDefinition(uref)
				}
			}
			if _, ok := seen[refName]; !ok {
				seen[refName] = struct{}{}
				queue = append(queue, refName)
			}
		}
	}
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
		walk(d.Items)
		walk(d.AdditionalProperties.Schema)
		walk(d.Not)
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
	if d.EnumNotes != nil {
		copyDef.EnumNotes = make(map[string]string, len(d.EnumNotes))
		for k, v := range d.EnumNotes {
			copyDef.EnumNotes[k] = v
		}
	}
	if d.Properties != nil {
		copyDef.Properties = make(map[string]*Definition, len(d.Properties))
		for k, v := range d.Properties {
			copyDef.Properties[k] = deepCopyDefinition(v)
		}
	}
	copyDef.Items = deepCopyDefinition(d.Items)
	copyDef.AdditionalProperties.Schema = deepCopyDefinition(d.AdditionalProperties.Schema)
	copyDef.Not = deepCopyDefinition(d.Not)
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
