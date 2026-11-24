# example/agent

## Overview

`example/agent` is a `package main` demo ACP agent built on `github.com/coder/acp-go-sdk`. It keeps sessions in a mutex-guarded map and answers `Initialize` with its `agentInfo`, the SDK protocol version, `LoadSession` enabled, and the `session/list` and `session/delete` capabilities. It lists and deletes the sessions in that map. Each `Prompt` runs a scripted turn: it streams text, reports two named tool calls, and sends one permission request to the client. A client that advertises notices gets the demo disclaimer as a notice. The prompt `/compact` compacts the session instead. A client that advertises form elicitation is asked for the database host of the edit first, and a client that advertises url elicitation is asked to review an allowed edit. It uses no AI model. `main` connects the agent over stdio, or over the stdio of a child process when you pass a command line, and blocks until the peer disconnects.

## Interface

- No exported top-level symbols. All top-level identifiers are unexported: `agentSession`, `exampleAgent`, `newExampleAgent`, `defaultHost`, `randomID`, `pause`, `main` [main.go:exampleAgent]
- `exampleAgent` satisfies `acp.Agent` and the optional interfaces `acp.AgentLoader` and `acp.AgentUnstableMessageMcp`, checked by compile-time assertions. It implements no other optional interface, so the SDK answers every other agent method, such as `session/fork`, with `-32601` [main.go:exampleAgent]
- CLI: no flags are parsed. With no arguments, the agent speaks ACP on its own stdin and stdout. With arguments, `os.Args[1]` is the client program and `os.Args[2:]` are its arguments [main.go:main]
- Environment variables: this file reads none. With arguments, `exec.CommandContext` looks up the client program through `PATH` when its name has no path separator, and the child inherits the full environment [main.go:main]
- `Initialize` records whether the client accepts boolean config options, form and url elicitation, notices, and compaction updates, and returns `AgentInfo` (name `acp-go-example-agent`, title `ACP Go Example Agent`, version `0.1.0`), `ProtocolVersion: acp.ProtocolVersionNumber`, `AgentCapabilities.LoadSession: true`, and `SessionCapabilities` with `List`, `Delete` and `SystemPrompt` set to `{}`, so it takes a system prompt in `append` mode only [main.go:Initialize]
- `NewSession` returns a new session ID of the form `sess_<hex>` and records the request's `Cwd` and the text of its `SystemPrompt`. A system prompt in another mode than `append`, with no content, or with a block that is not text gets `acp.NewInvalidParams` and no session. Each scripted turn of a session with a system prompt sends `Following your system prompt: <text>` after the disclaimer [main.go:NewSession, systemPromptText, simulateTurn]
- `ListSessions` returns one page, with no `NextCursor`, of every session in the map, or only those whose `Cwd` equals the request's `Cwd` when it is set, in creation order. Each entry has `SessionId` and `Cwd`. The agent issues no cursor, so a request with a `Cursor` gets `acp.NewInvalidParams` [main.go:ListSessions]
- `DeleteSession` removes the session from the map and returns an empty response and a nil error, also for an unknown session ID [main.go:DeleteSession]
- `Prompt` runs the scripted turn, or the compaction when the prompt is the single text block `/compact`, and returns `acp.StopReasonEndTurn`, or `acp.StopReasonCancelled` when the turn context ends [main.go:Prompt]
- `Cancel` cancels the running turn of a session [main.go:Cancel]
- `Authenticate` returns an empty response and a nil error [main.go:Authenticate]
- `LoadSession` replays the session's history as `session/update` notifications before it returns: each prompt's blocks as `user_message_chunk` updates, and every update that the agent sent for the session but notices, which are live events, in order. It returns the session's config options. A `SystemPrompt` replaces the session's prompt, with the rules of `NewSession`; without one the session keeps its prompt. An unknown session ID gets `acp.NewResourceNotFound` (`-32002`) with the `sessionId` in `data`, and a `Cwd` other than the session's gets `acp.NewInvalidParams`. Sessions live in memory, so only a session of this process loads [main.go:LoadSession, main.go:sessionUpdate]
- `SetSessionMode` answers an unknown session ID with `acp.NewResourceNotFound` (`-32002`) and the `sessionId` in `data`. The agent offers no modes, so any mode of a session it has gets `acp.NewInvalidParams` [main.go:SetSessionMode, main.go:sessionNotFound]
- `UnstableMessageMcp` returns nil and does nothing: the agent sends no `mcp/message` request, so no client notification belongs to one [main.go:UnstableMessageMcp]
- `SetSessionConfigOption` sets the `model` select to `demo-fast` or `demo-deep`, or the `web_search` boolean for a client that accepts booleans, and returns every option. An unknown session ID gets `acp.NewResourceNotFound` (`-32002`) with the `sessionId` in `data`, and any other id or value `acp.NewInvalidParams` [main.go:SetSessionConfigOption, main.go:sessionNotFound]
- These methods return `acp.NewMethodNotFound`: `ResumeSession`, `CloseSession`, `Logout` [main.go:ResumeSession, main.go:CloseSession, main.go:Logout]
- `SetAgentConnection` stores the `*acp.AgentSideConnection` that the turn uses to send updates. `main` calls it after `acp.NewAgentSideConnection` [main.go:SetAgentConnection, main.go:main]

## Invariants

- `Prompt` returns `acp.NewResourceNotFound` (`-32002`) with the `sessionId` in `data` when the session ID is not in the sessions map, so a prompt after `DeleteSession` fails [main.go:Prompt, main.go:DeleteSession, main.go:sessionNotFound]
- `DeleteSession` calls the session's cancel func, when it is set, under the session's `sendMu` and `a.mu`, so it waits for a running replay, and no update of the session follows it. `Prompt` checks its context under `a.mu` after the last step of a turn, so a turn that a delete reaches before that check ends with `StopReasonCancelled`, and a delete after it finds no cancel func [main.go:DeleteSession, main.go:Prompt]
- `Prompt` looks up the session, then in one critical section under `sendMu` and `a.mu` checks that the session still exists and that its context is not done, takes the earlier turn's cancel func from `s.cancel`, stores its own, and adds the prompt to `history`. Then it calls the earlier func. The SDK cancels the context of a prompt that arrived before the session's newest one, so a late prompt returns `StopReasonCancelled` at once: it neither cancels the newer turn nor lands after it in the history. A concurrent `DeleteSession` either runs first, so `Prompt` fails with `-32002`, or finds the new func and cancels the turn. The SDK also cancels the earlier prompt of a session when a later one registers [main.go:Prompt, main.go:DeleteSession] [../../agent.go:registerPrompt]
- `Prompt` maps any turn error that occurs after the turn context ends to `StopReasonCancelled` with a nil error [main.go:Prompt]
- `Prompt` stores the current turn's cancel func in `s.cancel` before the turn runs, and clears it only after a successful turn, and only when no later `Prompt` has replaced it (`s.turn` still holds its number). On the error and cancelled paths, `s.cancel` still holds the current turn's func [main.go:Prompt]
- Every session update of a turn goes through `sessionUpdate`, which holds the session's `sendMu` while it sends the update and appends it to `history`. `Prompt` appends the user's blocks under `sendMu` too, so an update of an earlier, overlapping turn lands before or after the whole prompt. `LoadSession` applies a new system prompt only after the replay succeeds. `LoadSession` holds `sendMu` while it replays, so a replay never misses or repeats an update of a running turn. Lock order: `sendMu`, then `a.mu`. No code takes `sendMu` while it holds `a.mu`, and no request to the client runs under `sendMu` [main.go:sessionUpdate, main.go:LoadSession]
- `Cancel` returns nil for unknown sessions and calls the cancel func only when it is set. It reads `s.cancel` after it releases `a.mu` [main.go:Cancel]
- `a.mu` guards the sessions map and `nextSeq` in `NewSession`, `ListSessions`, `DeleteSession`, `Cancel`, and `Prompt`, and guards `s.cancel` and `s.turn` in `Prompt` [main.go:NewSession, main.go:ListSessions, main.go:DeleteSession, main.go:Cancel, main.go:Prompt]
- A `Cancelled` permission outcome ends the turn with a nil error, so `Prompt` returns `StopReasonEndTurn` [main.go:simulateTurn]
- A permission outcome with no `Selected` option returns the error `unexpected permission outcome` [main.go:simulateTurn]
- A selected option other than `allow` or `reject` returns the error `unexpected permission option: <id>` [main.go:simulateTurn]
- `randomID` returns its prefix (`sess_`, `elic_` or `cmp_`) plus 24 hex characters, or the prefix plus `UnixNano` if `crypto/rand` fails [main.go:randomID]
- `pause` returns `ctx.Err()` when the context ends before the timer fires [main.go:pause]

## Side Effects

- Without arguments, the ACP connection uses `os.Stdin` and `os.Stdout` [main.go:main]
- With arguments, `main` starts the client with `exec.CommandContext`, pipes its stdin and stdout to the connection, and sets its stderr to `os.Stderr`. Errors from `StdinPipe` and `StdoutPipe` are discarded [main.go:main]
- If the client fails to start, `main` writes `failed to start client: <err>` to stderr and calls `os.Exit(1)` [main.go:main]
- After the peer disconnects, `main` kills the child process if one was started [main.go:main]
- `signal.NotifyContext` cancels the main context on `os.Interrupt`. The call also lists `os.Kill`, but no process can catch SIGKILL, so a kill ends the agent at once and no cleanup runs [main.go:main]
- `main` sets the connection logger to `slog.Default()` [main.go:main]
- `main` blocks on `<-asc.Done()` until the peer disconnects [main.go:main]
- A turn that is not cancelled sends `SessionUpdate` notifications with hard-coded text and tool calls `call_1` (name `read_file`, read `/project/README.md`) and `call_2` (name `write_file`, edit `/project/config.json`). Tool call ids are unique within a session: the session's next turn uses `call_3` and `call_4`, and so on [main.go:toolCallIDs]. Its first update is the disclaimer: an `info` notice titled `ACP Go Example Agent — demo only` for a client that advertises `session.notices`, else an agent message. A cancelled turn returns at its next `SessionUpdate`, `pause`, or `RequestPermission` call, so it can end before either tool call. No file is read or written [main.go:simulateTurn, main.go:pause]
- A turn that is not cancelled sends one `RequestPermission` request for `call_2` (name `write_file`, path `/project/config.json`, the same as the tool call) with options `allow` and `reject`, and waits for the client response [main.go:simulateTurn]
- Before that request, when the client advertises form elicitation, the turn sends `CreateElicitation` in form mode, tied to the session and `call_2`, for one required string `host` (default `new-host`). An accepted non-empty host goes into the request's `rawInput.content`; a decline, a cancel or an empty host keeps `new-host` [main.go:askHost]
- After an allowed edit, when the client advertises url elicitation, the turn sends `CreateElicitation` in url mode with a new `elic_<hex>` id, and sends `CompleteElicitation` with that id once the client accepts [main.go:askReview]
- A turn that is not cancelled sleeps between steps: one 250 ms step and several 1 s steps [main.go:simulateTurn, main.go:pause]
- `/compact` sends, for a client that advertises `session.compaction`, a `compaction_update` with a new `cmp_<hex>` id and status `in_progress`, one `compaction_summary_chunk` with the text `Compacted <n> earlier prompt(s) of this demo session.`, and a `compaction_update` with status `completed`, 250 ms apart. Any other client gets that text as one agent message [main.go:compact]
- `NewSession` adds an entry to the sessions map under `a.mu` [main.go:NewSession]
- `DeleteSession` removes an entry from the sessions map under the session's `sendMu` and `a.mu` [main.go:DeleteSession]
- `Prompt` stores a `context.CancelFunc` in the session under `a.mu` [main.go:Prompt]
- `randomID` reads 12 bytes from `crypto/rand` [main.go:randomID]

## Verification

- Run: `go vet ./example/agent` and `go test ./example/agent`. The tests drive `session/new`, `session/list`, `session/delete`, and `session/prompt` through an SDK client connection over pipes [main_test.go]
