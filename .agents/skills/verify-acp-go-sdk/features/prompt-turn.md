# Prompt turn

An editor starts the agent, initializes the connection, opens a session and sends a prompt. The agent streams message chunks and tool calls as `session/update` notifications, asks for permission before its second tool call, and answers the prompt with a stop reason only after it has sent every update of the turn.

## Sub-features

- `turn-init` answers `initialize` with protocol version 1.
- `turn-session` answers `session/new` with a session id that starts with `sess_`.
- `turn-stream` streams chunks, a tool call and its completion, a second chunk and a second tool call, in that order.
- `turn-allow` finishes the edit after the editor allows it, and ends with `end_turn`.
- `turn-reject` skips the edit after the editor rejects it, and ends with `end_turn`.
- `turn-order` sends every update of the turn before the `session/prompt` response.
- `turn-exit` exits 0 when the editor closes the agent's input.

## How to get to it (user POV)

- An editor runs the agent binary and talks ACP over its stdin and stdout.
- A developer runs `go run ./example/agent` and wires it to a client by hand.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.
- The turn takes about 5.3 s, because the example agent pauses between updates.

Steps:

- **Allow.** Play the session and allow the edit. Run `"$V" wire prompt-allow.jsonl prompt-allow`. Each step prints PASS. The prompt step prints `stopReason end_turn, 8 notifications before the response`, and the close step prints `agent exit code 0`.
- **Order after allow.** Print what the agent sent with the order command in the index, for label `prompt-allow`. The output is exactly `response 1 response 2 agent_message_chunk agent_message_chunk tool_call tool_call_update agent_message_chunk tool_call session/request_permission tool_call_update agent_message_chunk response 3`.
- **Tool call states.** Run `jq -r 'select(.dir=="agent->client" and .msg.params.update.sessionUpdate=="tool_call_update") | .msg.params.update | "\(.toolCallId) \(.status)"' "$ACP_VERIFY_RUN/evidence/prompt-allow/transcript.jsonl"`. The output is `call_1 completed` and `call_2 completed`.
- **Permission options.** Run `jq -c 'select(.msg.method=="session/request_permission") | .msg.params.options' "$ACP_VERIFY_RUN/evidence/prompt-allow/transcript.jsonl"`. The output lists `allow` (`allow_once`) and `reject` (`reject_once`).
- **Reject.** Play the session and reject the edit. Run `"$V" wire prompt-reject.jsonl prompt-reject`. Each step prints PASS. The prompt step prints `stopReason end_turn, 7 notifications before the response`, and the wait step finds the chunk ` I understand you prefer not to make that change. I'll skip the configuration update.`
- **Order after reject.** Print what the agent sent for label `prompt-reject`. The output is `response 1 response 2 agent_message_chunk agent_message_chunk tool_call tool_call_update agent_message_chunk tool_call session/request_permission agent_message_chunk response 3`, with no `tool_call_update` after the permission request.
- **Agent log.** Run `cat "$ACP_VERIFY_RUN/evidence/prompt-allow/agent.stderr.log"`. It has no `ERROR` line.

## Gotchas

- The example agent advertises `loadSession: false`. A `session/load` journey tests the SDK's dispatch only, not a feature that the agent offers.
- `session/new` needs `cwd` and `mcpServers`, or it answers `-32602`. An empty list is fine for `mcpServers`.
- The permission answer must be a rule (`respond`) before the prompt. Without one, `acpwire` answers `-32601`, and the agent ends the turn with that error.
- The agent exits as soon as its connection reports done, which can be before it logs `connection closed`. That line is in some logs and not in others. Do not assert on it.
- The update counts come from the example agent's script. A change to `example/agent/main.go` changes them: update this file in the same change.
