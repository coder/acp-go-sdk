# Cross-SDK

An agent built on this SDK must work in an editor built on the official TypeScript SDK, and a client built on this SDK must work with an agent built on the TypeScript SDK. A proxy built on this SDK must work in a proxy chain that the Rust conductor runs. Gate G4 runs one full prompt turn each way, and one through the chain. It needs no model and costs nothing.

## Sub-features

- `ts-client-go-agent`: the TypeScript SDK as the client, `example/agent` as the agent. The client advertises form and url elicitation, fills each form field with `ts-host` and accepts the url, so the turn also carries the stable `elicitation/create` in both modes and `elicitation/complete`.
- `go-client-ts-agent`: `acpharness -profile smoke` as the client, the TypeScript SDK's `src/examples/agent.ts` as the agent.
- `ts-client-conductor-go-proxy`: the TypeScript SDK as the client of `agent-client-protocol-conductor agent acp-example-proxy acp-example-agent`. The example proxy puts `[proxy] ` in front of the text of each `agent_message_chunk` and passes every other message on unchanged.

## How to get to it (user POV)

- An editor on the TypeScript SDK starts an agent built with this SDK.
- A tool built with this SDK starts an agent built with the TypeScript SDK.
- An editor starts a conductor, and the conductor runs a proxy built with this SDK in front of an agent.

## Driving it with harness

Preconditions:

- `"$V" doctor` exits 0. Its `TS agent.ts ready` and `TS ts-client.mjs ready` lines print ok.
- For the chain, its `acp-example-proxy built` and `agent-client-protocol-conductor <version> installed` lines print ok. A `WARN  no conductor` line means that `launch` found no cargo, and the chain drive will be a `SKIP`.

Steps:

- **Run both directions.** Run `"$V" g4`. The first block prints ten PASS lines from the TypeScript client (protocol version, a session id, `end_turn`, an agent message, the allowed permission request, a form elicitation whose answer `ts-host` reached the edit, a url elicitation that the agent completed with its id, a clean wire, the agent's exit 0, and the written transcript) and two `is clean` lines for the stderr files. The second block prints PASS for `initialize`, `session/new`, `prompt`, `agent-exit`, `no-method-not-found`, `no-decode-errors` and `no-agent-errors`.
- **Check the turn.** Run `jq -c '{outcome, kinds, permissions, elicitations}' "$HARNESS_RUN/evidence/g4-ts-client-go-agent/summary.json"`. `outcome.stopReason` is `end_turn`, the Go agent's permission request was answered `allow` and its `content` names `ts-host`, and `elicitations` lists a `form` request for `host` tied to `call_2`, a `url` request, and a `complete` with the url request's `elicitationId`.
- **Run the chain.** The same `"$V" g4` prints a third block, `TS client -> Rust conductor -> Go example proxy -> Go example agent`: the TypeScript client's PASS lines, then `every agent_message_chunk text starts with "[proxy] "`, then two `is clean` lines. Without a conductor the block is one `SKIP` line with the reason, and `report` shows the drive as `SKIP`.
- **Check the chain.** Run `jq -c '{outcome, chunkTexts}' "$HARNESS_RUN/evidence/g4-ts-client-conductor-go-proxy/summary.json"`. Each text in `chunkTexts` starts with `[proxy] `. `jq -c '[.from, .to, .method]' "$HARNESS_RUN/evidence/g4-ts-client-conductor-go-proxy/conductor.trace.jsons"` shows `proxy/initialize` going to `Proxy(0)`.

## Gotchas

- The TypeScript SDK's npm package ships no examples. `launch` downloads `agent.ts` from the git tag that matches the installed version, and fails when the file's import line changes.
- Node runs `agent.ts` without a build step, which needs Node 22.18 or later. `launch` checks `process.features.typescript`.
- The TypeScript example agent sends no `agentInfo`, so the `initialize` detail reads `agent without agentInfo`.
- The conductor drive with the example agent alone (`agent acp-example-agent`) passes every check but the prefix one. When only the prefix check fails, the proxy forwarded the chunks without rewriting them.
- `launch` installs the newest stable conductor from crates.io each time. A conductor release can change the chain, so compare `versions.json` with the last good run before you blame the proxy.
- Stderr lines fail the gate: an `ERROR` or `WARN` line from the Go agent, or from the conductor, proxy or agent of the chain, any line from the TypeScript client, and any error or warning line from the TypeScript agent.
