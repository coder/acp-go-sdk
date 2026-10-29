# example/agent

## Overview

`example/agent` is a `package main` demo ACP agent built on `github.com/coder/acp-go-sdk`. It keeps sessions in a mutex-guarded map and answers `Initialize` with the SDK protocol version and `LoadSession` disabled. Each `Prompt` runs a scripted turn: it streams text, reports two tool calls, and sends one permission request to the client. It uses no AI model. `main` connects the agent over stdio, or over the stdio of a child process when you pass a command line, and blocks until the peer disconnects.

## Interface

- No exported top-level symbols. All top-level identifiers are unexported: `agentSession`, `exampleAgent`, `newExampleAgent`, `randomID`, `pause`, `main` [main.go:exampleAgent]
- `exampleAgent` satisfies `acp.Agent`, `acp.AgentLoader`, and `acp.AgentExperimental`, checked by compile-time assertions [main.go:exampleAgent]
- CLI: no flags are parsed. With no arguments, the agent speaks ACP on its own stdin and stdout. With arguments, `os.Args[1]` is the client program and `os.Args[2:]` are its arguments [main.go:main]
- Environment variables: none are read [main.go:main]
- `Initialize` returns `ProtocolVersion: acp.ProtocolVersionNumber` and `AgentCapabilities.LoadSession: false` [main.go:Initialize]
- `NewSession` returns a new session ID of the form `sess_<hex>` [main.go:NewSession]
- `Prompt` runs the scripted turn and returns `acp.StopReasonEndTurn`, or `acp.StopReasonCancelled` when the turn context ends [main.go:Prompt]
- `Cancel` cancels the running turn of a session [main.go:Cancel]
- `Authenticate` and `LoadSession` return empty responses and a nil error [main.go:Authenticate, main.go:LoadSession]
- `SetSessionMode` returns an empty response and a nil error [main.go:SetSessionMode]
- `UnstableDidOpenDocument`, `UnstableDidChangeDocument`, `UnstableDidCloseDocument`, `UnstableDidFocusDocument`, `UnstableDidSaveDocument`, `UnstableAcceptNes`, and `UnstableRejectNes` return nil and do nothing [main.go:UnstableDidOpenDocument, main.go:UnstableAcceptNes]
- These methods return `acp.NewMethodNotFound`: `ListSessions`, `ResumeSession`, `CloseSession`, `SetSessionConfigOption`, `Logout`, `UnstableForkSession`, `UnstableDeleteSession`, `UnstableStartNes`, `UnstableSuggestNes`, `UnstableCloseNes`, `UnstableListProviders`, `UnstableSetProvider`, `UnstableDisableProvider` [main.go:ListSessions, main.go:CloseSession, main.go:UnstableDeleteSession]
- `SetAgentConnection` stores the `*acp.AgentSideConnection` that the turn uses to send updates. `main` calls it after `acp.NewAgentSideConnection` [main.go:SetAgentConnection, main.go:main]

## Invariants

- `Prompt` returns the error `session <id> not found` when the session ID is not in the sessions map [main.go:Prompt]
- `Prompt` calls the previous turn's cancel func before it starts a new turn [main.go:Prompt]
- `Prompt` maps any turn error that occurs after the turn context ends to `StopReasonCancelled` with a nil error [main.go:Prompt]
- `Prompt` stores the current turn's cancel func in `s.cancel` before the turn runs, and clears it only after a successful turn. On the error and cancelled paths, `s.cancel` still holds the current turn's func [main.go:Prompt]
- `Cancel` returns nil for unknown sessions and calls the cancel func only when it is set. It reads `s.cancel` after it releases `a.mu` [main.go:Cancel]
- `a.mu` guards the sessions map in `NewSession`, `Cancel`, and `Prompt`, and guards writes to `s.cancel` in `Prompt` [main.go:NewSession, main.go:Cancel, main.go:Prompt]
- A `Cancelled` permission outcome ends the turn with a nil error, so `Prompt` returns `StopReasonEndTurn` [main.go:simulateTurn]
- A permission outcome with no `Selected` option returns the error `unexpected permission outcome` [main.go:simulateTurn]
- A selected option other than `allow` or `reject` returns the error `unexpected permission option: <id>` [main.go:simulateTurn]
- `randomID` returns `sess_` plus 24 hex characters, or `sess_<UnixNano>` if `crypto/rand` fails [main.go:randomID]
- `pause` returns `ctx.Err()` when the context ends before the timer fires [main.go:pause]

## Side Effects

- Without arguments, the ACP connection uses `os.Stdin` and `os.Stdout` [main.go:main]
- With arguments, `main` starts the client with `exec.CommandContext`, pipes its stdin and stdout to the connection, and sets its stderr to `os.Stderr`. Errors from `StdinPipe` and `StdoutPipe` are discarded [main.go:main]
- If the client fails to start, `main` writes `failed to start client: <err>` to stderr and calls `os.Exit(1)` [main.go:main]
- After the peer disconnects, `main` kills the child process if one was started [main.go:main]
- `signal.NotifyContext` cancels the main context on `os.Interrupt` or `os.Kill` [main.go:main]
- `main` sets the connection logger to `slog.Default()` [main.go:main]
- `main` blocks on `<-asc.Done()` until the peer disconnects [main.go:main]
- Each turn sends `SessionUpdate` notifications with hard-coded text and tool calls `call_1` (read `/project/README.md`) and `call_2` (edit `/project/config.json`). No file is read or written [main.go:simulateTurn]
- Each turn sends one `RequestPermission` request for `call_2` (path `/project/config.json`, the same as the tool call) with options `allow` and `reject`, and waits for the client response [main.go:simulateTurn]
- Each turn sleeps between steps: one 250 ms step and several 1 s steps [main.go:simulateTurn, main.go:pause]
- `NewSession` adds an entry to the sessions map under `a.mu` [main.go:NewSession]
- `Prompt` stores a `context.CancelFunc` in the session under `a.mu` [main.go:Prompt]
- `randomID` reads 12 bytes from `crypto/rand` [main.go:randomID]

## Verification

- Run: `go vet ./example/agent`
