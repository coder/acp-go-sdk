# load

## Overview

Package `load` reads the generator inputs from the schema directory into minimal Go structs: `version`, `meta.json`, `schema.json`, and the optional `meta.unstable.json` and `schema.unstable.json`.
`Definition` models only the JSON Schema fields the generator uses, and it accepts boolean schemas.
`MergeStableAndUnstable` builds a new combined `Meta` and `Schema` from the stable and unstable inputs, with one definition per name, so that a shape that stable and unstable methods share has one Go type. A shared definition that the unstable schema changes is written as its unstable version, with each unstable addition marked **UNSTABLE**. The unstable-only methods are added, with `Unstable`-prefixed copies of their types and of the definitions that only they reach.

## Interface

- `type Meta struct { Version int; AgentMethods, ClientMethods, ProtocolMethods map[string]string }`, decoded from `meta.json` [load.go:Meta]
- `type Schema struct { Defs map[string]*Definition }`, decoded from the `$defs` key [load.go:Schema]
- `type Discriminator struct { PropertyName string }` [load.go:Discriminator]
- `type Definition struct` with JSON-tagged fields `Description`, `Type`, `Properties`, `Required`, `Enum`, `Items`, `Ref`, `AnyOf`, `OneOf`, `AllOf`, `DocsIgnore` (`x-docs-ignore`), `Title`, `Const`, `XSide` (`x-side`), `XMethod` (`x-method`), `Format`, `Default`, `Discriminator`, `DefaultOnError` (`x-deserialize-default-on-error`), `SkipInvalidItems` (`x-deserialize-skip-invalid-items`), `DefaultOnNull` (`x-deserialize-default-on-null`), `AdditionalProperties` (a `Members`), `Not` (`not`, a `*Definition`), and `UnevaluatedProperties`, which is true only for the literal `"unevaluatedProperties": true` [load.go:Definition, isTrue] [load_test.go:TestDefinition_UnmarshalJSON_UnevaluatedPropertiesIsSetOnlyByTrue]
- `type Members struct { Open bool; Schema *Definition }`: the `additionalProperties` keyword. `Open` is set only by the literal `true`, and `Schema` only by an object, the schema of each member that `properties` does not name. `false` and any other value leave both unset [load.go:Members] [load_test.go:TestDefinition_UnmarshalJSON_ReadsAdditionalProperties]
- `func (m *Members) UnmarshalJSON(b []byte) error` [load.go:Members.UnmarshalJSON]
- `func (d *Definition) UnmarshalJSON(b []byte) error` decodes the object and boolean JSON Schema forms [load.go:Definition.UnmarshalJSON]
- `func ReadSchemaVersion(schemaDir string) (string, error)` reads `version`, the ACP schema release, without surrounding whitespace. An empty file is an error [load.go:ReadSchemaVersion] [load_test.go:TestReadSchemaVersion]
- `func ReadMeta(schemaDir string) (*Meta, error)` reads `meta.json` [load.go:ReadMeta]
- `func ReadSchema(schemaDir string) (*Schema, error)` reads `schema.json` [load.go:ReadSchema]
- `func ReadMetaUnstable(schemaDir string) (*Meta, bool, error)` reads `meta.unstable.json`. The `bool` reports whether the file exists [load.go:ReadMetaUnstable]
- `func ReadSchemaUnstable(schemaDir string) (*Schema, bool, error)` reads `schema.unstable.json`. The `bool` reports whether the file exists [load.go:ReadSchemaUnstable]
- `func MergeStableAndUnstable(stableMeta *Meta, stableSchema *Schema, unstableMeta *Meta, unstableSchema *Schema) (*Meta, *Schema, error)` returns the combined meta and schema [merge.go:MergeStableAndUnstable]

## Invariants

- `ReadMetaUnstable` and `ReadSchemaUnstable` return `(nil, false, nil)` when the file does not exist. A parse error returns `found == true` with the error [load.go:ReadMetaUnstable] [load.go:ReadSchemaUnstable]
- `Definition.UnmarshalJSON` resets a boolean schema (`true` or `false`) to the zero `Definition` and records the value in the unexported `boolSchema` field [load.go:Definition.UnmarshalJSON]
- `MergeStableAndUnstable` returns an error when any of its four arguments is nil [merge.go:MergeStableAndUnstable]
- It returns an error when the stable and unstable meta versions differ and both are nonzero [merge.go:MergeStableAndUnstable]
- An unstable method whose wire name already exists in the stable meta is skipped, so stable method mappings stay unchanged. When the unstable wire name is not a stable one, a method key that already maps to a different wire name returns an error. Empty wire names are ignored [merge.go:mergeUnstableOnlyMethods, addWireMethods]
- A definition that both schemas have, that differs in shape, and that the Request, Response, or Notification of any method of the combined metadata (stable or unstable) reaches through unstable `$ref`s is written under its own name as a copy of its unstable version. A definition with `x-method` whose method the metadata does not list, such as `proxy/initialize`, gets no Go method and promotes nothing. Each property, and each `anyOf` or `oneOf` alternative, that the stable version does not have gets the unstable schema's note (`**UNSTABLE**` ...) in front of its description, unless its description says `UNSTABLE` already, and so does everything inside it (its properties, alternatives, inline `allOf` parts, items, and enum values, but not a `const` such as a variant's discriminator), as those become Go types, fields, and constants too. This holds at any depth: a property, alternative, or `allOf` part that both versions have is compared in turn, and so are their `items` and `additionalProperties`. Alternatives and `allOf` parts are matched by title, const, `$ref`, the const of a property, or the `$ref` of an `allOf` part. One without any of these matches one of equal shape, else one without them whose properties it all has, the one with the most, so that a property added to an anonymous alternative is marked as an addition to it [merge.go:counterparts]. An `allOf` part that only the unstable version has gets the note on each property that it brings into the flattened struct; a part that refers to a definition becomes an inline copy of it, so that the definition's other users stay unmarked. An `enum` value that the stable version does not have gets the note in `Definition.EnumNotes`, which the emitter writes as its constant's doc comment, since the schema has no place for the text. The unions of every request, response, or notification of a side, such as `AgentRequest`, are no method's types, so they keep their stable version. Two definitions that differ only in descriptions count as the same: from schema 1.27.0 on, the unstable descriptions link to the draft docs [merge.go:promoteChangedSharedDefs, markUnstableAdditions, alternativeKey, sameShape] [merge_test.go:TestMergeStableAndUnstable_IgnoresDescriptionOnlyChanges, TestMergeStableAndUnstable_MarksUnstableAdditions, TestMergeStableAndUnstable_MarksNestedAndEnumAdditions, TestMergeStableAndUnstable_MarksTheWholeAddedSubtree, TestMergeStableAndUnstable_IgnoresRootsThatTheMetadataDoesNotList]
- Request, Response, and Notification definitions of unstable-only methods are copied when they have both `x-method` and `x-side`, and so is each definition that they reach and that the combined schema does not have. A definition that the combined schema has is shared and never copied [merge.go:buildUnstableDuplicateSet]
- Copies are named `"Unstable" + name`. In a copy, only `$ref` values whose targets are also copied are rewritten to the prefixed names; a reference to a shared definition keeps its name. The merge fails if a prefixed name already exists in the combined defs [merge.go:MergeStableAndUnstable, rewriteDefinitionRefs]
- A copy whose `XMethod` names a stable wire method has `XMethod` and `XSide` cleared [merge.go:MergeStableAndUnstable]
- Definitions that only the unstable schema has and that stable Request, Response, and Notification roots reach through unstable `$ref`s are written into the combined schema under their own names. The missing `$ref` targets of every written definition are then copied from the unstable schema under their own names [merge.go:promoteChangedSharedDefs, ensureRefsAvailable]
- The merge does not change either input. Unstable definitions are copied as a tree (properties, items, the `additionalProperties` schema, `not`, and the other sub-schemas) before refs are rewritten. The ref scans and rewrites walk the same nodes. The `Type`, `Const`, `Default`, and `Enum` element values stay shared with the input [merge.go:MergeStableAndUnstable, deepCopyDefinition]
- An empty method map in the stable meta stays nil in the combined meta only when the matching unstable map is empty too. A non-empty unstable map makes `mergeUnstableOnlyMethods` allocate the combined map even when it adds no method [merge.go:cloneStringMap, mergeUnstableOnlyMethods]

## Side Effects

- Reads `meta.json`, `schema.json`, `meta.unstable.json`, and `schema.unstable.json` under `schemaDir` with `os.ReadFile` [load.go:ReadMeta] [load.go:ReadSchema] [load.go:ReadMetaUnstable] [load.go:ReadSchemaUnstable]
- The merge writes only to maps and definitions it allocates. Unchanged stable `*Definition` pointers are shared, not copied, into the combined schema [merge.go:MergeStableAndUnstable]
- No network, subprocess, environment, goroutine, lock, or package-level state use [load.go:ReadMeta] [merge.go:MergeStableAndUnstable]

## Verification

- Run: `cd cmd/generate && go test ./internal/load/...`
