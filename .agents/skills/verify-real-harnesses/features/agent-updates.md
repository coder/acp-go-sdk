# Tool names, notices and compaction

An editor shows which tool the agent runs, tells the user about a problem that is not part of the conversation, such as an MCP server that failed to start, and shows when the agent compacts the conversation to free its context window. ACP schema 1.22.0 makes the tool call `name` stable, and schema 1.25.0 makes the `notice`, `compaction_update` and `compaction_summary_chunk` session updates stable. An agent sends a notice only to a client that advertises `session.notices` in `initialize`, and compaction updates only to one that advertises `session.compaction`. Without them, the Claude adapter shows a compaction as a tool call and a notice as an agent message.

## Sub-features

- `tool-call-name`: every tool call on the wire, in `tool_call`, `tool_call_update` and `session/request_permission`, decodes with the name it has on the wire, and at least one has a name.
- `notice`: session N asks for an MCP server that cannot start, and the agent sends a `notice` about it.
- `compaction`: session C runs `/compact`, and the agent sends `compaction_update`s that end with `completed`.

## How to get to it (user POV)

- Run any tool, such as a file edit: the editor labels the tool call with its `name` and asks for permission with it.
- Configure an MCP server whose command is missing: the editor shows the agent's notice, such as `MCP server "harness-broken" failed to start`, apart from the chat.
- Type `/compact`, which both adapters list in `available_commands_update`: the editor shows the compaction in progress, then its summary.

## Driving it with harness

Preconditions:

- `"$V" doctor` exits 0, and the adapter's credential variable is set.

Steps:

- **Run the scenarios.** Run `"$V" g5 claude notice,compaction` or `"$V" g5 codex notice,compaction`. The `notice` row lists each notice's severity, title and description, and the `compaction` row lists each `compactionId` with its statuses in wire order and the size of its summary.
- **Read the updates.** Run `jq -c 'select(.dir=="agent->client") | .msg.params.update | select(.sessionUpdate=="notice" or (.sessionUpdate|startswith("compaction")))' "$HARNESS_RUN/evidence/g5-claude.2/transcript.jsonl"` (the label of the run).
- **Check the tool names.** A full run, `"$V" g5 claude`, ends with the `tool-call-name` row, which counts the names on each kind of tool call and lists the distinct names.

## Gotchas

- The Claude adapter reports the failed MCP server in its first prompt turn, from the agent SDK's `system/init` message, so the notice arrives during the pong turn, not after `session/new`. The scenario counts every update of session N.
- codex-acp sends no notice for an MCP server that cannot start. It sends a `tool_call` with status `failed` and title `mcp__harness-broken__startup`, without a name. Its notices come from Codex config warnings, deprecation notices, `warning` events, hooks that stop a turn and model reroutes, which the driver does not provoke. The adapter table gives codex `-sends-none notice`, so its `notice` row is a `SKIP` with `adapter sends none`.
- codex-acp 2.7.0 names only a few tools, such as `view_image` and `request_permissions`, and none of the file edits and permission requests that a run makes. The adapter table gives codex `-sends-none tool-call-name`, so its `tool-call-name` row is a `SKIP` with `adapter sends none`, and still fails on a decode mismatch: every unnamed tool call must decode without a name.
- A `-only` run without a scenario that uses a tool has no tool call, so `tool-call-name` skips with `not selected by -only`.
- The Claude adapter sends `completed` twice for one compaction, the first time with the summary. The predicate reads the last `compaction_update` of each id.
