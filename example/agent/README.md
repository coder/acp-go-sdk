# example/agent

## Overview

`example/agent` is a `package main` demo ACP agent built on `github.com/coder/acp-go-sdk`. It keeps sessions in a mutex-guarded map and answers `Initialize` with the SDK protocol version, `LoadSession` disabled, and the `session/list` and `session/delete` capabilities. It lists and deletes the sessions in that map. Each `Prompt` runs a scripted turn: it streams text, reports two named tool calls, and sends one permission request to the client. A client that advertises notices gets the demo disclaimer as a notice. The prompt `/compact` compacts the session instead. A client that advertises form elicitation is asked for the database host of the edit first, and a client that advertises url elicitation is asked to review an allowed edit. It uses no AI model. `main` connects the agent over stdio, or over the stdio of a child process when you pass a command line, and blocks until the peer disconnects.

## Interface

- No exported top-level symbols. All top-level identifiers are unexported: `agentSession`, `exampleAgent`, `newExampleAgent`, `defaultHost`, `randomID`, `pause`, `main` [main.go:exampleAgent]
- `exampleAgent` satisfies `acp.Agent`, `acp.AgentLoader`, and `acp.AgentExperimental`, checked by compile-time assertions [main.go:exampleAgent]
- CLI: no flags are parsed. With no arguments, the agent speaks ACP on its own stdin and stdout. With arguments, `os.Args[1]` is the client program and `os.Args[2:]` are its arguments [main.go:main]
- Environment variables: this file reads none. With arguments, `exec.CommandContext` looks up the client program through `PATH` when its name has no path separator, and the child inherits the full environment [main.go:main]
- `Initialize` records whether the client accepts boolean config options, form and url elicitation, notices, and compaction updates, and returns `ProtocolVersion: acp.ProtocolVersionNumber`, `AgentCapabilities.LoadSession: false`, and `SessionCapabilities` with `List` and `Delete` set to `{}` [main.go:Initialize]
- `NewSession` returns a new session ID of the form `sess_<hex>` and records the request's `Cwd` [main.go:NewSession]
- `ListSessions` returns one page, with no `NextCursor`, of every session in the map, or only those whose `Cwd` equals the request's `Cwd` when it is set, in creation order. Each entry has `SessionId` and `Cwd` [main.go:ListSessions]
- `DeleteSession` removes the session from the map and returns an empty response and a nil error, also for an unknown session ID [main.go:DeleteSession]
- `Prompt` runs the scripted turn, or the compaction when the prompt is the single text block `/compact`, and returns `acp.StopReasonEndTurn`, or `acp.StopReasonCancelled` when the turn context ends [main.go:Prompt]
- `Cancel` cancels the running turn of a session [main.go:Cancel]
- `Authenticate` and `LoadSession` return empty responses and a nil error [main.go:Authenticate, main.go:LoadSession]
- `SetSessionMode` returns an empty response and a nil error [main.go:SetSessionMode]
- `UnstableDidOpenDocument`, `UnstableDidChangeDocument`, `UnstableDidCloseDocument`, `UnstableDidFocusDocument`, `UnstableDidSaveDocument`, `UnstableAcceptNes`, `UnstableRejectNes`, and `UnstableMessageMcp` return nil and do nothing [main.go:UnstableDidOpenDocument, main.go:UnstableAcceptNes, main.go:UnstableMessageMcp]
- `SetSessionConfigOption` sets the `model` select to `demo-fast` or `demo-deep`, or the `web_search` boolean for a client that accepts booleans, and returns every option. Any other id or value returns `acp.NewInvalidParams` [main.go:SetSessionConfigOption]
- These methods return `acp.NewMethodNotFound`: `ResumeSession`, `CloseSession`, `Logout`, `UnstableForkSession`, `UnstableStartNes`, `UnstableSuggestNes`, `UnstableCloseNes`, `UnstableListProviders`, `UnstableSetProvider`, `UnstableDisableProvider` [main.go:ResumeSession, main.go:CloseSession, main.go:UnstableForkSession]
- `SetAgentConnection` stores the `*acp.AgentSideConnection` that the turn uses to send updates. `main` calls it after `acp.NewAgentSideConnection` [main.go:SetAgentConnection, main.go:main]

## Invariants

- `Prompt` returns the error `session <id> not found` when the session ID is not in the sessions map, so a prompt after `DeleteSession` fails [main.go:Prompt, main.go:DeleteSession]
- `DeleteSession` calls the session's cancel func, when it is set, under `a.mu`. `Prompt` checks its context under `a.mu` after the last step of a turn, so a turn that a delete reaches before that check ends with `StopReasonCancelled`, and a delete after it finds no cancel func [main.go:DeleteSession, main.go:Prompt]
- `Prompt` looks up the session, takes the earlier turn's cancel func from `s.cancel`, and stores its own in one critical section, and then calls the earlier func. A concurrent `DeleteSession` either runs first, so `Prompt` fails with `session <id> not found`, or finds the new func and cancels the turn. The SDK also cancels the earlier prompt of a session when a later one registers [main.go:Prompt, main.go:DeleteSession] [../../agent.go:registerPrompt]
- `Prompt` maps any turn error that occurs after the turn context ends to `StopReasonCancelled` with a nil error [main.go:Prompt]
- `Prompt` stores the current turn's cancel func in `s.cancel` before the turn runs, and clears it only after a successful turn, and only when no later `Prompt` has replaced it (`s.turn` still holds its number). On the error and cancelled paths, `s.cancel` still holds the current turn's func [main.go:Prompt]
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
- A turn that is not cancelled sends `SessionUpdate` notifications with hard-coded text and tool calls `call_1` (name `read_file`, read `/project/README.md`) and `call_2` (name `write_file`, edit `/project/config.json`). Its first update is the disclaimer: an `info` notice titled `ACP Go Example Agent — demo only` for a client that advertises `session.notices`, else an agent message. A cancelled turn returns at its next `SessionUpdate`, `pause`, or `RequestPermission` call, so it can end before either tool call. No file is read or written [main.go:simulateTurn, main.go:pause]
- A turn that is not cancelled sends one `RequestPermission` request for `call_2` (name `write_file`, path `/project/config.json`, the same as the tool call) with options `allow` and `reject`, and waits for the client response [main.go:simulateTurn]
- Before that request, when the client advertises form elicitation, the turn sends `CreateElicitation` in form mode, tied to the session and `call_2`, for one required string `host` (default `new-host`). An accepted non-empty host goes into the request's `rawInput.content`; a decline, a cancel or an empty host keeps `new-host` [main.go:askHost]
- After an allowed edit, when the client advertises url elicitation, the turn sends `CreateElicitation` in url mode with a new `elic_<hex>` id, and sends `CompleteElicitation` with that id once the client accepts [main.go:askReview]
- A turn that is not cancelled sleeps between steps: one 250 ms step and several 1 s steps [main.go:simulateTurn, main.go:pause]
- `/compact` sends, for a client that advertises `session.compaction`, a `compaction_update` with a new `cmp_<hex>` id and status `in_progress`, one `compaction_summary_chunk` with the text `Compacted <n> earlier prompt(s) of this demo session.`, and a `compaction_update` with status `completed`, 250 ms apart. Any other client gets that text as one agent message [main.go:compact]
- `NewSession` adds an entry to the sessions map under `a.mu` [main.go:NewSession]
- `DeleteSession` removes an entry from the sessions map under `a.mu` [main.go:DeleteSession]
- `Prompt` stores a `context.CancelFunc` in the session under `a.mu` [main.go:Prompt]
- `randomID` reads 12 bytes from `crypto/rand` [main.go:randomID]

## Verification

- Run: `go vet ./example/agent` and `go test ./example/agent`. The tests drive `session/new`, `session/list`, `session/delete`, and `session/prompt` through an SDK client connection over pipes [main_test.go]
