# Cancel

A user presses stop in the editor while a turn runs. The editor sends `session/cancel`, and the agent stops streaming and answers the open prompt with `stopReason: "cancelled"`. An editor can also cancel any single request with `$/cancel_request`, and the agent then ends that request.

## Sub-features

- `cancel-session` ends a running turn with `cancelled` after `session/cancel`.
- `cancel-quiet` sends no update after the cancelled response.
- `cancel-request` ends an in-flight `session/prompt` after `$/cancel_request` names its id.

## How to get to it (user POV)

- Press stop in the editor during a turn: the editor sends `session/cancel` with the session id.
- An editor that cancels one request sends `$/cancel_request` with that request's id.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.
- Both journeys cancel after the first `tool_call` update, about 1.3 s into the turn.

Steps:

- **Stop a turn.** Run `"$V" wire cancel-session.jsonl cancel-session`. Each step prints PASS. The await step prints `stopReason cancelled, 3 notifications before the response`.
- **Nothing after the stop.** Run `jq -r 'select(.dir=="agent->client") | .msg.params.update.sessionUpdate // .msg.method // ("response " + (.msg.id|tostring))' "$ACP_VERIFY_RUN/evidence/cancel-session/transcript.jsonl" | paste -sd' '`. The output is exactly `response 1 response 2 agent_message_chunk agent_message_chunk tool_call response 3`. The journey waits 1.5 s after the response, so a late update would show here.
- **Cancel one request.** Run `"$V" wire cancel-request.jsonl cancel-request`. Each step prints PASS. The await step prints `stopReason cancelled, 3 notifications before the response`. Then run the `jq` command from **Nothing after the stop** on `evidence/cancel-request/transcript.jsonl`. The output is exactly `response 1 response 2 agent_message_chunk agent_message_chunk tool_call response 3`.

## Gotchas

- On `$/cancel_request`, the ACP spec lets the agent answer the request with a valid response or with error `-32800`. The example agent answers `session/prompt` with `stopReason: "cancelled"`, because its `Prompt` derives the turn ctx from the request ctx that the SDK cancels. An agent that returns the ctx error (`context.Canceled`) instead gets `-32800` from the SDK (`errors.go`). Both are correct. Change the journey's `expect` to the one the agent under test sends.
- Request ids count up from 1 in send order. In `cancel-request.jsonl`, the prompt is request 3. If you add a `send` step before it, update `requestId`.
- `session/cancel` for an unknown session is a no-op. It gets no reply, because it is a notification.
- The `cancelled` stop reason comes from the example agent, which checks its own ctx after the cancel. An agent that ignores cancel shows `end_turn` here.
