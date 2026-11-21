# example/proxy

## Overview

`example/proxy` is a `package main` demo proxy for an ACP proxy chain, built on the unstable `acp.NewUnstableProxySideConnection`. A conductor runs it between a client and an agent and talks to it over its stdin and stdout. It adds `[proxy] ` in front of the text of each `agent_message_chunk` session update from its successor and forwards every other message unchanged, in both directions.

## Interface

- No exported top-level symbols. The package declares only `main` [main.go:main].
- CLI: no flags or arguments. It speaks ACP to its conductor on stdin and stdout, for example under the Rust conductor: `agent-client-protocol-conductor agent "go run ./example/proxy" "go run ./example/agent"` [main.go:main].
- Handlers: only `FromSuccessor.SessionUpdate`, which edits an agent message chunk with text content and sends the update to the client with `Client().SessionUpdate`. Every other handler is nil, so `proxy/initialize` goes to the successor as `initialize` and the other messages pass through [main.go:main].
- Environment variables: none.

## Invariants

- Every `session/update` from the successor goes through the typed handler, so it is decoded and encoded again: members that the SDK's Go types do not know are lost. An update that does not decode, such as a kind newer than the SDK's schema, reaches the client unchanged, with a Debug log. An update that is not an agent message chunk with text content reaches the client otherwise unchanged [main.go:main].
- The prefix is the literal `[proxy] ` [main.go:main].

## Side Effects

- Stdin and stdout carry ACP. The connection logs to stderr through `slog.NewTextHandler(os.Stderr, nil)` [main.go:main].
- `main` returns when the conductor disconnects (`Done`) [main.go:main].
- Goroutines: none in this file. `acp.NewUnstableProxySideConnection` starts the connection's own goroutines, see the root README.

## Verification

- Run: `go vet ./example/proxy`
- Run in a chain (needs `cargo install agent-client-protocol-conductor` and an ACP client): `agent-client-protocol-conductor agent "go run ./example/proxy" "go run ./example/agent"`
