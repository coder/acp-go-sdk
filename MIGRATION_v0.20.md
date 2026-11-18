# Migrating to v0.20.0

v0.20.0 makes every planned API break in one release, so that you update once. The ACP schema stays at 1.32.0, and `acp.Agent` and `acp.Client` do not change. Most breaks are compile errors: update the module, build, and fix each error with the tables below.

```bash
go get github.com/coder/acp-go-sdk@v0.20.0
go build ./...
```

**Some changes still compile but behave differently.** Check each of them in your code: [Changes the compiler does not catch](#changes-the-compiler-does-not-catch).

| Change | Section |
| --- | --- |
| `_meta` is `acp.Meta`; `rawInput`, `rawOutput`, error `data` and the `mcp/message` payloads are `json.RawMessage` | [Free-form values are raw JSON](#free-form-values-are-raw-json) |
| Elicitation maps, `env` and `headers` take their member types | [Typed maps](#typed-maps) |
| Optional fields of an update are `acp.Patch[T]` | [Patch fields](#patch-fields) |
| Integers of format `int64`, `uint32` and `uint64` are `int64` | [64-bit integers](#64-bit-integers) |
| `Unstable` copies of shared types are gone | [One type per shape](#one-type-per-shape) |

## Free-form values are raw JSON

A value that the schema leaves free keeps the JSON as it arrived, so an integer above 2^53, such as a nanosecond timestamp or a 64-bit id, no longer changes on a round trip. `_meta` is `acp.Meta`, a `map[string]json.RawMessage` with `NewMeta`, `Get` and `Set`. `rawInput`, `rawOutput`, `RequestError.Data` and the result of `mcp/message` are `json.RawMessage`. The params of `mcp/message` are a `map[string]json.RawMessage`. `ExtRequest`, `ExtResponse` and `ExtNotification` are aliases of `json.RawMessage`, and the `Params` or `Result` of the envelope types `AgentRequest`, `AgentNotification`, `AgentResult`, `ClientRequest`, `ClientNotification` and `ClientResult` is a `json.RawMessage` too (#117). A struct with a `json.RawMessage` field cannot be compared, so these six types, `AgentError`, `ClientError`, `Error` and `RequestError` are no longer comparable: `==` on them fails to compile, and `==` through an interface panics.

| v0.19.0 | v0.20.0 |
| --- | --- |
| `Meta: map[string]any{"k": v}` | `meta, err := acp.NewMeta(map[string]any{"k": v})`, or `var meta acp.Meta; err := meta.Set("k", v)` |
| `s, ok := x.Meta["k"].(string)` | `var s string; err := x.Meta.Get("k", &s)`. A missing key returns an error that wraps `acp.ErrMetaKeyNotFound`. |
| `RawInput: map[string]any{"path": p}` | `acp.WithStartRawInput(map[string]any{"path": p})`, or `json.Marshal` it |
| `in, ok := tc.RawInput.(map[string]any)` (`tc` an `acp.ToolCall`) | `var in map[string]any; if len(tc.RawInput) > 0 { err = json.Unmarshal(tc.RawInput, &in) }`. An absent value is a nil `json.RawMessage`, and `json.Unmarshal` of it fails. |
| `in, ok := u.RawInput.(map[string]any)` (`u` an `acp.ToolCallUpdate`) | `var in map[string]any; if raw, ok := u.RawInput.Value(); ok { err = json.Unmarshal(raw, &in) }` |
| `acp.ToolCallUpdate{RawInput: v}` | `acp.WithUpdateRawInput(v)`, or `RawInput: acp.PatchSet(json.RawMessage(b))` with `b` the JSON of `v` |
| `p, ok := req.Params.(map[string]any)` (`req` an `acp.AgentRequest`; also `AgentNotification`, `ClientRequest`, `ClientNotification`, and `Result` of `AgentResult`, `ClientResult`) | `var p map[string]any; if len(req.Params) > 0 { err = json.Unmarshal(req.Params, &p) }` |
| `&acp.RequestError{Code: c, Message: m, Data: d}` | `acp.NewRequestError(c, m, d)`; read `re.Data` with `json.Unmarshal` when `len(re.Data) > 0` |

In the update types whose fields are [patch fields](#patch-fields), such as `ToolCallUpdate`, `_meta`, `rawInput` and `rawOutput` are a `Patch` of these types: set one with `acp.PatchSet(meta)`, and read it with `Value()` first, as the second `RawInput` row shows.

The raw helpers (`WithStartRawInput`, `WithStartRawOutput`, `WithUpdateRawInput`, `WithUpdateRawOutput`) and the `New*` error constructors still take `any` and encode it. When `json.Marshal` cannot encode the value, such as a channel or a NaN:

- `WithUpdateRawInput` and `WithUpdateRawOutput` make the update fail to encode, so `SessionUpdate` returns `-32602` invalid params that name the marshal error, as v0.19.0 did (#121).
- `WithStartRawInput`, `WithStartRawOutput` and the error constructors leave the field out, and the send succeeds. v0.19.0 failed the send. To see the error, call `json.Marshal` yourself and pass the `json.RawMessage`.

**Wire changes.** A free-form value is written back as it was read. Nested values keep their key order and number spelling, where v0.19.0 sorted nested keys and wrote `1e3` as `1000` and `2.0` as `2`; the keys of `_meta` itself are still sorted. `"data": null` in `RequestError` and `UnstableMcpError` is kept, so `Data != nil` is true for it, while the generated `Error` leaves it out. A handler that returns a `RequestError` whose `Data` is not valid JSON now sends `-32603`; before, the response failed to encode and the peer waited (#118).

## Typed maps

A map whose members have a schema takes their Go type (#118).

| v0.19.0 | v0.20.0 |
| --- | --- |
| `Properties: map[string]any{"host": acp.ElicitationPropertySchema{...}}` | `Properties: map[string]acp.ElicitationPropertySchema{"host": {...}}` |
| re-encode `schema.Properties[name]` into an `ElicitationPropertySchema` | `field := schema.Properties[name]` |
| `Content: map[string]any{"host": v}` | `Content: map[string]acp.ElicitationContentValue{"host": acp.NewElicitationContentValueString(v)}` |
| `host, ok := c.Content["host"].(string)` | `if s := c.Content["host"].String; s != nil { host := string(*s) }` |
| `Env: map[string]any{"K": "v"}` | `Env: map[string]string{"K": "v"}` (`AuthMethodTerminal`, `AuthMethodTerminalInline`) |
| `Headers: map[string]any{"Authorization": "Bearer x"}` | `Headers: map[string]string{"Authorization": "Bearer x"}` (`UnstableSetProviderRequest`) |

`NewElicitationContentValueString`, `...Integer` (an `int64`), `...Number`, `...Boolean` and `...StringArray` build a content value. A zero `ElicitationContentValue{}` fails to encode.

**Stricter decoding.** These maps now decode into their schema's types, as the Rust SDK does. A message that v0.19.0 accepted now fails to decode when:

- an elicitation content value is `null`, an object, or a mixed array;
- an integer field's `minimum` or `maximum` is not an integer, including `1.0`;
- a property schema is `null` or has no `type`;
- an `env` or `headers` value is neither a string nor `null`. A `null` value decodes as `""`.

For example, docker/docker-agent decodes an MCP server's form schema into `acp.ElicitationSchema`, so `{"type": "integer", "minimum": 1.0}` from a Python server is now refused instead of forwarded.

## Patch fields

The optional fields of an update that changes a stored value are `acp.Patch[T]`, so that "unchanged" and "clear" differ (#119). They are the fields of `ToolCallUpdate`, `SessionInfoUpdate`, `CompactionUpdate` (`summary`, `error`, `_meta`), the unstable `SubagentUpdate` and `SessionMessage`, and the `SessionUpdate` variants that embed them.

A `Patch` has three states:

- unset, the zero value: the member is absent, and the stored value stays;
- null, `acp.PatchNull[T]()`: the member is `null`, which clears the stored value;
- set, `acp.PatchSet(v)`: the member holds `v`, which replaces the stored value.

Read one with `Value() (T, bool)`, `Ptr()` (nil unless set), `IsNull()` and `IsUnset()`. `acp.PatchFromPtr(p)` sets `*p`, and leaves the field unset for a nil `p`.

| v0.19.0 | v0.20.0 |
| --- | --- |
| `Title: acp.Ptr("x")`, `Content: blocks` | `Title: acp.PatchSet("x")`; `Content: acp.PatchSet(blocks)` only when `len(blocks) > 0`. v0.19.0 left an empty or nil slice or map out; `PatchSet` writes it as `[]` or `{}`, which clears the stored value. |
| `Title: title` (a `*string` that may be nil) | `Title: acp.PatchFromPtr(title)` (unset for nil) |
| `if u.Status != nil { s = *u.Status }` | `if s, ok := u.Status.Value(); ok { ... }`, or `u.Status.Ptr()` where the code wants a `*T` |
| `for _, c := range u.Content` | `content, _ := u.Content.Value(); for _, c := range content` |
| (no way to clear) | `info.Title = acp.PatchNull[string]()` on `SessionInfoUpdate` (and the patch fields of `CompactionUpdate`, `SubagentUpdate`, `SessionMessage`); check `IsNull()`. ACP v1 defines no clear for `ToolCallUpdate`: send `acp.PatchSet([]acp.ToolCallContent{})` or `acp.PatchSet([]acp.ToolCallLocation{})` to clear `content` or `locations`. |

**`null` in a `ToolCallUpdate`, and in `_meta` of a `SessionInfoUpdate`, does not clear.** ACP v1 says that `null` leaves `name`, `rawInput` and `rawOutput` unchanged, and the Rust SDK reads `null` in every `ToolCallUpdate` field and in `SessionInfoUpdate._meta` as absent. Treat `IsNull()` there like `IsUnset()`, and do not send `PatchNull` there to clear. The docs of those fields say so (`SessionSessionInfoUpdate.Meta` too).

**A `Patch` in a struct of your own** needs the `omitzero` tag option (Go 1.24 or later) to be left out when unset. `omitempty` never leaves out a struct, so it writes an unset `Patch` as `null`, which clears. Go 1.21 to 1.23 ignore `omitzero`. There, give the struct a `MarshalJSON` of its own, or, in a struct that you only write, make the field a `*acp.Patch[T]` with `omitempty` and leave it nil when unset. Do not read into such a pointer: `encoding/json` reads `null` into it as nil, so `null` and absent look the same.

`PatchSet` of a nil slice or map writes `[]` or `{}`. A type with its own `MarshalJSON` writes itself: a nil `json.RawMessage` or a nil pointer is `null`, and so it reads back as `IsNull()`. A nil `[]byte` is `[]`, and an empty one is `""`.

## 64-bit integers

An integer of format `int64`, `uint32` or `uint64` is `int64`, so that a 32-bit target (`GOARCH=386`, `arm`) holds a Windows exit code such as `3221225477`, or a token count above 2^31 (#118, #120). A `uint64` value above 2^63−1, which the schema allows, does not fit: it fails to decode, or is dropped in a field that reads a bad value as absent. No ACP value comes near it. On a 64-bit target, decoding does not change.

- `*int` to `*int64`: `TerminalExitStatus.ExitCode`, `WaitForTerminalExitResponse.ExitCode`, `ReadTextFileRequest.Line` and `Limit`, `ToolCallLocation.Line`, `CreateTerminalRequest.OutputByteLimit`, `MinLength`/`MaxLength` of `StringPropertySchema` and `ElicitationPropertySchemaString`, `MinItems`/`MaxItems` of `MultiSelectPropertySchema` and `ElicitationPropertySchemaArray`, `ThoughtTokens`, `CachedReadTokens` and `CachedWriteTokens` of `Usage`, `Size` of `ResourceLink` and `ContentBlockResourceLink`, and `Minimum`, `Maximum` and `Default` of `IntegerPropertySchema` and `ElicitationPropertySchemaInteger`.
- `int` to `int64`: `InputTokens`, `OutputTokens` and `TotalTokens` of `Usage`, and `Used` and `Size` of `UsageUpdate` and of the `SessionUpdate` variant `SessionUsageUpdate`.
- `RequestIdNumber` and `ElicitationContentValueInteger` are `int64` too. apidiff calls this compatible, and conversions still compile, but `int(...)` of them truncates on a 32-bit target.

| v0.19.0 | v0.20.0 |
| --- | --- |
| `Line: acp.Ptr(10)`, `MinLength: acp.Ptr(1)` | `Line: acp.Ptr[int64](10)`, `MinLength: acp.Ptr[int64](1)` |
| `size := 1024; link.Size = &size` | `size := int64(1024); link.Size = &size` |
| `acp.UsageUpdate{Used: used, Size: size}` with `int` values | `acp.UsageUpdate{Used: int64(used), Size: int64(size)}` |
| `var in int = usage.InputTokens` | `in := usage.InputTokens` (an `int64`), or `int(usage.InputTokens)` |
| `lines[*p.Line-1:]` | `lines[int(max(0, min(*p.Line-1, int64(len(lines))))):]`: clamp to the slice, at both ends, before you convert. The schema allows `line: 0`. |
| `n := *status.ExitCode` used as `int` | `code := *status.ExitCode` (an `int64`). Keep it `int64`: on a 32-bit target, `int(...)` wraps a Windows exit code such as `3221225477`. |

A negative value of an unsigned format, or a value above its range such as 4294967296 in a `uint32` field, still decodes. The Rust SDK rejects such a value, or drops it in a field that reads a bad value as absent (`exitCode`, `ToolCallLocation.line`).

## One type per shape

Unstable methods no longer get `Unstable`-prefixed copies of the types that stable methods use (#122). A shared type carries the unstable additions, each marked `**UNSTABLE**` in its doc comment: at schema 1.32.0, the constant `SessionConfigOptionCategorySpeed` and the `Acp` variant of `McpServer`. The types of unstable-only methods keep the prefix, such as `UnstableForkSessionRequest`. In every case but two you drop the `Unstable` prefix: `UnstableMcpServerHttp` and `UnstableMcpServerSse` become `McpServerHttpInline` and `McpServerSseInline`.

| v0.19.0 | v0.20.0 |
| --- | --- |
| `acp.UnstableSessionConfigOption`, `UnstableSessionConfigOptionBoolean`, `UnstableSessionConfigOptionSelect`, `UnstableSessionConfigOptionCategory` | `acp.SessionConfigOption`, `SessionConfigOptionBoolean`, `SessionConfigOptionSelect`, `SessionConfigOptionCategory` |
| `acp.UnstableSessionConfigOptionCategorySpeed` (and the other `UnstableSessionConfigOptionCategory*` constants) | `acp.SessionConfigOptionCategorySpeed` (and so on) |
| `acp.NewUnstableSessionConfigOptionBoolean(...)`, `NewUnstableSessionConfigOptionSelect(...)` | `acp.NewSessionConfigOptionBoolean(...)`, `NewSessionConfigOptionSelect(...)` |
| `acp.UnstableInitialSessionConfigOption`, `UnstableInitialSessionConfigOptionBoolean`, `UnstableInitialSessionConfigOptionValueId` | `acp.InitialSessionConfigOption`, `InitialSessionConfigOptionBoolean`, `InitialSessionConfigOptionValueId` |
| `acp.UnstableMcpServer`, `UnstableMcpServerAcp`, `UnstableMcpServerAcpId`, `UnstableMcpServerAcpInline` | `acp.McpServer`, `McpServerAcp`, `McpServerAcpId`, `McpServerAcpInline` |
| `acp.UnstableMcpServerHttp`, `UnstableMcpServerSse` (the http and sse variants of `UnstableMcpServer`, with a `Type` field) | `acp.McpServerHttpInline`, `McpServerSseInline` (`McpServerHttp` and `McpServerSse` are the definitions without `Type`, which the variants embed) |

The fields of the unstable methods' types (`UnstableForkSessionRequest.ConfigOptions` and `.McpServers`, `UnstableForkSessionResponse.ConfigOptions`, `UnstableSessionOptionsResponse.ConfigOptions`, and `ServerId` of `UnstableMessageMcpRequest` and `UnstableMessageMcpNotification`) now use the shared types. The compiler reports each break, and the wire does not change.

## Changes the compiler does not catch

These compile after the migration and behave differently. Find them with `grep`, and check each one.

1. **A free-form value passed to `func(v any)`.** These are now a `json.RawMessage`: `RawInput` and `RawOutput` of a `ToolCall`, a `Meta` value, an error's `Data`, the `Params` or `Result` of the envelope types (`AgentRequest`, `AgentNotification`, `AgentResult`, `ClientRequest`, `ClientNotification`, `ClientResult`), and the result of `mcp/message`. The `Params` of `mcp/message` are a `map[string]json.RawMessage`. In a `ToolCallUpdate`, `RawInput` and `RawOutput` are `acp.Patch[json.RawMessage]`: read them with `Value()` first. Either way, an assertion or type switch on `map[string]any`, `[]any`, `string` or `float64` takes its default branch, while `json.Marshal(v)` still writes the same JSON. Decode first: into a typed struct to keep every digit, or into `any` for the v0.19.0 behavior. Find these calls, and those of item 2, with `grep -nE '\.(RawInput|RawOutput|Data|Meta|Params|Result|Content|Properties|Env|Headers)\b'`.

   For example, jaegertracing/jaeger passes `u.ToolCallUpdate.RawOutput` to its `flattenToolResultContent(raw any)`. The `!= nil` check before the call fails to compile, but once that check is migrated, the call still compiles. It then gets a `Patch` and sends the tool output's JSON instead of its text:

   ```go
   // v0.19.0
   if u.ToolCallUpdate.RawOutput != nil {
   	text := flattenToolResultContent(u.ToolCallUpdate.RawOutput)
   }
   // v0.20.0
   if raw, ok := u.ToolCallUpdate.RawOutput.Value(); ok {
   	var out any
   	if err := json.Unmarshal(raw, &out); err != nil {
   		return err
   	}
   	text := flattenToolResultContent(out)
   }
   ```

1. **A typed map passed to `func(v any)`.** Elicitation `Content` holds `ElicitationContentValue` structs where it held strings and numbers, `ElicitationSchema.Properties` holds `ElicitationPropertySchema` values, and `Env` and `Headers` are a `map[string]string`, so a type switch on `map[string]any` takes its default branch. Encode the map and decode it into `map[string]any` first. docker/docker-agent passes `Content` to `jsonschema.Resolved.Validate`.

1. **Any other `Patch` passed to `func(v any)`.** `json.Marshal` of it writes the same JSON, but a type switch on `*T` or `[]T` no longer matches. Read it with `Value()` first.

1. **`fmt` prints a `Patch` as a struct**, such as `{[...] 2 <nil>}`. Print `Value()` instead.

1. **`WithUpdateContent` and `WithUpdateLocations` with an empty non-nil slice** now send `[]`, which clears the stored list. v0.19.0 left it out. A nil slice still leaves the field out.

1. **An integer field passed through `any`** and asserted as `int` (`v.(int)`, `case int:`) no longer matches: it is an `int64`.

1. **A value that does not encode** given to `WithStartRawInput`, `WithStartRawOutput` or an error constructor is left out, and the send succeeds. v0.19.0 failed the send.

1. **Re-wrapping a `Patch` from `WithUpdateRawInput` or `WithUpdateRawOutput` whose value did not encode**, as `acp.PatchFromPtr(p.Ptr())` or `acp.PatchSet` of its `Value()`, drops its error and writes `null`: such a Patch reads as set with a nil value, and only its `MarshalJSON` returns the error.

1. **`==` through an interface** (`any(a) == any(b)`) panics for the ten types that are no longer comparable: `AgentRequest`, `AgentNotification`, `AgentResult`, `ClientRequest`, `ClientNotification`, `ClientResult`, `AgentError`, `ClientError`, `Error` and `RequestError`. `==` on them directly fails to compile.

1. **Stricter decoding** of elicitation schemas and content, `env` and `headers`: see [Typed maps](#typed-maps).
