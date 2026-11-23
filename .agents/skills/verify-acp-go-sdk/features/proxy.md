# Proxy

A user puts a proxy built on the SDK between an editor and an agent, inside a proxy chain that a conductor runs. The conductor sends the proxy `proxy/initialize` instead of `initialize`, sends it the editor's messages as they are, and carries everything to and from the agent in `proxy/successor` envelopes. `example/proxy` puts `[proxy] ` in front of the text of each `agent_message_chunk` that comes from its successor, and passes every other message on unchanged. Here `acpwire` plays the conductor on both sides of the proxy: the editor's side with plain methods, and the successor's side with envelopes and the answers that the journey writes. The proxy methods are unstable in ACP schema 1.34.1.

## Sub-features

- `proxy-initialize` forwards `proxy/initialize` to its successor as `initialize`, and answers with protocol version 1.
- `proxy-forward` forwards `session/new` and `session/prompt` to the successor in `proxy/successor` requests, and returns the successor's results unchanged.
- `proxy-rewrite` sends the editor each successor `agent_message_chunk` with `[proxy] ` in front of its text.
- `proxy-passthrough` sends the editor any other successor `session/update`, such as `agent_thought_chunk`, unchanged.
- `proxy-reverse` sends the editor a successor request (`session/request_permission`) as a plain request, and returns the editor's answer to the successor.

## How to get to it (user POV)

- Run `agent-client-protocol-conductor agent acp-example-proxy acp-example-agent` as the agent command of an editor. G4 in `verify-real-harnesses` drives that real chain with the TypeScript SDK as the editor.
- Start the proxy directly under a client that speaks the conductor's side of the protocol, as this journey does.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`, and its `acp-example-proxy built` line prints ok.

Steps:

- **Run the chain.** Run `"$V" wire proxy.jsonl proxy proxy`. The third argument starts `acp-example-proxy` instead of the example agent. Each step prints PASS: `send proxy/initialize`, the `wait for proxy/successor` steps that show `initialize`, `session/new` and `session/prompt` reaching the successor, `wait for session/update` for `[proxy] Hello from the successor.` and for the unchanged `Thinking it over.`, `send proxy/successor` for the permission request, which the editor's rule answers `allow`, `send session/prompt` with `stopReason end_turn`, and `close stdin` with `agent exit code 0`.
- **Read the envelopes.** Run `jq -c '[.dir, .msg.method // "response", .msg.params.method // ""]' "$ACP_VERIFY_RUN/evidence/proxy/transcript.jsonl"`. Each editor request is followed by a `proxy/successor` request from the proxy that wraps the same method, and the successor's `session/request_permission` reaches the editor as a plain `session/request_permission`.
- **Agent log.** Run `cat "$ACP_VERIFY_RUN/evidence/proxy/agent.stderr.log"`. It has no `ERROR` line.

## Gotchas

- `acpwire` stops at the first failed step. When `[proxy] Hello from the successor.` times out, read the transcript for the `session/update` that the proxy sent instead.
- To tell a journey bug from a proxy bug, run the journey against the Rust conductor in proxy mode, which passes every message on unchanged: `"$ACP_VERIFY_RUN/work/bin/acpwire" -agent CONDUCTOR -journey .agents/skills/verify-acp-go-sdk/journeys/proxy.jsonl -out DIR -- proxy`, with `CONDUCTOR` the `work/cargo/bin/agent-client-protocol-conductor` of a `verify-real-harnesses` run. Every step passes up to the `[proxy] ` one, which fails because nothing rewrites the chunk. With that step's text changed to `Hello from the successor.`, every step passes.
- The session id `sess-1` comes from the journey's `session/new` rule, which plays the successor.
