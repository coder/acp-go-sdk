<a href="https://agentclientprotocol.com/" >
  <img alt="Agent Client Protocol" src="https://zed.dev/img/acp/banner-dark.webp">
</a>

# ACP Go SDK

Go library for the Agent Client Protocol (ACP) - a standardized communication protocol
between code editors and AI‑powered coding agents.

Learn more about the protocol itself at <https://agentclientprotocol.com>.

## Installation

<!-- `$ printf 'go get github.com/coder/acp-go-sdk@v%s\n' "$(cat schema/version)"` as bash -->

```bash
go get github.com/coder/acp-go-sdk@v0.13.5
```

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

- Implement the `acp.Agent` interface (and optionally `acp.AgentLoader` for `session/load`).
- Create a connection with `acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)`.
- Send updates and make client requests using the returned connection.

If you're building a [Client](https://agentclientprotocol.com/protocol/overview#client):

- Implement the `acp.Client` interface. It includes the terminal methods
  (`CreateTerminal`, `TerminalOutput`, `ReleaseTerminal`, `WaitForTerminalExit`,
  `KillTerminal`). Optionally implement `acp.ClientExperimental` for the unstable
  client methods.
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
- Interfaces you implement: `Agent`, `AgentLoader` (`LoadSession`), `AgentExperimental` (the `Unstable*` agent methods), `Client`, and `ClientExperimental` (the `Unstable*` client methods) [types_gen.go:Agent, AgentLoader, AgentExperimental, Client, ClientExperimental]. Optionally `ExtensionMethodHandler` for `_`-prefixed methods [extensions.go:ExtensionMethodHandler]
- Outbound calls: `ClientSideConnection` has one method per agent method, for example `Initialize`, `NewSession`, `Prompt`, and `Cancel` [client_gen.go:Initialize, NewSession, Prompt, Cancel]. `AgentSideConnection` has one method per client method, for example `SessionUpdate`, `RequestPermission`, `ReadTextFile`, and `CreateTerminal` [agent_gen.go:SessionUpdate, RequestPermission, ReadTextFile, CreateTerminal]. Both types have `CallExtension` and `NotifyExtension` [extensions.go:CallExtension, NotifyExtension]
- Exception: `mcp/message` has the constants `AgentMethodMcpMessage` and `ClientMethodMcpMessage`, but no wrapper method and no dispatch case ([#76](https://github.com/coder/acp-go-sdk/issues/76)). An inbound `mcp/message` request gets a `-32601` reply from the `default` case. The notification form gets no reply, and the connection logs it at Error [constants_gen.go:AgentMethodMcpMessage, ClientMethodMcpMessage] [agent_gen.go:handle] [client_gen.go:handle] [connection.go:handleInbound]
- Low-level transport: `Connection`, `NewConnection(handler MethodHandler, peerInput io.Writer, peerOutput io.Reader)`, `MethodHandler`, `SendRequest[T]` (a generic function that takes the `*Connection`), `(*Connection).SendRequestNoResult`, `(*Connection).SendNotification`, `(*Connection).SetLogger`, and `(*Connection).Done` [connection.go:NewConnection, MethodHandler, SendRequest, SendRequestNoResult, SendNotification, SetLogger, Done]
- Wire types: one struct per request, response, and notification, union wrappers with one pointer field per variant, and named `string` enums with constants such as `StopReason`, `ToolKind`, `ToolCallStatus`, and `PermissionOptionKind` [types_gen.go:StopReason, ToolKind, ToolCallStatus, PermissionOptionKind]. `ExtNotification`, `ExtRequest`, `ExtResponse`, and `UnstableMessageMcpResponse` are `any`, so they have no generated fields and no `Validate` [types_gen.go:ExtNotification, ExtRequest, ExtResponse, UnstableMessageMcpResponse]
- Unstable types: the types of unstable-only methods, and the definitions they reach that are new or changed, get the `Unstable` prefix. Definitions that stable methods reach keep their schema names, even when they exist only in `schema/schema.unstable.json`, for example `Cost` and `AuthEnvVar` [cmd/generate/internal/load/merge.go:buildUnstableDuplicateSet, promoteChangedSharedDefs, ensureRefsAvailable]
- Constants: `ProtocolVersionNumber` (`1`), plus 28 `AgentMethod*` and 14 `ClientMethod*` wire-name constants, for example `AgentMethodSessionPrompt` (`session/prompt`) [constants_gen.go:ProtocolVersionNumber, AgentMethodSessionPrompt]
- Errors: `RequestError` (`Code`, `Message`, `Data`) and its constructors `NewParseError` (-32700), `NewInvalidRequest` (-32600), `NewMethodNotFound` (-32601), `NewInvalidParams` (-32602), `NewInternalError` (-32603), `NewRequestCancelled` (-32800), and `NewAuthRequired` (-32000) [errors.go:RequestError]. `NewMethodNotFound` sets `Data` to `{"method": method}` [errors.go:NewMethodNotFound]. `(*RequestError).Error` returns the JSON of code, message, and data, `<nil>` for a nil receiver, and the text `code %d: %s` (with ` (data: %v)` appended when `Data` is set) when marshalling fails [errors.go:Error]
- Builders: content blocks (`TextBlock`, `ImageBlock`, `AudioBlock`, `ResourceLinkBlock`, `ResourceBlock`), tool content (`ToolContent`, `ToolDiffContent`, `ToolTerminalRef`), session updates (`UpdateUserMessage`, `UpdateAgentMessage`, `UpdateAgentThought`, their `*Text` forms, and `UpdatePlan`), tool calls (`StartToolCall`, `StartReadToolCall`, and `StartEditToolCall` with `WithStart*` options, `UpdateToolCall` with `WithUpdate*` options), and `Ptr[T]` [helpers.go]. Generated `New<Union><Variant>` constructors, for example `NewRequestPermissionOutcomeSelected` [helpers_gen.go:NewRequestPermissionOutcomeSelected]

### Invariants

- The `*_gen.go` files start with `Code generated by acp-go-generator; DO NOT EDIT.` Change `cmd/generate` instead of editing them, then run `cd cmd/generate && env -u GOPATH -u GOMODCACHE go run .`. `make version` runs the generator too, but only when `version` is older than a schema file or this README, so a generator change alone does not trigger it [constants_gen.go] [types_gen.go] [agent_gen.go] [client_gen.go] [helpers_gen.go]
- Notifications from the peer run one at a time, in arrival order, on one goroutine [connection.go:processNotifications]
- A request from the peer starts its handler only after the handlers of all notifications that arrived before it have finished. There are two exceptions. First, it also starts while the running notification handler waits for a peer response, because the peer can need this answer first. Second, when the peer disconnects during the wait, the handler starts at once with a cancelled ctx, so it can clean up while earlier notification handlers still run [connection.go:receive, waitInboundBarrier, waitNotifications, startPeerWait]
- After a response arrives, `SendRequest` and `SendRequestNoResult` return only after the handlers of all notifications that arrived before the response have finished, or when the ctx ends or the peer disconnects. Before a response, they return at once when the params do not marshal (`-32602`) or the write fails (`-32603`). In a notification handler, a request made with the handler's ctx (or a ctx derived from it) waits only for the earlier notifications [connection.go:SendRequest, SendRequestNoResult, prepareRequest]
- The read loop handles `$/cancel_request` from the peer at once, outside notification order. It cancels the in-flight request with the same id and ignores an unknown id [connection.go:receive, handleCancelRequest]
- When the ctx of an outbound request ends before the response, the connection sends `$/cancel_request` for that id to the peer with `context.Background()`. At most 1024 cancels wait in the queue (`maxPendingCancelRequests`). Extra cancels are dropped with a Debug log [connection.go:waitForResponse, sendCancelRequest, sendCancelRequests]
- JSON-RPC ids must be a string or a number. A `null` id unmarshals as no id: a request with `"id": null` runs as a notification and gets no reply, and a response with `"id": null` is logged and skipped. Numeric ids are canonicalized, so equal values match one pending request. A numeric id can have at most 4096 digits in canonical form and an exponent magnitude of at most 4096 (`maxCanonicalJSONRPCIDKeyLen`, `maxCanonicalJSONRPCIDAbsExp10`). When an inbound request id fails this check, the connection logs it and uses the raw id text [connection.go:canonicalJSONRPCIDKey, receive]
- The connection closes when more than 1024 notifications are queued (`defaultMaxQueuedNotifications`) or when an input line, without its newline, is 10 MiB (`maxBufSize`) or longer. Other bad input does not close it: the read loop skips blank lines, and it logs and skips a line that is not valid JSON or a message with neither id nor method [connection.go:receive]
- On disconnect, `Done()` closes at once. Waiting callers fail, and the contexts of inbound request handlers (derived from the connection context) are cancelled at the same time. Queued notifications then drain for up to 5 s, and only then are the contexts of notification handlers cancelled [connection.go:shutdownReceive, receive, processNotifications, notificationQueueDrainTimeout]
- For the generated ACP methods, inbound params are unmarshalled into the generated type and checked with `Validate()`. Extension methods (`_`-prefixed) reach `ExtensionMethodHandler` as the raw `json.RawMessage`, so the handler must decode and validate them itself [extensions.go:handleWithExtensions]. For a request, a failure returns `-32602` (`NewInvalidParams`), and an unknown method returns `-32601` (`NewMethodNotFound`), as does a method of `AgentLoader`, `AgentExperimental`, or `ClientExperimental` that your implementation does not have. A notification never gets a reply: the connection logs these errors at Error instead [agent_gen.go:handle] [client_gen.go:handle] [connection.go:handleInbound]
- Handler errors pass through `toReqErr`: a `*RequestError` is sent unchanged, `context.Canceled` becomes `-32800`, and any other error becomes `-32603` [errors.go:toReqErr]
- Extension method names must start with `_`. `CallExtension` and `NotifyExtension` reject other names before they send. Inbound `_` methods go to `ExtensionMethodHandler`. When it is not implemented, a `_` request gets `-32601`, and a `_` notification is dropped with no reply and no log line. Other unhandled notifications are logged at Error [extensions.go:validateExtensionMethodName, handleWithExtensions] [connection.go:handleInbound]
- Agent side: each inbound `session/prompt` runs with its own cancellable ctx, and an inbound `session/cancel` cancels that ctx before `Agent.Cancel` runs. A prompt that arrived before a cancel but registers after it is cancelled at once. At most 1024 of these cancel markers are kept [agent_gen.go:handle] [agent.go:registerPrompt, cancelPrompt, maxCancelMarkers]
- Client side: when `ClientSideConnection.Prompt` fails after its ctx ended, it sends `session/cancel` with `context.Background()` [client_gen.go:Prompt]
- Union wrappers: `Validate` on a `oneOf` union requires exactly one variant to be set, and `UnmarshalJSON` returns `no matching variant for union` when no variant fits [types_gen.go:ContentBlock.Validate, SessionUpdate.UnmarshalJSON]
- Capability structs fill schema defaults on decode when a key is missing or `null`. For example, a missing `ClientCapabilities.fs` becomes `{"readTextFile":false,"writeTextFile":false}` [types_gen.go:ClientCapabilities.UnmarshalJSON]
- `StartReadToolCall` applies kind `read`, status `pending`, one location, and `rawInput {"path"}` before the caller's options. `StartEditToolCall` applies kind `edit`, status `pending`, one location, and `rawInput {"content", "path"}` [helpers.go:StartReadToolCall, StartEditToolCall]

### Side Effects

- `NewConnection` starts 4 goroutines: a ctx-done broadcaster, the cancel sender, the read loop, and the notification processor. Each inbound request runs in its own goroutine [connection.go:NewConnection, receive]
- The read loop reads the peer output with a `bufio.Scanner` (1 MiB initial buffer, 10 MiB maximum line). `sendMessage` writes each message plus `\n` to the peer input while it holds `writeMu` [connection.go:receive, sendMessage]
- Diagnostics go to the logger from `SetLogger`, or to `slog.Default()` when none is set [connection.go:loggerOrDefault]
- The connection panics when an internal sequence invariant breaks, for example a notification that would run out of order [connection.go:processNotifications, startPeerWait]
- `WithStartLocations` with exactly one location that has a non-empty `Path` mirrors that path into `RawInput`. It creates `{"path": ...}` when `RawInput` is nil, adds a `path` key to a caller-supplied `map[string]any` that has none, and leaves any other `RawInput` unchanged [helpers.go:WithStartLocations]

## Subdirectories & System Boundaries

- cmd: [./cmd/README.md](./cmd/README.md) - Command-line programs. `cmd/generate` is a separate Go module that reads `schema/` and writes the root `*_gen.go` files.
- example: [./example/README.md](./example/README.md) - Four `package main` programs on the public `acp` API: a scripted agent, a demo client, and REPL clients for Claude Code and the Gemini CLI.
- schema (no contract file): the ACP JSON schemas (`meta.json`, `schema.json`, `meta.unstable.json`, `schema.unstable.json`) and the `version` pin. Input of `cmd/generate`.
- testdata (no contract file): golden JSON payloads in `testdata/json_golden`, used by `json_parity_test.go`.

## Global Invariants

- `schema/version` pins the ACP release. `make version` regenerates the `mdsh` block in this README, downloads the schemas of that release, runs `cmd/generate`, runs `gofumpt -w .`, and writes the root `version` stamp. Commit the schemas, the generated Go, and `version` together. `make release` fails when `schema/version` and `version` differ [Makefile:version, release]
- The generated `handle` switches use unexported names from the hand-written root files: `registerPrompt`, `cancelPrompt`, `sessionCancels`, and `mu` [agent.go], and `toReqErr` [errors.go]. Rename them only together with `cmd/generate/internal/emit` [cmd/generate/internal/emit/dispatch.go] [cmd/generate/internal/emit/dispatch_helpers.go]
- `cmd/generate` emits no constructors for `ContentBlock`, `ToolCallContent`, and `SessionUpdate`. Their builders are hand-written in `helpers.go` [cmd/generate/internal/emit/helpers.go:WriteHelpersJen]
- The root module uses only the standard library. Only `cmd/generate` depends on `github.com/dave/jennifer` [go.mod] [cmd/generate/go.mod]
- The examples import only `github.com/coder/acp-go-sdk` from this module, so they exercise the public API only [example/agent/main.go] [example/client/main.go] [example/claude-code/main.go] [example/gemini/main.go]

## Global Side Effects

- `make version` is a file-stamp target: it runs only when a prerequisite is newer than `version`. It downloads a schema file from `https://github.com/agentclientprotocol/agent-client-protocol/releases` only when that file is older than `schema/version`. When the release has no unstable schema file (HTTP 404), it writes an empty placeholder (`{"agentMethods":{},"clientMethods":{},"protocolMethods":{}}` or `{"$defs":{}}`) and continues. Its first prerequisite rewrites this README through `mdsh` when `schema/version` is newer than the README. The recipe then runs `cmd/generate`, which overwrites the five `*_gen.go` files, reformats the Go files with `gofumpt`, and writes `version` [Makefile:version, README.md]
- The SDK speaks ACP only over the `io.Reader` and `io.Writer` you pass in. Its diagnostics (for example an Info line on every disconnect, and Error lines for malformed input) go to `slog.Default()`, which writes to stderr unless you change it, or to the logger set with `SetLogger`. The root package opens no file, network connection, or subprocess. The examples do: they start child processes (`npx`, `gemini`, `go run`) and read and write local files when the agent asks

## System Verification

- Run: `make test` (runs `go test ./...`, `go build ./example/...`, and the `acpwire` driver tests)
- Run: `cd cmd/generate && go test ./...` (separate module, not covered by `make test`)
- Run: `make check` (runs `treefmt`, then `git diff --exit-code`. It needs the formatters from `mise install`)

## Resources

- [Go package docs](https://pkg.go.dev/github.com/coder/acp-go-sdk)
- [Examples (Go)](https://github.com/coder/acp-go-sdk/tree/main/example)
- [Protocol Documentation](https://agentclientprotocol.com)
- [Agent Client Protocol GitHub Repository](https://github.com/agentclientprotocol/agent-client-protocol)

## License

Apache 2.0. See [LICENSE](./LICENSE).
