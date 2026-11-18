<a href="https://agentclientprotocol.com/" >
  <img alt="Agent Client Protocol" src="https://zed.dev/img/acp/banner-dark.webp">
</a>

# ACP Go SDK

Go library for the Agent Client Protocol (ACP) - a standardized communication protocol
between code editors and AI‑powered coding agents.

Learn more about the protocol itself at <https://agentclientprotocol.com>.

## Installation

<!-- `$ printf 'go get github.com/coder/acp-go-sdk@v%s\n' "$(cat version)"` as bash -->

```bash
go get github.com/coder/acp-go-sdk@v0.20.0
```

The SDK has its own semver, separate from the ACP schema version.
`acp.SchemaVersion` names the ACP schema release that an SDK version targets.

Upgrading from v0.19.x? v0.20.0 makes every planned API break at once:
follow [MIGRATION_v0.20.md](MIGRATION_v0.20.md).

## Get Started

### Understand the Protocol

Start by reading the [official ACP documentation](https://agentclientprotocol.com)
to understand the core concepts and protocol specification.

### Try the Examples

The [examples directory](https://github.com/coder/acp-go-sdk/tree/main/example)
contains simple implementations of both Agents and Clients in Go.
You can run them from your terminal or connect to external ACP agents.

- `go run ./example/agent` starts a minimal ACP agent over stdio.
- `go run ./example/claude-code` demonstrates bridging to Claude Code.
- `go run ./example/client` connects to a running agent and streams a sample turn.
- `go run ./example/gemini` bridges to the Gemini CLI in ACP mode (flags: -model, -sandbox, -yolo, -debug, -gemini /path/to/gemini).

You can watch the interaction by running `go run ./example/client` locally.

### Explore the API

Browse the Go package docs on pkg.go.dev for detailed API documentation:

- <https://pkg.go.dev/github.com/coder/acp-go-sdk>

If you're building an [Agent](https://agentclientprotocol.com/protocol/overview#agent):

- Implement the `acp.Agent` interface. Every other agent method has its own
  optional interface, such as `acp.AgentLoader` for `session/load` and
  `acp.AgentUnstableForkSession` for the unstable `session/fork`. Implement the
  ones you support: the connection answers the others with `-32601` (method not
  found), and ignores their notifications with a Debug log record. Assert each one you implement, for example
  `var _ acp.AgentLoader = (*myAgent)(nil)`: when a schema release changes
  the method, your build fails, instead of the method turning into `-32601`.
- Create a connection with `acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)`.
- Send updates and make client requests using the returned connection.

If you're building a [Client](https://agentclientprotocol.com/protocol/overview#client):

- Implement the `acp.Client` interface. It includes the terminal methods
  (`CreateTerminal`, `TerminalOutput`, `ReleaseTerminal`, `WaitForTerminalExit`,
  `KillTerminal`) and the elicitation methods (`CreateElicitation`,
  `CompleteElicitation`). A client that does not offer a capability returns
  `acp.NewMethodNotFound` from its methods and leaves the capability out of
  `ClientCapabilities`. Every other client method, such as the unstable
  `mcp/message`, has its own optional interface (`acp.ClientUnstableMessageMcp`).
- Launch or connect to your Agent process (stdio), then create a connection with
  `acp.NewClientSideConnection(client, stdin, stdout)`.
- Call `Initialize`, `NewSession`, and `Prompt` to run a turn and stream updates.

Helper constructors are provided to reduce boilerplate when working with union types:

- Content blocks: `acp.TextBlock`, `acp.ImageBlock`, `acp.AudioBlock`,
  `acp.ResourceLinkBlock`, `acp.ResourceBlock`.
- Tool content: `acp.ToolContent`, `acp.ToolDiffContent`, `acp.ToolTerminalRef`.
- Utility: `acp.Ptr[T]` for pointer fields in request/update structs.

### Extension methods

ACP supports **extension methods** for custom JSON-RPC methods whose names start with `_`.
Use them to add functionality without conflicting with future ACP versions.

#### Handling inbound extension methods

Implement `acp.ExtensionMethodHandler` on your Agent or Client. Your handler will be
invoked for any incoming method starting with `_`.

```go
// HandleExtensionMethod handles ACP extension methods (names starting with "_").
func (a MyAgent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "_example.com/hello":
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return map[string]any{"greeting": "hello " + p.Name}, nil
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}
```

> Note: Per the ACP spec, unknown extension notifications should be ignored.
> This SDK suppresses noisy logs for unhandled extension notifications that return
> “Method not found”.

#### Calling extension methods

From either side, use `CallExtension` / `NotifyExtension` on the connection.

```go
raw, err := conn.CallExtension(ctx, "_example.com/hello", map[string]any{"name": "world"})
if err != nil {
	return err
}

var resp struct {
	Greeting string `json:"greeting"`
}
if err := json.Unmarshal(raw, &resp); err != nil {
	return err
}

if err := conn.NotifyExtension(ctx, "_example.com/progress", map[string]any{"pct": 50}); err != nil {
	return err
}
```

> Note: A request returns after the handlers of all notifications that arrived
> before its response have returned. To make a request from a notification
> handler, such as `SessionUpdate`, pass the handler's `ctx` or a ctx derived
> from it. With any other ctx, the request waits for the handler that made it,
> so it returns only when that ctx ends or the peer disconnects. A goroutine
> that the handler starts with the handler's `ctx` also skips the wait for
> later notifications while the handler runs: give it a ctx that does not
> derive from the handler's `ctx` if it needs the full wait.

#### Advertising extension support via `_meta`

ACP uses the `_meta` field inside capability objects as the negotiation/advertising
surface for extensions.

- Client -> Agent: `InitializeRequest.ClientCapabilities.Meta`
- Agent -> Client: `InitializeResponse.AgentCapabilities.Meta`

`Meta` keeps each value as raw JSON. `Set` encodes a Go value and `Get` decodes one:

```go
caps := acp.ClientCapabilities{}
if err := caps.Meta.Set("example.com/feature", map[string]any{"version": 2}); err != nil {
	return err
}

var feature struct{ Version int }
if err := resp.AgentCapabilities.Meta.Get("example.com/feature", &feature); err == nil {
	// The agent supports the extension.
}
```

The [ACP spec](https://agentclientprotocol.com/protocol/extensibility#the-_meta-field)
reserves the root-level `_meta` keys `traceparent`, `tracestate`, and `baggage` for W3C
trace context/OpenTelemetry compatibility. This SDK does not read or set these keys.

### Study a Production Implementation

For a complete, production‑ready integration, see the
[Gemini CLI Agent](https://github.com/google-gemini/gemini-cli) which exposes an
ACP interface. The Go example client `example/gemini` demonstrates connecting
to it via stdio.

## System Overview

Package `acp` (module `github.com/coder/acp-go-sdk`, `go 1.21`, no `require` directives) implements both ends of ACP as JSON-RPC 2.0 messages, one JSON object per line.
`Connection` reads lines from the peer, routes requests, responses, and notifications, and serializes writes [connection.go:Connection].
`AgentSideConnection` and `ClientSideConnection` wrap a `Connection` and dispatch inbound methods to your `Agent` or `Client` implementation [agent.go:AgentSideConnection] [client.go:ClientSideConnection].
The wire types, method-name constants, inbound dispatch switches, outbound wrapper methods, and union constructors live in the `*_gen.go` files.
`cmd/generate` writes these files from the schemas in `schema/`, and `schema/version` pins the ACP release.
The hand-written files add errors, extension methods, and builder helpers.

## Local Interface & Invariants

### Interface

- Connections: `NewAgentSideConnection(agent Agent, peerInput io.Writer, peerOutput io.Reader) *AgentSideConnection` and `NewClientSideConnection(client Client, peerInput io.Writer, peerOutput io.Reader) *ClientSideConnection`. Both types have `Done() <-chan struct{}` and `SetLogger(*slog.Logger)` [agent.go:NewAgentSideConnection] [client.go:NewClientSideConnection]
- Interfaces you implement: `Agent` and `Client`, whose methods are the stable methods of ACP schema 1.25.0, and one optional interface for each other method, named after the side and the Go method, such as `AgentUnstableForkSession` and `ClientUnstableMessageMcp`. The optional interface of `LoadSession` keeps its name `AgentLoader`. A schema release never adds a method to `Agent` or `Client`: a method that it adds or makes stable gets its own optional interface [types_gen.go:Agent, AgentLoader, Client] [cmd/generate/internal/ir/baseline.go:requiredMethods]. Optionally `ExtensionMethodHandler` for `_`-prefixed methods [extensions.go:ExtensionMethodHandler]
- Outbound calls: `ClientSideConnection` has one method per agent method, for example `Initialize`, `NewSession`, `Prompt`, and `Cancel` [client_gen.go:Initialize, NewSession, Prompt, Cancel]. `AgentSideConnection` has one method per client method, for example `SessionUpdate`, `RequestPermission`, `ReadTextFile`, and `CreateTerminal` [agent_gen.go:SessionUpdate, RequestPermission, ReadTextFile, CreateTerminal]. Both types have `CallExtension` and `NotifyExtension` [extensions.go:CallExtension, NotifyExtension]
- `mcp/message` (unstable) has two forms. An agent sends the request with `(*AgentSideConnection).UnstableMessageMcp`, and a client answers it in `ClientUnstableMessageMcp`. A client sends the notification with `(*ClientSideConnection).UnstableMessageMcp`, and an agent receives it in `AgentUnstableMessageMcp` [agent_gen.go:handle, UnstableMessageMcp] [client_gen.go:handle, UnstableMessageMcp]
- Low-level transport: `Connection`, `NewConnection(handler MethodHandler, peerInput io.Writer, peerOutput io.Reader)`, `MethodHandler`, `SendRequest[T]` (a generic function that takes the `*Connection`), `(*Connection).SendRequestNoResult`, `(*Connection).SendNotification`, `(*Connection).SetLogger`, and `(*Connection).Done` [connection.go:NewConnection, MethodHandler, SendRequest, SendRequestNoResult, SendNotification, SetLogger, Done]
- Wire types: one struct per request, response, and notification, union wrappers with one pointer field per variant, and named `string` enums with constants such as `StopReason`, `ToolKind`, `ToolCallStatus`, and `PermissionOptionKind` [types_gen.go:StopReason, ToolKind, ToolCallStatus, PermissionOptionKind]. `ExtNotification`, `ExtRequest`, and `ExtResponse` are aliases of `json.RawMessage`, so they have no generated fields and no `Validate` [types_gen.go:ExtNotification, ExtRequest, ExtResponse]
- Free-form values: a value that the schema leaves free holds the JSON as it was read, so a number keeps every digit. `_meta` is a `Meta`, a `map[string]json.RawMessage` with `NewMeta`, `Get`, and `Set`, and an empty one is left out of the message. `Get` of a missing key returns an error that wraps `ErrMetaKeyNotFound`. `rawInput`, `rawOutput`, the `data` of an error (`RequestError.Data` included), and the result of `mcp/message` are `json.RawMessage`, and the params of `mcp/message` are a `map[string]json.RawMessage` [meta.go:Meta, NewMeta, Get, Set] [types_gen.go:ToolCall, UnstableMcpError] [errors.go:RequestError]. `RequestError.Data` keeps `"data": null`, while the generated `Error` reads it as no data, as its schema asks with `x-deserialize-default-on-error` [errors_test.go:TestRequestError_KeepsNullDataThatErrorDrops]
- Typed maps: a map whose members have a schema takes their Go type. `ElicitationSchema.Properties` is a `map[string]ElicitationPropertySchema`, the `Content` of an accepted elicitation is a `map[string]ElicitationContentValue`, and `env` and `headers` are `map[string]string`. `NewElicitationContentValueString`, `...Integer` (an `int64`, as the schema's `format: int64` asks), `...Number`, `...Boolean`, and `...StringArray` build a content value [types_gen.go:ElicitationSchema, ElicitationAcceptAction] [helpers.go:NewElicitationContentValueString]
- Patch fields: the optional fields of an update that changes a stored value, such as `ToolCallUpdate`, `SessionInfoUpdate`, `CompactionUpdate`, and the unstable `SubagentUpdate` and `SessionMessage`, are `Patch[T]`. Its zero value is unset (the member is absent: unchanged), `PatchNull[T]()` writes `null` (clear), `PatchSet(v)` writes `v`, a nil slice or map as `[]` or `{}` (but a nil `json.RawMessage` as `null`), and `PatchFromPtr(p)` sets `*p`, or leaves the field unset for a nil `p`. Read one with `Value() (T, bool)`, `Ptr()` (nil unless set), `IsNull()`, and `IsUnset()`. In ACP v1 a `null` in a `ToolCallUpdate` does not clear: the protocol docs say that it leaves `name`, `rawInput` and `rawOutput` unchanged, and the Rust SDK reads `null` in every `ToolCallUpdate` field as absent. Treat `IsNull()` there like `IsUnset()`. A set `Patch` whose value encodes as JSON `null`, such as a nil `json.RawMessage`, is `null` on the wire and reads back as `IsNull()`. The generated structs leave an unset field out; in a struct of your own, tag a `Patch` field `omitzero` (Go 1.24 or later), since `omitempty` writes an unset one as `null`. The `WithUpdate*` options set their field with `PatchSet`, except that a nil slice to `WithUpdateContent` or `WithUpdateLocations`, or a nil value to `WithUpdateRawInput` or `WithUpdateRawOutput`, leaves it unset. A value that `json.Marshal` cannot encode, such as a channel or a NaN, makes an update from `WithUpdateRawInput` or `WithUpdateRawOutput` fail to encode, so sending it returns the error, while `WithStartRawInput` and `WithStartRawOutput` leave it out, because their field is a plain `json.RawMessage` [patch.go:Patch, PatchFromPtr] [helpers.go:WithUpdateContent, WithUpdateRawInput, WithStartRawInput]
- Unstable types: one Go type per shape. The Request, Response, and Notification types of unstable-only methods, and the definitions that only they reach, get the `Unstable` prefix, such as `UnstableForkSessionRequest` and `UnstableProviderInfo`. A type that stable and unstable methods share keeps its name and carries the unstable additions, each with a doc comment that starts with `**UNSTABLE**`, as the Rust SDK gates them behind a feature: `SessionConfigOption`, `McpServer` with its `Acp` variant, and the constant `SessionConfigOptionCategorySpeed`. Definitions that exist only in `schema/schema.unstable.json` and that stable methods reach keep their names too, for example `SubagentUpdate` and `ErrorStopReason` [cmd/generate/internal/load/merge.go:buildUnstableDuplicateSet, promoteChangedSharedDefs, markUnstableAdditions, ensureRefsAvailable]
- Constants: `ProtocolVersionNumber` (`1`), `SchemaVersion` (the ACP schema release in `schema/version`), plus 24 `AgentMethod*`, 13 `ClientMethod*`, and 1 `ProtocolMethod*` wire-name constants, for example `AgentMethodSessionPrompt` (`session/prompt`) and `ProtocolMethodCancelRequest` (`$/cancel_request`) [constants_gen.go:ProtocolVersionNumber, AgentMethodSessionPrompt]
- Errors: `RequestError` (`Code`, `Message`, and `Data`, the raw JSON of the error's data) and its constructors `NewRequestError(code, message, data)`, `NewParseError` (-32700), `NewInvalidRequest` (-32600), `NewMethodNotFound` (-32601), `NewInvalidParams` (-32602), `NewInternalError` (-32603), `NewRequestCancelled` (-32800), and `NewAuthRequired` (-32000) [errors.go:RequestError]. Each constructor encodes its `data` argument as JSON; a nil one, or one that does not encode, gives no `Data`. `NewMethodNotFound` sets `Data` to `{"method": method}` [errors.go:NewRequestError, NewMethodNotFound]. `(*RequestError).Error` returns the JSON of code, message, and data, `<nil>` for a nil receiver, and the text `code %d: %s` (with ` (data: %s)` appended when `Data` is set) when `Data` is not valid JSON [errors.go:Error]
- Builders: content blocks (`TextBlock`, `ImageBlock`, `AudioBlock`, `ResourceLinkBlock`, `ResourceBlock`), tool content (`ToolContent`, `ToolDiffContent`, `ToolTerminalRef`), session updates (`UpdateUserMessage`, `UpdateAgentMessage`, `UpdateAgentThought`, their `*Text` forms, and `UpdatePlan`), tool calls (`StartToolCall`, `StartReadToolCall`, and `StartEditToolCall` with `WithStart*` options, `UpdateToolCall` with `WithUpdate*` options), and `Ptr[T]` [helpers.go]. Generated `New<Union><Variant>` constructors, for example `NewRequestPermissionOutcomeSelected` [helpers_gen.go:NewRequestPermissionOutcomeSelected]

### Invariants

- The `*_gen.go` files start with `Code generated by acp-go-generator; DO NOT EDIT.` Change `cmd/generate` instead of editing them, then run `make generate` [Makefile:generate] [constants_gen.go] [types_gen.go] [agent_gen.go] [client_gen.go] [helpers_gen.go]
- Notifications from the peer run one at a time, in arrival order, on one goroutine [connection.go:processNotifications]
- A request from the peer starts its handler only after the handlers of all notifications that arrived before it have finished. There are two exceptions. First, it also starts while the running notification handler waits for a peer response, because the peer can need this answer first. Second, when the peer disconnects during the wait, the handler starts at once with a cancelled ctx, so it can clean up while earlier notification handlers still run [connection.go:receive, waitInboundBarrier, waitNotifications, startPeerWait]
- After a response arrives, `SendRequest` and `SendRequestNoResult` return only after the handlers of all notifications that arrived before the response have finished, or when the ctx ends or the peer disconnects. Before a response, they return at once when the params do not marshal (`-32602`) or the write fails (`-32603`). In a notification handler, a request made with the handler's ctx (or a ctx derived from it) waits only for the earlier notifications [connection.go:SendRequest, SendRequestNoResult, prepareRequest]
- The read loop handles `$/cancel_request` from the peer at once, outside notification order. It cancels the in-flight request with the same id and ignores an unknown id [connection.go:receive, handleCancelRequest]
- When the ctx of an outbound request ends before the response, the connection sends `$/cancel_request` for that id to the peer with `context.Background()`. At most 1024 cancels wait in the queue (`maxPendingCancelRequests`). Extra cancels are dropped with a Debug log [connection.go:waitForResponse, sendCancelRequest, sendCancelRequests]
- JSON-RPC ids must be a string or a number. A `null` id unmarshals as no id: a request with `"id": null` runs as a notification and gets no reply, and a response with `"id": null` is logged and skipped. Numeric ids are canonicalized, so equal values match one pending request. A numeric id can have at most 4096 digits in canonical form and an exponent magnitude of at most 4096 (`maxCanonicalJSONRPCIDKeyLen`, `maxCanonicalJSONRPCIDAbsExp10`). When an inbound request id fails this check, the connection logs it and uses the raw id text [connection.go:canonicalJSONRPCIDKey, receive]
- The connection closes when more than 1024 notifications are queued (`defaultMaxQueuedNotifications`) or when an input line, without its newline, is 10 MiB (`maxBufSize`) or longer. Other bad input does not close it: the read loop skips blank lines, and it logs and skips a line that is not valid JSON or a message with neither id nor method [connection.go:receive]
- On disconnect, `Done()` closes at once. Waiting callers fail, and the contexts of inbound request handlers (derived from the connection context) are cancelled at the same time. Queued notifications then drain for up to 5 s, and only then are the contexts of notification handlers cancelled [connection.go:shutdownReceive, receive, processNotifications, notificationQueueDrainTimeout]
- For the generated ACP methods, inbound params are unmarshalled into the generated type and checked with `Validate()`. Extension methods (`_`-prefixed) reach `ExtensionMethodHandler` as the raw `json.RawMessage`, so the handler must decode and validate them itself [extensions.go:handleWithExtensions]. For a request, a failure returns `-32602` (`NewInvalidParams`), and an unknown method returns `-32601` (`NewMethodNotFound`), as does a method of an optional interface that your implementation does not have. A notification never gets a reply: the connection logs a decode or handler failure at Error instead, and an unknown or unimplemented notification (`-32601`) at Debug as `ignored unhandled notification` [agent_gen.go:handle] [client_gen.go:handle] [connection.go:handleInbound]
- Handler errors pass through `toReqErr`: a `*RequestError` is sent unchanged, `context.Canceled` becomes `-32800`, and any other error becomes `-32603` [errors.go:toReqErr]. A `*RequestError` whose `Data` is not valid JSON does not encode, so the request gets a `-32603` response instead [connection.go:handleInbound]
- Extension method names must start with `_`. `CallExtension` and `NotifyExtension` reject other names before they send. Inbound `_` methods go to `ExtensionMethodHandler`. When it is not implemented, a `_` request gets `-32601`, and a `_` notification is dropped with no reply and no log line. Other unhandled notifications are logged at Debug [extensions.go:validateExtensionMethodName, handleWithExtensions] [connection.go:handleInbound]
- Agent side: each inbound `session/prompt` runs with its own cancellable ctx, and an inbound `session/cancel` cancels that ctx before `Agent.Cancel` runs. A prompt that arrived before a cancel but registers after it is cancelled at once. At most 1024 of these cancel markers are kept [agent_gen.go:handle] [agent.go:registerPrompt, cancelPrompt, maxCancelMarkers]
- Client side: when `ClientSideConnection.Prompt` fails after its ctx ended, it sends `session/cancel` with `context.Background()` [client_gen.go:Prompt]
- Union wrappers: `Validate` on a `oneOf` union requires exactly one variant to be set. It also rejects a set variant that leaves a required `string` or `array` property of the union itself unset, such as the `message` of `UnstableCreateElicitationRequest` or the `name` of `SessionConfigOption`. `UnmarshalJSON` returns `no matching variant for union` when no variant fits [types_gen.go:ContentBlock.Validate, UnstableCreateElicitationRequest.Validate, SessionUpdate.UnmarshalJSON]
- Capability structs fill schema defaults on decode when a key is missing or `null`. For example, a missing `ClientCapabilities.fs` becomes `{"readTextFile":false,"writeTextFile":false}` [types_gen.go:ClientCapabilities.UnmarshalJSON]
- `StartReadToolCall` applies kind `read`, status `pending`, one location, and `rawInput {"path"}` before the caller's options. `StartEditToolCall` applies kind `edit`, status `pending`, one location, and `rawInput {"content", "path"}` [helpers.go:StartReadToolCall, StartEditToolCall]

### Side Effects

- `NewConnection` starts 4 goroutines: a ctx-done broadcaster, the cancel sender, the read loop, and the notification processor. Each inbound request runs in its own goroutine [connection.go:NewConnection, receive]
- The read loop reads the peer output with a `bufio.Scanner` (1 MiB initial buffer, 10 MiB maximum line). `sendMessage` writes each message plus `\n` to the peer input while it holds `writeMu` [connection.go:receive, sendMessage]
- Diagnostics go to the logger from `SetLogger`, or to `slog.Default()` when none is set [connection.go:loggerOrDefault]
- The connection panics when an internal sequence invariant breaks, for example a notification that would run out of order [connection.go:processNotifications, startPeerWait]
- `WithStartLocations` with exactly one location that has a non-empty `Path` mirrors that path into `RawInput`. It creates `{"path": ...}` when `RawInput` is nil, adds a `path` member to a `RawInput` object that has none, and leaves any other `RawInput` unchanged [helpers.go:WithStartLocations, withPath]

## Subdirectories & System Boundaries

- cmd: [./cmd/README.md](./cmd/README.md) - Command-line programs. `cmd/generate` is a separate Go module that reads `schema/` and writes the root `*_gen.go` files.
- example: [./example/README.md](./example/README.md) - Four `package main` programs on the public `acp` API: a scripted agent, a demo client, and REPL clients for Claude Code and the Gemini CLI.
- schema (no contract file): the ACP JSON schemas (`meta.json`, `schema.json`, `meta.unstable.json`, `schema.unstable.json`) and the `version` pin. Input of `cmd/generate`.
- testdata (no contract file): golden JSON payloads in `testdata/json_golden`, used by `json_parity_test.go`.

## Global Invariants

- `version` holds the SDK's own semver, and `schema/version` pins the ACP schema release. They move apart: a release can change the SDK without a new schema. `make generate` and `make release` never derive one from the other. `cmd/generate` writes `schema/version` into the `SchemaVersion` constant, and the `mdsh` block in this README prints the `go get` line from `version`. A schema bump commits the schemas, the generated Go, and `schema/version` together [Makefile:generate, release] [cmd/generate/main.go:main]
- The generated `handle` switches use unexported names from the hand-written root files: `registerPrompt`, `cancelPrompt`, `sessionCancels`, and `mu` [agent.go], and `toReqErr` [errors.go]. Rename them only together with `cmd/generate/internal/emit` [cmd/generate/internal/emit/dispatch.go] [cmd/generate/internal/emit/dispatch_helpers.go]
- `cmd/generate` emits no constructors for `ContentBlock`, `ToolCallContent`, and `SessionUpdate`. Their builders are hand-written in `helpers.go` [cmd/generate/internal/emit/helpers.go:WriteHelpersJen]
- The root module uses only the standard library. Only `cmd/generate` depends on `github.com/dave/jennifer` [go.mod] [cmd/generate/go.mod]
- The examples import only `github.com/coder/acp-go-sdk` from this module, so they exercise the public API only [example/agent/main.go] [example/client/main.go] [example/claude-code/main.go] [example/gemini/main.go]

## Global Side Effects

- `make generate` downloads a schema file from `https://github.com/agentclientprotocol/agent-client-protocol/releases` only when that file is older than `schema/version`. A 0.x schema comes from the `v<version>` release tag, and 1.0 or later from `schema-v<version>`. When the release has no unstable schema file (HTTP 404), it writes an empty placeholder (`{"agentMethods":{},"clientMethods":{},"protocolMethods":{}}` or `{"$defs":{}}`) and continues. It then runs `cmd/generate`, which overwrites the five `*_gen.go` files. `make release VERSION=<sdk> [SCHEMA=<schema>]` writes `version` (and `schema/version`), then runs `generate`, `fmt`, and `test` [Makefile:generate, release]
- The SDK speaks ACP only over the `io.Reader` and `io.Writer` you pass in. Its diagnostics (for example an Info line on every disconnect, and Error lines for malformed input) go to `slog.Default()`, which writes to stderr unless you change it, or to the logger set with `SetLogger`. The root package opens no file, network connection, or subprocess. The examples do: they start child processes (`npx`, `gemini`, `go run`) and read and write local files when the agent asks

## System Verification

- Run: `make test` (runs `go vet ./...`, `go test ./...`, `go build ./example/...`, `go vet` and `go test` in the separate `cmd/generate` module, and the `acpwire` driver tests)
- Run: `make check` (runs `treefmt`, then `git diff --exit-code`. It needs the formatters from `mise install`)

## Resources

- [Go package docs](https://pkg.go.dev/github.com/coder/acp-go-sdk)
- [Examples (Go)](https://github.com/coder/acp-go-sdk/tree/main/example)
- [Protocol Documentation](https://agentclientprotocol.com)
- [Agent Client Protocol GitHub Repository](https://github.com/agentclientprotocol/agent-client-protocol)

## License

Apache 2.0. See [LICENSE](./LICENSE).
