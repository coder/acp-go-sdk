# ir

## Overview

Package `ir` holds the generator's intermediate representation of ACP methods.
`BuildMethodGroups` links each wire method on the agent or client side to its Go Request, Response, and Notification type names, its Go method name, and the interface the emitter puts it in: `Agent` or `Client`, or the method's own optional interface.
`Fields` turns the properties of an object schema into the fields of a Go struct.
It also provides schema type helpers (`PrimaryType`, `IsNullResponse`) and naming helpers used by `github.com/coder/acp-go-sdk/cmd/generate/internal/emit`.
The package imports only `fmt`, `sort`, `strings`, and the sibling `load` and `util` packages, and it does no I/O.

## Interface

- `type MethodInfo struct` with fields `Side`, `Method`, `Req`, `Resp`, `Notif`, `Name` (the Go method that handles the wire method), `Optional` (the name of the method's own optional interface, `""` for a method of `Agent` or `Client`; all `string`), and `DocsIgnored` (`bool`) [ir.go:MethodInfo]
- `type Groups map[string]*MethodInfo`, keyed by `side|wire` [ir.go:Groups]
- `func PrimaryType(d *load.Definition) string` returns the primary JSON Schema type name of a definition [ir.go:PrimaryType]
- `func IsNullResponse(def *load.Definition) bool` reports whether a response definition is missing or has type `"null"` [ir.go:IsNullResponse]
- `func BuildMethodGroups(schema *load.Schema, meta *load.Meta) Groups` builds the method groups from the schema and the meta fallback [ir.go:BuildMethodGroups]
- `func SortedKeys(m map[string]string) []string` returns the keys of `m` in sorted order [ir.go:SortedKeys]
- `type Lenient struct` with the `bool` fields `DefaultOnError`, `DefaultOnNull`, and `SkipInvalidItems`: the decode leniency that the `x-deserialize-*` hints of one property ask for. The zero value is strict [field.go:Lenient]
- `type Field struct` with fields `Prop` (the property name on the wire), `Name` (the exported Go field name), `Def` (`*load.Definition`, the property's schema), `Required` (`bool`), and `Lenient` (`Lenient`) [field.go:Field]
- `func Fields(props map[string]*load.Definition, required []string) []Field` returns one `Field` per property of an object schema [field.go:Fields]
- `func PatchFields(schema *load.Schema) map[*load.Definition]bool` returns the patch fields of the schema, keyed by the property's definition [patch.go:PatchFields]
- `func NullAsAbsent(schema *load.Schema) map[*load.Definition]bool` returns the patch fields for which the schema defines no clear, keyed by the property's definition [patch.go:NullAsAbsent]

## Invariants

- A group key is `side + "|" + wire method`, for example `agent|session/new` [ir.go:key]
- Definitions without `XMethod` or `XSide` are skipped. A definition name ending in `Request`, `Response`, or `Notification` sets `Req`, `Resp`, or `Notif` of its group [ir.go:BuildMethodGroups]
- The meta fallback adds a group only when the schema produced none for that side and wire method (the `side|wire` key), and only when `Req` or `Notif` ends up non-empty. Agent `session/cancel` gets `Notif` `CancelNotification`, client `session/update` gets `Notif` `SessionNotification`, and other methods use type names that `inferTypeBaseFromMethodKey` derives and that exist in the schema [ir.go:BuildMethodGroups, inferTypeBaseFromMethodKey]
- Every group gets `Name` from `methodName`, `Optional` from `optionalInterface`, and `DocsIgnored` from `isDocsIgnoredMethod` after all groups exist [ir.go:BuildMethodGroups]
- `Optional` is `""` only for a method that is not unstable and that `requiredMethods` lists: the stable methods of schema 1.25.0 but `session/load`. That list never grows, so a schema release never adds a method to `Agent` or `Client`. Every other method gets the name from `optionalNames` (`AgentLoader` for agent `session/load`), else the side title-cased plus `Name`, such as `AgentUnstableForkSession` or `ClientUnstableMessageMcp` [ir.go:optionalInterface] [baseline.go:requiredMethods, optionalNames] [ir_test.go:TestBuildMethodGroups_PutsOnlyStableBaselineMethodsInAgentAndClient]
- A method is unstable when its `Req`, `Resp`, or `Notif` name starts with `Unstable`, or when one of those definitions has a description that contains `unstable` (case-insensitive) [ir.go:isUnstableMethod]
- A method is docs-ignored when its `Req`, `Resp`, or `Notif` definition has `DocsIgnore` set [ir.go:isDocsIgnoredMethod]
- `PrimaryType` returns `""` for a nil definition. When `Type` is nil it returns the first non-empty result from the `AllOf` entries. For a `[]any` type it returns the first string that is not `"null"`, else the first string [ir.go:PrimaryType]
- `inferTypeBaseFromMethodKey` maps `terminal_wait_for_exit` to `WaitForTerminalExit`. A two-part key whose second part is `new`, `create`, `release`, `wait`, `load`, `authenticate`, `prompt`, `cancel`, `read`, or `write` becomes verb then noun (for example `session_new` becomes `NewSession`) [ir.go:inferTypeBaseFromMethodKey]
- `Name` is `SessionUpdate` for `session/update` and `Cancel` for `session/cancel`. Otherwise it is the `Notif` type name without `Notification`, else the `Req` type name without `Request` [ir.go:methodName]
- `BuildMethodGroups` does not check `schema` or `meta` for nil, so a nil argument panics [ir.go:BuildMethodGroups]
- `Fields` returns the fields sorted by property name. `Name` is `util.ToExportedField` of the property name, and `Required` is true for a property that `required` lists. `Lenient` copies the three hint fields of the property's `load.Definition` [field.go:Fields] [field_test.go:TestFields_ReadsNamesRequiredAndHints]
- `Fields` returns no field for an optional property whose type is `"null"` alone. Such a property can hold no value, as `stopReason` in the `none` variant of `IdleStateUpdate`. A required one keeps its field, so that the struct writes the member as `null`. No schema from 0.13.5 through 1.32.0 has one [field.go:Fields] [field_test.go:TestFields_SkipsAnOptionalPropertyWhoseTypeIsOnlyNull]
- `Fields` panics when a property that is not an array has `x-deserialize-skip-invalid-items` [field.go:Fields] [field_test.go:TestFields_PanicsOnSkipInvalidItemsOutsideAnArray]
- A patch field is an optional property of an update that changes a value the receiver stores: absent leaves it, `null` clears it, and a value replaces it. The schema says so only in prose, so `PatchFields` reads the descriptions. Every optional property of an object is one when the object's description matches `only changed fields`, `partial update(s)` or `omitted patch fields` (`ToolCallUpdate`, `SessionInfoUpdate`, `SubagentUpdate`). A property is one when the object's description names it in backticks in a sentence with `have patch semantics` (`CompactionUpdate`: `summary`, `error`, `_meta`), or when its own description says that omitted means unchanged, that `null` clears or removes it, or `Set to null to clear` (`SessionMessage`). A required property is never one. A union variant that takes an object's properties through `allOf` shares their definitions, so its fields are patch fields too [patch.go:PatchFields] [patch_test.go:TestPatchFields_ReadsTheDescriptions]
- A patch field has no clear when its object's description matches only `only changed fields` or `partial update(s)`, and its own description does not mention `null`: all of `ToolCallUpdate` but `name`, and `_meta` of `SessionInfoUpdate`. ACP v1 peers, the Rust SDK among them, read `null` there as absent, so `NullAsAbsent` returns these fields and the emitter says so in their docs [patch.go:NullAsAbsent] [patch_test.go:TestNullAsAbsent_ReadsTheDescriptions]

## Side Effects

- None. The package does no file, network, subprocess, or environment access and starts no goroutines [ir.go:BuildMethodGroups]
- `BuildMethodGroups` writes only to the `Groups` map and `MethodInfo` values it allocates. It does not change `schema` or `meta` [ir.go:BuildMethodGroups]

## Verification

- Run: `cd cmd/generate && go test ./internal/ir/...`
