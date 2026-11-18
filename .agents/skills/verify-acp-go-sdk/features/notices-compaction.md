# Notices, compaction and tool names

The agent tells the user something outside the conversation with a `notice` update, compacts a session on the prompt `/compact`, and names the tool of each tool call. An editor that advertises `session.notices` in `initialize` gets the example agent's demo disclaimer as an `info` notice, and any other editor gets it as an agent message. An editor that advertises `session.compaction` sees `/compact` as a `compaction_update` that starts, a `compaction_summary_chunk` with the summary and a `compaction_update` that completes, all with one `compactionId`. Any other editor gets the summary as an agent message. Every editor gets the tool name `read_file` on tool call `call_1` and `write_file` on `call_2` and on its permission request. The tool call `name` is stable from ACP schema 1.22.0, and notices and compaction from 1.25.0 (SDK v0.17.0) on.

## Sub-features

- `notice` sends the disclaimer as a `notice` with severity `info`, title `ACP Go Example Agent — demo only` and description `No AI model runs. Every turn is scripted.`, only to an editor that advertises `session.notices`.
- `notice-gated` sends the disclaimer as the agent message `ACP Go Example Agent — demo only (no AI model).` to any other editor.
- `tool-name` sets `name` on both `tool_call` updates and on the `toolCall` of `session/request_permission`.
- `compact` answers the prompt `/compact` with `compaction_update` (`in_progress`), `compaction_summary_chunk` and `compaction_update` (`completed`), with one `compactionId` that starts with `cmp_`, and ends the turn with `end_turn`.
- `compact-gated` answers `/compact` with one agent message that holds the summary, for an editor without `session.compaction`.
- `notice-cli` makes `example/client` print the notice. See [client-cli.md](./client-cli.md).

## How to get to it (user POV)

- An editor that advertises `session.notices` and `session.compaction` runs a prompt turn and then sends `/compact`.
- An editor that advertises neither does the same.
- Run `go run ./example/client`: it advertises `session.notices` and prints the notice after `📣 [info]`, and each tool call with its name in brackets.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.
- The first turn takes about 5.3 s, as in [prompt-turn.md](./prompt-turn.md). `/compact` takes about 0.5 s.

Steps:

- **Both capabilities.** Run `"$V" wire notices-compaction.jsonl notices-compaction`. Each step prints PASS: `disclaimer is a notice`, `read tool is named`, `edit tool is named`, `permission names the tool`, then `compaction starts`, `summary streams` with the text `Compacted 1 earlier prompt(s) of this demo session.` and `compaction completes`.
- **Order with both.** Run `jq -r 'select(.dir=="agent->client") | .msg.params.update.sessionUpdate // .msg.method // ("response " + (.msg.id|tostring))' "$ACP_VERIFY_RUN/evidence/notices-compaction/transcript.jsonl" | paste -sd' '`. The output is exactly `response 1 response 2 notice agent_message_chunk tool_call tool_call_update agent_message_chunk tool_call session/request_permission tool_call_update agent_message_chunk response 3 compaction_update compaction_summary_chunk compaction_update response 4`.
- **One compaction id.** Run `jq -r 'select(.msg.params.update.compactionId) | .msg.params.update.compactionId' "$ACP_VERIFY_RUN/evidence/notices-compaction/transcript.jsonl" | sort -u`. The output is one line that starts with `cmp_`.
- **Neither capability.** Run `"$V" wire notices-compaction-off.jsonl notices-compaction-off`. Each step prints PASS: `disclaimer is a message` and `summary is a message`. The order is exactly `response 1 response 2 agent_message_chunk agent_message_chunk tool_call tool_call_update agent_message_chunk tool_call session/request_permission tool_call_update agent_message_chunk response 3 agent_message_chunk response 4`, with no `notice` and no `compaction_*` kind.
- **Agent log.** Run `cat "$ACP_VERIFY_RUN/evidence/notices-compaction/agent.stderr.log"`. It is empty, or has the one `connection closed` line that [elicitation.md](./elicitation.md) explains.

## Gotchas

- `/compact` is a command only as the whole text of a one-block prompt. Any other prompt, `/compact now` included, runs the scripted turn.
- The summary counts the session's earlier prompts, so it says `1` only when `/compact` is the second prompt of the session.
- The `compactionId` is random per compaction. Compare the ids with `jq`, not with a fixed value.
