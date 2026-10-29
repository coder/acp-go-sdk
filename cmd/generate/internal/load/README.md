# load

## Overview

Package `load` reads the generator inputs from the schema directory into minimal Go structs: `meta.json`, `schema.json`, and the optional `meta.unstable.json` and `schema.unstable.json`.
`Definition` models only the JSON Schema fields the generator uses, and it accepts boolean schemas.
`MergeStableAndUnstable` builds a new combined `Meta` and `Schema` from the stable and unstable inputs. It adds the unstable-only methods and `Unstable`-prefixed copies of their types and of the new or changed definitions those types reach. It also writes the unstable version, under the same name, of each definition that stable methods reach through unstable `$ref`s and that is new or differs from stable.

## Interface

- `type Meta struct { Version int; AgentMethods, ClientMethods, ProtocolMethods map[string]string }`, decoded from `meta.json` [load.go:Meta]
- `type Schema struct { Defs map[string]*Definition }`, decoded from the `$defs` key [load.go:Schema]
- `type Discriminator struct { PropertyName string }` [load.go:Discriminator]
- `type Definition struct` with JSON-tagged fields `Description`, `Type`, `Properties`, `Required`, `Enum`, `Items`, `Ref`, `AnyOf`, `OneOf`, `AllOf`, `DocsIgnore` (`x-docs-ignore`), `Title`, `Const`, `XSide` (`x-side`), `XMethod` (`x-method`), `Default`, and `Discriminator` [load.go:Definition]
- `func (d *Definition) UnmarshalJSON(b []byte) error` decodes the object and boolean JSON Schema forms [load.go:Definition.UnmarshalJSON]
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
- Request, Response, and Notification definitions of unstable-only methods are copied when they have both `x-method` and `x-side`. A definition without `x-side` is copied only when another copied root reaches it. Reachable definitions that are new or differ from stable are copied too, and a fixpoint loop also copies any reachable definition that refers to a copied one [merge.go:buildUnstableDuplicateSet]
- Copies are named `"Unstable" + name`. In a copy, only `$ref` values whose targets are also copied are rewritten to the prefixed names. A reference to a definition that stays unprefixed (for example one identical to its stable counterpart) keeps pointing at that definition. The merge fails if a prefixed name already exists in the combined defs [merge.go:MergeStableAndUnstable, rewriteDefinitionRefs]
- A copy whose `XMethod` names a stable wire method has `XMethod` and `XSide` cleared [merge.go:MergeStableAndUnstable]
- Definitions reachable through unstable `$ref`s from stable Request, Response, and Notification roots that are new or differ in the unstable schema are written into the combined schema under the same name, as copies of the unstable version. Their missing `$ref` targets are then copied from the unstable schema [merge.go:promoteChangedSharedDefs, ensureRefsAvailable]
- The merge does not change either input. Unstable definitions are copied as a tree (properties, items, and sub-schemas) before refs are rewritten. The `Type`, `Const`, `Default`, and `Enum` element values stay shared with the input [merge.go:MergeStableAndUnstable, deepCopyDefinition]
- An empty method map in the stable meta stays nil in the combined meta only when the matching unstable map is empty too. A non-empty unstable map makes `mergeUnstableOnlyMethods` allocate the combined map even when it adds no method [merge.go:cloneStringMap, mergeUnstableOnlyMethods]

## Side Effects

- Reads `meta.json`, `schema.json`, `meta.unstable.json`, and `schema.unstable.json` under `schemaDir` with `os.ReadFile` [load.go:ReadMeta] [load.go:ReadSchema] [load.go:ReadMetaUnstable] [load.go:ReadSchemaUnstable]
- The merge writes only to maps and definitions it allocates. Unchanged stable `*Definition` pointers are shared, not copied, into the combined schema [merge.go:MergeStableAndUnstable]
- No network, subprocess, environment, goroutine, lock, or package-level state use [load.go:ReadMeta] [merge.go:MergeStableAndUnstable]

## Verification

- Run: `cd cmd/generate && go test ./internal/load/...`
