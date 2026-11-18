# Client system prompt

An editor gives the agent system-level instructions for a session with `systemPrompt` in `session/new`. The example agent advertises `sessionCapabilities.systemPrompt` as `{}` in `initialize`: it takes a prompt in `append` mode, the one mode that every agent with the capability supports. It cannot follow the instructions, because no model runs, so each scripted turn of the session shows them as the agent message `Following your system prompt: <text>`. It refuses a prompt that it cannot apply with `-32602` and creates no session. The client system prompt is stable from ACP schema 1.30.0 (SDK v0.18.0) on.

## Sub-features

- `advertise` puts `systemPrompt: {}` in `agentCapabilities.sessionCapabilities`, with no `mode.override`.
- `append` keeps the text of an `append` prompt with the session and sends `Following your system prompt: <text>` after the disclaimer of each scripted turn. Text blocks are joined with a blank line.
- `refuse-mode` answers a prompt in any other mode, such as `override`, with `-32602` and the data `systemPrompt mode "override" is not supported`.
- `refuse-content` answers an empty `content` with `-32602` and `systemPrompt content is empty`, and a block that is not text with `-32602` and `systemPrompt content must be text blocks`.
- `no-session` creates no session for a refused prompt.

## How to get to it (user POV)

- An editor reads `agentCapabilities.sessionCapabilities.systemPrompt` from `initialize`, sends `session/new` with `systemPrompt`, and runs a prompt turn.
- An editor sends a `systemPrompt` that the agent did not advertise, or that breaks the rules of the protocol.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.
- The turn takes about 5.3 s, as in [prompt-turn.md](./prompt-turn.md).

Steps:

- **Append, then refusals.** Run `"$V" wire system-prompt.jsonl system-prompt`. Each step prints PASS: `send initialize` (the result has `systemPrompt: {}`), `append mode`, `send session/prompt` with `end_turn`, `the turn shows the system prompt`, `override mode is refused`, `empty content is refused`, `a non-text block is refused`, and `a refused prompt creates no session` (`session/list` returns only the first session).
- **Order.** Run `jq -r 'select(.dir=="agent->client") | .msg.params.update.sessionUpdate // .msg.method // ("response " + (.msg.id|tostring))' "$ACP_VERIFY_RUN/evidence/system-prompt/transcript.jsonl" | paste -sd' '`. The output starts with `response 1 response 2 agent_message_chunk agent_message_chunk agent_message_chunk tool_call`: the disclaimer, the system prompt message and the first scripted message, then the turn as in [prompt-turn.md](./prompt-turn.md).
- **Agent log.** Run `cat "$ACP_VERIFY_RUN/evidence/system-prompt/agent.stderr.log"`. It has only the `connection closed` line that [elicitation.md](./elicitation.md) explains: a refused prompt is an answer, not an error to log.

## Gotchas

- The agent shows the prompt only in the scripted turn. `/compact` does not show it.
- `session/load` and `session/resume` also take a `systemPrompt`. The example agent does not advertise either method, so it ignores the field there.
