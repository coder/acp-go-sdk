# Cross-SDK

An agent built on this SDK must work in an editor built on the official TypeScript SDK, and a client built on this SDK must work with an agent built on the TypeScript SDK. Gate G4 runs one full prompt turn each way. It needs no model and costs nothing.

## Sub-features

- `ts-client-go-agent`: the TypeScript SDK as the client, `example/agent` as the agent.
- `go-client-ts-agent`: `acpharness -profile smoke` as the client, the TypeScript SDK's `src/examples/agent.ts` as the agent.

## How to get to it (user POV)

- An editor on the TypeScript SDK starts an agent built with this SDK.
- A tool built with this SDK starts an agent built with the TypeScript SDK.

## Driving it with harness

Preconditions:

- `"$V" doctor` exits 0. Its `TS agent.ts ready` and `TS ts-client.mjs ready` lines print ok.

Steps:

- **Run both directions.** Run `"$V" g4`. The first block prints seven PASS lines from the TypeScript client (protocol version, a session id, `end_turn`, an agent message, the allowed permission request, a clean wire, and the agent's exit 0) and two `is clean` lines for the stderr files. The second block prints PASS for `initialize`, `session/new`, `prompt`, `agent-exit`, `no-method-not-found`, `no-decode-errors` and `no-agent-errors`.
- **Check the turn.** Run `jq -c '{outcome, kinds, permissions}' "$HARNESS_RUN/evidence/g4-ts-client-go-agent/summary.json"`. `outcome.stopReason` is `end_turn`, and the Go agent's permission request was answered `allow`.

## Gotchas

- The TypeScript SDK's npm package ships no examples. `launch` downloads `agent.ts` from the git tag that matches the installed version, and fails when the file's import line changes.
- Node runs `agent.ts` without a build step, which needs Node 22.18 or later. `launch` checks `process.features.typescript`.
- The TypeScript example agent sends no `agentInfo`, so the `initialize` detail reads `agent without agentInfo`.
- Stderr lines fail the gate: an `ERROR` or `WARN` line from the Go agent, any line from the TypeScript client, and any error or warning line from the TypeScript agent.
