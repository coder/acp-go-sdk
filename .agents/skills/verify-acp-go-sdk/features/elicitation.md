# Elicitation

The agent asks the user a question during a turn. An editor that advertises `elicitation.form` gets an `elicitation/create` request in `form` mode before the edit of the turn: the agent asks which database host the configuration should use, and the edit that it then asks permission for carries the answer. An editor that advertises `elicitation.url` gets a second request in `url` mode after an allowed edit, a page to review the change. Once the editor accepts it, the agent sends `elicitation/complete` with the same `elicitationId`. An editor that advertises neither gets no elicitation, and the turn is the one in [prompt-turn.md](./prompt-turn.md). Elicitation is stable from ACP schema 1.21.0 (SDK v0.16.0) on.

## Sub-features

- `elicit-form` sends a `form` request tied to the session and to tool call `call_2`, with one required string field `host` whose default is `new-host`.
- `elicit-accept` puts the accepted `host` into the `rawInput.content` of the permission request for `call_2`.
- `elicit-decline` keeps `new-host` after a `decline` or a `cancel`.
- `elicit-url` sends a `url` request after the edit is allowed, only to an editor that advertises `elicitation.url`.
- `elicit-complete` sends `elicitation/complete` with the `elicitationId` of the accepted `url` request, before the turn ends, and nothing after a decline.
- `elicit-gated` sends no elicitation to an editor without the capability, and no `url` request to one that advertises `form` only.
- `elicit-cli` makes `example/client` ask the question on its terminal. See [client-cli.md](./client-cli.md).

## How to get to it (user POV)

- An editor that advertises `elicitation` in `initialize` runs a prompt turn and shows the agent's questions.
- Run `go run ./example/client`: it advertises both modes, asks for the host at `Database host [new-host]:` and for the review at `Open this URL? [y/N]:`.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.
- The turn takes about 5.3 s, as in [prompt-turn.md](./prompt-turn.md).

Steps:

- **Accept both.** Run `"$V" wire elicitation-accept.jsonl elicitation-accept`. Each step prints PASS: `form asks for the host of the edit` matches the whole `requestedSchema`, `edit carries the answer` finds `db.internal` in the permission request, `url asks for a review` and `review completes` follow.
- **Order after accept.** Run `jq -r 'select(.dir=="agent->client") | .msg.params.update.sessionUpdate // .msg.method // ("response " + (.msg.id|tostring))' "$ACP_VERIFY_RUN/evidence/elicitation-accept/transcript.jsonl" | paste -sd' '`. The output is exactly `response 1 response 2 agent_message_chunk agent_message_chunk tool_call tool_call_update agent_message_chunk tool_call elicitation/create session/request_permission tool_call_update agent_message_chunk elicitation/create elicitation/complete response 3`.
- **Same id.** Run `jq -r 'select(.msg.params.mode=="url" or .msg.method=="elicitation/complete") | .msg.params.elicitationId' "$ACP_VERIFY_RUN/evidence/elicitation-accept/transcript.jsonl"`. The output is two equal lines that start with `elic_`.
- **Decline both.** Run `"$V" wire elicitation-decline.jsonl elicitation-decline`. Each step prints PASS. The order is the one after accept without `elicitation/complete`.
- **Form only.** Run `"$V" wire elicitation-form-only.jsonl elicitation-form-only`. Each step prints PASS. The order is `response 1 response 2 agent_message_chunk agent_message_chunk tool_call tool_call_update agent_message_chunk tool_call elicitation/create session/request_permission tool_call_update agent_message_chunk response 3`: one `form` request, then no `url` request.
- **Not advertised.** The order of `prompt-allow` in [prompt-turn.md](./prompt-turn.md) has no `elicitation/create`.
- **Agent log.** Run `cat "$ACP_VERIFY_RUN/evidence/elicitation-accept/agent.stderr.log"`. It is empty, or has one line: `INFO connection closed cause="peer connection closed"`. The agent exits as soon as its connection reports done, and the SDK logs that line after it reports done, so the line is in some logs and not in others.

## Gotchas

- `acpwire` answers `elicitation/create` from `respond` rules. A rule with `where` answers only the requests that match it, so `elicitation-accept.jsonl` answers the two modes differently. A request that no rule matches gets `-32601`, and the agent then ends the turn with that error.
- `waitFor` matches requests from the agent as well as notifications, after the response that it waits for, so a step after the prompt checks the whole turn.
- The example agent writes the host into `rawInput.content` as JSON. A host with a quote stays valid JSON there.
- The `elicitationId` is random per request. Compare the two ids with `jq`, not with a fixed value.
