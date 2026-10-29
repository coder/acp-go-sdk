# ir

## Overview

Package `ir` holds the generator's intermediate representation of ACP methods.
`BuildMethodGroups` links each wire method on the agent or client side to its Go Request, Response, and Notification type names and to the interface binding the emitter puts it in.
It also provides schema type helpers (`PrimaryType`, `IsNullResponse`) and naming helpers used by `github.com/coder/acp-go-sdk/cmd/generate/internal/emit`.
The package imports only `sort`, `strings`, and the sibling `load` and `util` packages, and it does no I/O.

## Interface

- `type MethodBinding int` names the interface a method belongs to [ir.go:MethodBinding]
- Constants `BindUnknown`, `BindAgent`, `BindAgentLoader`, `BindAgentExperimental`, `BindClient`, `BindClientExperimental`, `BindClientTerminal` (`iota` values of `MethodBinding`) [ir.go:BindUnknown]
- `type MethodInfo struct` with fields `Side`, `Method`, `MethodKey`, `Req`, `Resp`, `Notif` (`string`), `Binding` (`MethodBinding`), and `DocsIgnored` (`bool`) [ir.go:MethodInfo]
- `type Groups map[string]*MethodInfo`, keyed by `side|wire` [ir.go:Groups]
- `func PrimaryType(d *load.Definition) string` returns the primary JSON Schema type name of a definition [ir.go:PrimaryType]
- `func IsNullResponse(def *load.Definition) bool` reports whether a response definition is missing or has type `"null"` [ir.go:IsNullResponse]
- `func BuildMethodGroups(schema *load.Schema, meta *load.Meta) Groups` builds the method groups from the schema and the meta fallback [ir.go:BuildMethodGroups]
- `func DispatchMethodNameForNotification(methodKey, typeName string) string` returns the Go method name for a notification [ir.go:DispatchMethodNameForNotification]
- `func SortedKeys(m map[string]string) []string` returns the keys of `m` in sorted order [ir.go:SortedKeys]

## Invariants

- A group key is `side + "|" + wire method`, for example `agent|session/new` [ir.go:key]
- Definitions without `XMethod` or `XSide` are skipped. A definition name ending in `Request`, `Response`, or `Notification` sets `Req`, `Resp`, or `Notif` of its group [ir.go:BuildMethodGroups]
- The meta fallback adds a group only when the schema produced none for that side and wire method (the `side|wire` key), and only when `Req` or `Notif` ends up non-empty. Agent `session/cancel` gets `Notif` `CancelNotification`, client `session/update` gets `Notif` `SessionNotification`, and other methods use type names that `inferTypeBaseFromMethodKey` derives and that exist in the schema [ir.go:BuildMethodGroups, inferTypeBaseFromMethodKey]
- `BuildMethodGroups` never sets `MethodKey`, so it stays empty in the returned groups [ir.go:BuildMethodGroups]
- Every group gets `Binding` from `classifyBinding` and `DocsIgnored` from `isDocsIgnoredMethod` after all groups exist [ir.go:BuildMethodGroups]
- Agent `session/load` binds to `BindAgentLoader`. Other unstable methods bind to `BindAgentExperimental` or `BindClientExperimental`. Other agent-side methods bind to `BindAgent` and other client-side methods to `BindClient`. A group with any other side, such as `both`, gets `BindUnknown` [ir.go:classifyBinding]
- `BuildMethodGroups` never assigns `BindClientTerminal` [ir.go:classifyBinding]
- A method is unstable when its `Req`, `Resp`, or `Notif` name starts with `Unstable`, or when one of those definitions has a description that contains `unstable` (case-insensitive) [ir.go:isUnstableMethod]
- A method is docs-ignored when its `Req`, `Resp`, or `Notif` definition has `DocsIgnore` set [ir.go:isDocsIgnoredMethod]
- `PrimaryType` returns `""` for a nil definition. When `Type` is nil it returns the first non-empty result from the `AllOf` entries. For a `[]any` type it returns the first string that is not `"null"`, else the first string [ir.go:PrimaryType]
- `inferTypeBaseFromMethodKey` maps `terminal_wait_for_exit` to `WaitForTerminalExit`. A two-part key whose second part is `new`, `create`, `release`, `wait`, `load`, `authenticate`, `prompt`, `cancel`, `read`, or `write` becomes verb then noun (for example `session_new` becomes `NewSession`) [ir.go:inferTypeBaseFromMethodKey]
- `DispatchMethodNameForNotification` returns `SessionUpdate` for `session_update` and `Cancel` for `session_cancel`. Otherwise it trims the `Notification` suffix from `typeName` [ir.go:DispatchMethodNameForNotification]
- `BuildMethodGroups` does not check `schema` or `meta` for nil, so a nil argument panics [ir.go:BuildMethodGroups]

## Side Effects

- None. The package does no file, network, subprocess, or environment access and starts no goroutines [ir.go:BuildMethodGroups]
- `BuildMethodGroups` writes only to the `Groups` map and `MethodInfo` values it allocates. It does not change `schema` or `meta` [ir.go:BuildMethodGroups]

## Verification

- Run: `cd cmd/generate && go test ./internal/ir/...`
