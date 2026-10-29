# example/claude-code

## Overview

This directory holds a `package main` example that connects the `github.com/coder/acp-go-sdk` client side to the Claude Code ACP server. It starts `npx -y @zed-industries/claude-code-acp@latest` as a subprocess and talks ACP over the subprocess stdin and stdout. It sends `Initialize` with file system read and write capabilities, creates one session in the current working directory, and then sends each non-empty stdin line as a prompt. The `:cancel` command sends a cancel notification, and `:exit` or `:quit` ends the program.

## Interface

- Flag `-yolo` (bool, default `false`) auto-approves permission prompts [main.go:main]
- `func main()` starts the Claude Code ACP subprocess, connects with `acp.NewClientSideConnection`, and runs the stdin prompt loop [main.go:main]
- REPL commands, read only between prompts: `:cancel` sends `acp.CancelNotification` for the session, and `:exit` / `:quit` cancel the context and return. During a turn the loop is blocked in `conn.Prompt`, so `:cancel` cannot stop the running turn, and a line typed at a permission prompt goes to that prompt [main.go:main]
- `type replClient struct` with fields `autoApprove bool` and `input <-chan string` (the lines of stdin) [main.go:replClient]
- `var _ acp.Client = (*replClient)(nil)` compile-time check that `replClient` implements `acp.Client` [main.go:replClient]
- `replClient` methods `RequestPermission`, `SessionUpdate`, `WriteTextFile`, and `ReadTextFile` implement the `acp.Client` permission, update, and file system calls [main.go:RequestPermission]
- `replClient` terminal methods `CreateTerminal` (returns terminal ID `term-1`), `TerminalOutput`, `ReleaseTerminal`, `WaitForTerminalExit`, and `KillTerminal` are stubs that print their parameters [main.go:CreateTerminal]
- `func mustCwd() string` returns the working directory, or `"."` on error [main.go:mustCwd]
- `func readLines(r io.Reader) <-chan string` reads `r` line by line on one goroutine, sends each line on the returned channel, and closes it at EOF or on a read error [main.go:readLines]

## Invariants

- `WriteTextFile` requires an absolute path and returns an error otherwise [main.go:WriteTextFile]
- `ReadTextFile` requires an absolute path and returns an error otherwise [main.go:ReadTextFile]
- `ReadTextFile` treats `Line` as 1-based, clamps the start to the file length, and applies `Limit` only when it is greater than 0 [main.go:ReadTextFile]
- With `autoApprove`, `RequestPermission` picks the first `allow_once` or `allow_always` option, else the first option, else returns `Cancelled` [main.go:RequestPermission]
- Without `autoApprove`, `RequestPermission` returns `Cancelled` when stdin ends before a valid option number is entered [main.go:RequestPermission]
- `Initialize` and `NewSession` errors kill the subprocess and exit with status 1 [main.go:main]
- A `Prompt` error prints to stderr and the prompt loop continues [main.go:main]

## Side Effects

- Starts an `npx` subprocess, found through `PATH` and with the full environment, with stdin and stdout pipes, and forwards its stderr to `os.Stderr` [main.go:main]
- Prints agent messages, thoughts, tool calls, plan updates, and a `[user_message_chunk]` notice to stdout [main.go:SessionUpdate]
- Starts one goroutine (`readLines`) that reads `os.Stdin`. The prompt loop and the interactive permission prompts both take lines from its channel, so a permission answer typed or piped after a prompt reaches the permission prompt [main.go:readLines, main.go:main, main.go:RequestPermission]
- Creates missing parent directories (mode `0o755`) and writes files with `os.WriteFile`: a new file gets mode `0o644`, and an existing file is truncated and keeps its permissions (modes before umask) [main.go:WriteTextFile]
- Reads files from disk [main.go:ReadTextFile]
- Kills the subprocess on `Initialize` or `NewSession` error and when stdin ends, and cancels its context on `:exit` / `:quit` [main.go:main]

## Verification

- Run: `go vet ./example/claude-code`
