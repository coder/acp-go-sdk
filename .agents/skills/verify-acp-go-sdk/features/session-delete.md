# Session delete

A user removes a chat from the editor's session history. The editor calls `session/list` to show the agent's sessions, sends `session/delete` for the one that the user removed, and lists again. The deleted session is gone from the list, and the others stay. The agent advertises both methods in `initialize`, as `sessionCapabilities.list` and `sessionCapabilities.delete`. `session/delete` is stable from ACP schema 1.13.7 (SDK v0.14.0) on.

## Sub-features

- `delete-advertised` shows `sessionCapabilities.list` and `sessionCapabilities.delete` as `{}` in the `initialize` result.
- `delete-listed` drops a deleted session from `session/list`, and keeps the other sessions of its cwd in creation order.
- `delete-again` answers a second `session/delete` for the same session with an empty result, as the ACP spec asks for an unknown session.
- `delete-then-prompt` answers `session/prompt` for a deleted session with `-32002` (resource not found) and the session's ID in `data.sessionId`, as the ACP spec asks for an unknown session.
- `delete-running` ends a running turn of the deleted session with `stopReason: "cancelled"`.
- `delete-cwd` leaves the sessions of another cwd in the list.

## How to get to it (user POV)

- Delete a chat from the editor's history view: the editor sends `session/delete` with its session id, and then `session/list` to refresh the view.
- Delete a chat while its turn still runs.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.
- The journey makes sessions A and B in cwd `/tmp/acp-delete` and session C in `/tmp/acp-keep`. The agent keeps sessions in memory only, so no folder is read or created.

Steps:

- **Delete sessions.** Run `"$V" wire session-delete.jsonl session-delete`. Each step prints PASS. `prompt A fails` prints `error -32002 Resource not found`. `await turn` prints `stopReason cancelled, 3 notifications before the response`, and `close stdin` prints `agent exit code 0`.
- **Read the order.** Run `jq -r 'select(.dir=="agent->client") | .msg.params.update.sessionUpdate // .msg.method // ("response " + (.msg.id|tostring))' "$ACP_VERIFY_RUN/evidence/session-delete/transcript.jsonl" | paste -sd' '`. The output is exactly `response 1 response 2 response 3 response 4 response 5 response 6 response 7 response 8 response 9 agent_message_chunk agent_message_chunk tool_call response 11 response 10 response 12 response 13`. Request 11 is the delete of B, and request 10 is B's prompt, so the delete answers before the cancelled turn does.
- **Agent log.** Run `cat "$ACP_VERIFY_RUN/evidence/session-delete/agent.stderr.log"`. It is empty, or has one line: `INFO connection closed cause="peer connection closed"`. The agent exits as soon as its connection reports done, and the SDK logs that line after it reports done, so the line is in some logs and not in others.

## Gotchas

- The `expect` values compare whole JSON values. `result.sessions` must match the full list, with each `sessionId` taken from a saved `session/new` response through `{{name.result.sessionId}}`.
- The ACP spec asks for `-32002` with `data.sessionId` when a session-scoped request names a session that does not exist. The example agent's `Prompt` returns `acp.NewResourceNotFound` for a session that is not in its map, so a deleted session gets it.
- The delete of B must come after the first `tool_call` update, or the turn has not started and the await step sees `end_turn`.
