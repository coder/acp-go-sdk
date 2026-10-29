# example/client

## Overview

`example/client` is a `package main` demo client for the `github.com/coder/acp-go-sdk` module. It starts an agent subprocess and talks to it over the child's stdin and stdout through `acp.NewClientSideConnection`. It sends one `Initialize`, one `NewSession`, and one `Prompt` request, and it prints the session updates it receives. The unexported type `exampleClient` implements the `acp.Client` callbacks: file reads and writes use the local disk, and the terminal methods are stubs.

## Interface

- No exported top-level symbols. The package declares only the unexported identifiers `exampleClient` (field `input`, the channel of stdin lines), `displayUpdateKind`, `main`, `mustCwd`, and `readLines` [main.go:main] [main.go:exampleClient] [main.go:readLines].
- Compile-time assertion `var _ acp.Client = (*exampleClient)(nil)` [main.go:exampleClient].
- `exampleClient` methods that satisfy `acp.Client`: `RequestPermission` [main.go:RequestPermission], `SessionUpdate` [main.go:SessionUpdate], `WriteTextFile` [main.go:WriteTextFile], `ReadTextFile` [main.go:ReadTextFile], `CreateTerminal` [main.go:CreateTerminal], `TerminalOutput` [main.go:TerminalOutput], `ReleaseTerminal` [main.go:ReleaseTerminal], `WaitForTerminalExit` [main.go:WaitForTerminalExit], `KillTerminal` [main.go:KillTerminal].
- CLI: with at least one argument, `os.Args[1]` is the agent program and `os.Args[2:]` are its arguments [main.go:main].
- CLI: with no arguments, the client runs `go run <clientDir>/../agent`, where `clientDir` is the directory of `main.go` from `runtime.Caller(0)` [main.go:main]. This is the [example agent](https://github.com/coder/acp-go-sdk/blob/main/example/agent/main.go).
- Environment variables: none are read [main.go:main].
- `Initialize` request: `ProtocolVersion` is `acp.ProtocolVersionNumber`, `Fs.ReadTextFile` and `Fs.WriteTextFile` are `true`, and `Terminal` is `true` [main.go:main].
- `NewSession` request: `Cwd` is the process working directory from `mustCwd`, and `McpServers` is empty [main.go:main][main.go:mustCwd].
- `Prompt` request: one text block, `"Hello, agent!"` [main.go:main].
- `SessionUpdate` prints agent message text, tool calls, and tool call updates. For plan, agent thought, and user message updates, it prints only the kind name from `displayUpdateKind` [main.go:SessionUpdate][main.go:displayUpdateKind].

## Invariants

- `ReadTextFile` and `WriteTextFile` return an error when the path is not absolute [main.go:ReadTextFile][main.go:WriteTextFile].
- `WriteTextFile` creates the parent directory with mode `0o755`, then writes the file with mode `0o644` [main.go:WriteTextFile].
- `ReadTextFile` treats `Line` as a 1-based start line, clamped to the number of lines. It applies `Limit` as a line count only when `Limit` is greater than 0 [main.go:ReadTextFile].
- `RequestPermission` asks again until the user enters a valid 1-based option number, and prints `Invalid option. Please try again.` for other input [main.go:RequestPermission].
- `RequestPermission` returns a `Cancelled` outcome when stdin ends before a valid option number is entered [main.go:RequestPermission].
- `main` exits with status 1 when `runtime.Caller` fails, when the sibling `../agent` directory does not exist, or when `cmd.Start`, `Initialize`, or `NewSession` fails [main.go:main].
- A `Prompt` error is printed to stderr but does not change the exit status [main.go:main].
- `mustCwd` returns `"."` when `os.Getwd` fails [main.go:mustCwd].
- Terminal methods return fixed values: `CreateTerminal` returns `TerminalId` `"term-1"`, `TerminalOutput` returns empty output with `Truncated` `false`, and the other terminal methods return empty responses [main.go:CreateTerminal][main.go:TerminalOutput][main.go:ReleaseTerminal][main.go:WaitForTerminalExit][main.go:KillTerminal].
- The connection logger is `slog.Default()` [main.go:main].

## Side Effects

- Stdin: `main` starts one goroutine (`readLines`) that reads `os.Stdin` line by line. Every `RequestPermission` call takes lines from its channel, so no call drops lines that a later call needs [main.go:readLines] [main.go:RequestPermission].
- Stdout: permission prompts, session updates, file operation messages, and progress messages are printed with `fmt.Printf` and `fmt.Println` [main.go:RequestPermission][main.go:SessionUpdate][main.go:WriteTextFile][main.go:ReadTextFile][main.go:main]. Terminal stubs print their request values [main.go:CreateTerminal][main.go:TerminalOutput][main.go:ReleaseTerminal][main.go:WaitForTerminalExit][main.go:KillTerminal].
- Stderr: error messages are written with `fmt.Fprintf(os.Stderr, ...)`. A `*acp.RequestError` is printed as indented JSON. The child process stderr is connected to `os.Stderr` [main.go:main].
- Filesystem: `os.MkdirAll` and `os.WriteFile` on the requested path [main.go:WriteTextFile]. `os.ReadFile` on the requested path [main.go:ReadTextFile]. `os.Stat` on the sibling agent directory [main.go:main]. `os.Getwd` for the session working directory [main.go:mustCwd].
- Subprocess: `exec.CommandContext` starts `os.Args[1]` or `go run <agentPath>`. The client ignores errors from `StdinPipe` and `StdoutPipe` [main.go:main].
- Subprocess termination: `cmd.Process.Kill` runs on the `Initialize` and `NewSession` error paths and at the end of `main`, and its errors are ignored [main.go:main].
- Process termination: `os.Exit(1)` on the error paths listed under Invariants [main.go:main].
- Network: none directly. Protocol traffic goes over the child's stdio [main.go:main].
- Context: `main` creates a cancellable context and cancels it with `defer` [main.go:main].
- Goroutines: only the stdin reader that `readLines` starts. Locks and global mutable state: none in this package [main.go:readLines].

## Verification

- Run: `go vet ./example/client`
