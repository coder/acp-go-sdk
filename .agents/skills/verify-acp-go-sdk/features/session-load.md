# Session load

An editor reopens a session with `session/load`, for example after it closed the session's tab. The example agent advertises `loadSession: true` in `initialize`. Before it answers, it replays the session's history as `session/update` notifications: each user prompt as `user_message_chunk` updates, and each update that it sent during the session's turns, in the order of the session. Notices are live events, not session history, so a replay leaves them out. The response carries the session's config options. A `systemPrompt` in the load replaces the session's prompt once the replay succeeded, with the same rules as in `session/new`; without one the session keeps its prompt. Tool call ids are unique within the session (`call_1` and `call_2` in its first turn, `call_3` and `call_4` in the next), so a replay of several turns keeps their tool calls apart. The agent keeps sessions in memory, so it loads only a session of the same agent process.

## Sub-features

- `advertise` puts `loadSession: true` in `agentCapabilities`.
- `replay` sends the session's history before the response: the user's prompts and the agent's updates, in order, each once, without notices.
- `system-prompt` applies the `systemPrompt` of the load to the session's later turns, and refuses one that `session/new` would refuse.
- `refuse-cwd` answers a load whose `cwd` differs from the session's with `-32602` and `cwd does not match the session`.
- `refuse-unknown` answers a load of a session that the agent does not have, or that was deleted, with `-32002` (resource not found) and the requested `sessionId` in `data`.

## How to get to it (user POV)

- An editor reads `agentCapabilities.loadSession` from `initialize`, runs turns in a session, and later sends `session/load` for it with the same `cwd`.
- An editor loads a session in another folder, or one that the agent never had.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.
- The turn after the load takes about 5.3 s, as in [prompt-turn.md](./prompt-turn.md).

Steps:

- **Load, replay, then refusals.** Run `"$V" wire session-load.jsonl session-load`. Each step prints PASS. `load with a new system prompt` prints `4 notifications before the response`: two prompts and two replies. `the replay has both prompts` and `the replay has both replies` find the replayed updates. `the turn follows the new system prompt` finds `Following your system prompt: Answer like a parrot.`. `another cwd is refused` prints `error -32602 Invalid params`, and `an unknown session is refused` prints `error -32002 Resource not found`.
- **Order.** Run `jq -r 'select(.dir=="agent->client") | .msg.params.update.sessionUpdate // .msg.method // ("response " + (.msg.id|tostring))' "$ACP_VERIFY_RUN/evidence/session-load/transcript.jsonl" | paste -sd' '`. The output starts with `response 1 response 2 agent_message_chunk response 3 agent_message_chunk response 4 user_message_chunk agent_message_chunk user_message_chunk agent_message_chunk response 5`: the two `/compact` turns, then the replay before the load's response.
- **Agent log.** Run `cat "$ACP_VERIFY_RUN/evidence/session-load/agent.stderr.log"`. It has no `ERROR` line: a refused load is an answer, not an error to log.

## Gotchas

- The journey uses `/compact` turns because each sends one agent message and needs no permission answer. Without notice and compaction capabilities, the summary is an `agent_message_chunk`.
- A turn never sends the user's prompt back. A `user_message_chunk` in the transcript comes only from a replay.
- `waitFor` counts matching messages since the start of the drive, not since the last step. The live `/compact` turns sent one reply of each text, so the replay check for a reply waits for two.
