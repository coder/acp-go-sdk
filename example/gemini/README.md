# example/gemini

## Overview

This directory holds one `package main` program, `main.go`. It starts the Gemini CLI as a child process with `--experimental-acp` and connects to it with `acp.NewClientSideConnection` from `github.com/coder/acp-go-sdk`. It runs the ACP `initialize` and `session/new` handshake, then sends each stdin line as a prompt and prints the streamed session updates. The `replClient` type implements `acp.Client` with permission prompts, file read and write for absolute paths, and stub terminal methods.

## Interface

- Entry point `main`, run with `go run ./example/gemini` [main.go:main]
- Flag `-gemini` (string, default `gemini`): path of the Gemini CLI binary [main.go:-gemini]
- Flag `-model` (string, default empty): when set, passes `--model <value>` to Gemini [main.go:-model]
- Flag `-sandbox` (bool): passes `--sandbox` to Gemini [main.go:-sandbox]
- Flag `-yolo` (bool): auto-approves permission prompts [main.go:-yolo]
- Flag `-debug` (bool): passes `--debug` to Gemini [main.go:-debug]
- REPL commands: `:exit` and `:quit` end the program, `:cancel` sends `session/cancel`, any other non-empty line is sent as a text prompt [main.go:main]
- Unexported type `replClient` (fields `autoApprove` and `input`, the channel of stdin lines), checked at compile time by `var _ acp.Client = (*replClient)(nil)` [main.go:replClient]
- `replClient` methods: `RequestPermission`, `SessionUpdate`, `WriteTextFile`, `ReadTextFile`, `CreateTerminal`, `TerminalOutput`, `ReleaseTerminal`, `WaitForTerminalExit`, `KillTerminal` [main.go:replClient]
- Unexported helper `mustCwd` returns the working directory, or `.` on error [main.go:mustCwd]
- Unexported helper `readLines` reads an `io.Reader` line by line on one goroutine and sends each line on a channel, which it closes at EOF or on a read error [main.go:readLines]

## Invariants

- The child argv always starts with `--experimental-acp` [main.go:main]
- `initialize` sends `acp.ProtocolVersionNumber` and advertises `fs.readTextFile`, `fs.writeTextFile`, and `terminal` as `true` [main.go:main]
- `session/new` uses `mustCwd()` as `cwd` and an empty MCP server list [main.go:main]
- With `-yolo`, `RequestPermission` selects the first `allow_once` or `allow_always` option, else the first option, else returns `cancelled` [main.go:RequestPermission]
- Without `-yolo`, `RequestPermission` asks again until the user enters a 1-based option number in range. When stdin ends first, it returns `cancelled` [main.go:RequestPermission]
- `WriteTextFile` and `ReadTextFile` return an error for a path that is not absolute [main.go:WriteTextFile, main.go:ReadTextFile]
- `ReadTextFile` applies `line` (1-based, clamped to the line count) and `limit` only when they are greater than 0 [main.go:ReadTextFile]
- `SessionUpdate` always returns `nil` [main.go:SessionUpdate]
- A failed `initialize` or `session/new` prints the error, kills the child, and exits with status 1 [main.go:main]
- A failed prompt prints the error to stderr and the REPL continues. A `*acp.RequestError` is printed as indented JSON [main.go:main]
- Empty input lines are skipped [main.go:main]

## Side Effects

- Starts the Gemini CLI with `exec.CommandContext`, pipes its stdin and stdout to the ACP connection, and connects its stderr to `os.Stderr` [main.go:main]
- Reads user input from `os.Stdin` on one goroutine (`readLines`). The REPL loop and the interactive permission prompts both take lines from its channel, so a permission answer typed or piped after a prompt reaches the permission prompt [main.go:readLines, main.go:main, main.go:RequestPermission]
- Prints connection status, permission prompts, agent message and thought chunks, a `[user_message_chunk]` notice, tool call and plan notices, and file and terminal calls to stdout [main.go:main, main.go:SessionUpdate, main.go:RequestPermission]
- Prints pipe, start, `initialize`, `session/new`, and prompt errors to stderr [main.go:main]
- `WriteTextFile` creates the parent directory with `os.MkdirAll` (mode `0755`) and writes the file with `os.WriteFile` (mode `0644`) [main.go:WriteTextFile]
- `ReadTextFile` reads the file with `os.ReadFile` [main.go:ReadTextFile]
- Terminal methods only print their parameters and return stub values (`CreateTerminal` returns terminal ID `term-1`). They start no process [main.go:CreateTerminal, main.go:TerminalOutput, main.go:ReleaseTerminal, main.go:WaitForTerminalExit, main.go:KillTerminal]
- Sets the connection logger to `slog.Default()` [main.go:main]
- Calls `os.Exit(1)` on a pipe, start, `initialize`, or `session/new` failure [main.go:main]
- `:exit` and `:quit` cancel the context passed to `exec.CommandContext`. End of stdin kills the child with `cmd.Process.Kill` [main.go:main]
- Calls `os.Getwd` for the session working directory [main.go:mustCwd]

## Verification

- Run: `go vet ./example/gemini`
