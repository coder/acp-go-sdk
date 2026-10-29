# example

## Overview

`example` holds four `package main` programs. Each one imports only `github.com/coder/acp-go-sdk` from this module. `agent` is a scripted ACP agent. `client`, `claude-code`, and `gemini` are ACP clients that start an agent as a child process and speak ACP over its stdin and stdout. All four are part of the root module, so `make test` builds them with `go build ./example/...`.

## Subdirectories & Boundaries

- agent: [./agent/README.md](./agent/README.md) - Scripted demo agent. It implements `acp.Agent`, `acp.AgentLoader`, and `acp.AgentExperimental`. Each prompt streams text, reports two tool calls, and sends one permission request. It uses no AI model. With no arguments it speaks ACP on its own stdin and stdout. With arguments it starts the named client program and speaks ACP over that program's pipes.
- claude-code: [./claude-code/README.md](./claude-code/README.md) - REPL client for the Claude Code ACP server, which it starts with `npx -y @zed-industries/claude-code-acp@latest`. Flag `-yolo` auto-approves permission prompts.
- client: [./client/README.md](./client/README.md) - Demo client. It starts the agent named on the command line, or `go run` of `../agent`, then sends one `Initialize`, `NewSession`, and `Prompt` and prints the session updates.
- gemini: [./gemini/README.md](./gemini/README.md) - REPL client for the Gemini CLI, which it starts with `--experimental-acp`. Flags: `-gemini`, `-model`, `-sandbox`, `-yolo`, `-debug`.
