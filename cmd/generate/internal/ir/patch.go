package ir

import (
	"regexp"
	"slices"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// A patch field is an optional property of an update that changes a value
// the receiver stores: absent leaves the value as it is, null clears it, and
// a value replaces it. The schema says so only in its text, so these
// patterns read the descriptions.
var (
	// patchType matches the description of an object whose optional
	// properties are all patch fields, such as ToolCallUpdate ("only changed
	// fields need to be included"), SessionInfoUpdate ("optional to support
	// partial updates"), and SubagentUpdate ("Omitted patch fields keep their
	// previous values").
	patchType = regexp.MustCompile(`(?i)only changed fields|partial updates?\b|omitted patch fields`)
	// patchSemantics matches the sentence of an object description that names
	// its patch fields, as CompactionUpdate does: "`summary`, `error`, and
	// `_meta` have patch semantics".
	patchSemantics = regexp.MustCompile("(?i)[^.]*\\bhave patch semantics")
	backticked     = regexp.MustCompile("`([^`]+)`")
	// patchProperty matches the description of a property that is a patch
	// field on its own: "Omitted means unchanged. `null` clears it.", "Set to
	// null to clear."
	patchProperty = regexp.MustCompile("(?i)omitt(?:ed|ing)[^.]*\\bunchanged\\b|\\bnull`? (?:clears|removes)\\b|set to null to clear")
)

// PatchFields returns the patch fields of the object definitions in schema,
// keyed by the property's definition. An object variant of a union that takes
// its properties from a definition through allOf shares those property
// definitions, so the set covers it too.
func PatchFields(schema *load.Schema) map[*load.Definition]bool {
	patch := map[*load.Definition]bool{}
	for _, def := range schema.Defs {
		if def == nil || len(def.Properties) == 0 {
			continue
		}
		named := map[string]bool{}
		for _, sentence := range patchSemantics.FindAllString(def.Description, -1) {
			for _, m := range backticked.FindAllStringSubmatch(sentence, -1) {
				named[m[1]] = true
			}
		}
		allOptional := patchType.MatchString(def.Description)
		for name, prop := range def.Properties {
			if prop == nil || slices.Contains(def.Required, name) {
				continue
			}
			if allOptional || named[name] || patchProperty.MatchString(prop.Description) {
				patch[prop] = true
			}
		}
	}
	return patch
}
