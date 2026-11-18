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
	// changedOnly and patchedObject match the description of an object whose
	// optional properties are all patch fields. changedOnly says only that
	// the object carries the changed fields, as ToolCallUpdate ("only changed
	// fields need to be included") and SessionInfoUpdate ("optional to
	// support partial updates") do; patchedObject says that they are patch
	// fields, as SubagentUpdate ("Omitted patch fields keep their previous
	// values") does.
	changedOnly   = regexp.MustCompile(`(?i)only changed fields|partial updates?\b`)
	patchedObject = regexp.MustCompile(`(?i)omitted patch fields`)
	// patchSemantics matches the sentence of an object description that names
	// its patch fields, as CompactionUpdate does: "`summary`, `error`, and
	// `_meta` have patch semantics".
	patchSemantics = regexp.MustCompile("(?i)[^.]*\\bhave patch semantics")
	backticked     = regexp.MustCompile("`([^`]+)`")
	// patchProperty matches the description of a property that is a patch
	// field on its own: "Omitted means unchanged. `null` clears it.", "Set to
	// null to clear."
	patchProperty = regexp.MustCompile("(?i)omitt(?:ed|ing)[^.]*\\bunchanged\\b|\\bnull`? (?:clears|removes)\\b|set to null to clear")
	// mentionsNull matches a property description that says what null does.
	mentionsNull = regexp.MustCompile(`(?i)\bnull\b`)
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
		allOptional := changedOnly.MatchString(def.Description) || patchedObject.MatchString(def.Description)
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

// NullAsAbsent returns the patch fields of schema for which the schema
// defines no clear: the optional properties of an object that says only that
// it carries the changed fields (changedOnly, and neither patchedObject nor a
// "have patch semantics" sentence), whose own description does not say what
// null does. ACP v1 peers, the Rust SDK among them, read null in these fields
// as absent: all of ToolCallUpdate except name, and _meta of
// SessionInfoUpdate.
func NullAsAbsent(schema *load.Schema) map[*load.Definition]bool {
	out := map[*load.Definition]bool{}
	for _, def := range schema.Defs {
		if def == nil || !changedOnly.MatchString(def.Description) ||
			patchedObject.MatchString(def.Description) || patchSemantics.MatchString(def.Description) {
			continue
		}
		for name, prop := range def.Properties {
			if prop == nil || slices.Contains(def.Required, name) || mentionsNull.MatchString(prop.Description) {
				continue
			}
			out[prop] = true
		}
	}
	return out
}
