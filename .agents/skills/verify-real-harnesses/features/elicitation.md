# Elicitation

An agent asks the user a question in the middle of a turn, and the editor shows it as a form. The user's answer goes back to the agent, and the agent's reply depends on it. ACP schema 1.21.0 makes `elicitation/create` and `elicitation/complete` stable, and `acp.Client` gains `CreateElicitation` and `CompleteElicitation`. The Claude adapter maps its AskUserQuestion tool to a form elicitation, and the Codex adapter sends its questions to the user as form elicitations, once the client advertises `elicitation.form` in `initialize`.

## Sub-features

- `elicitation`: session E asks the agent to ask the user to choose red or blue. The driver answers blue, and the agent replies BLUE.

## How to get to it (user POV)

- Ask the agent something that it must ask you about, in an editor that advertises `elicitation`. The editor shows a form, and the agent goes on with your answer.

## Driving it with harness

Preconditions:

- `"$V" doctor` exits 0, and the adapter's credential variable is set.

Steps:

- **Run the scenario.** Run `"$V" g5 claude elicitation` or `"$V" g5 codex elicitation`. The `elicitation` row prints PASS with the form's message, its field names, the answer (`accept {"question_0":"blue"}` on both adapters on 2026-11-18) and the reply.
- **Read the request.** Run `jq -c 'select(.msg.method=="elicitation/create") | .msg.params' "$HARNESS_RUN/evidence/g5-claude.2/transcript.jsonl"` (the label of the run). Claude ties the form to the AskUserQuestion tool call with `toolCallId`. Codex marks it `"_meta":{"codex":{"delivery":"async"}}` and keeps working while the user answers.
- **Read the answers.** Run `jq -c '.elicitations' "$HARNESS_RUN/evidence/g5-claude.2/summary.json"`. Each entry has its scenario, mode, message, fields and answer.

## Gotchas

- The prompt asks for the color in capital letters, and the predicate looks for `BLUE` and no `RED`. Codex repeats its question with both options in lower case in its reply, so a check for `blue` alone would pass without the answer.
- The driver cancels every elicitation outside this scenario, and every url elicitation, because it opens no browser. In a probe, Codex's async question did not end its turn within 240 s after a `decline`.
- The SDK handles each agent request on its own goroutine, so the scenario compares the decoded requests with the wire as a set, not in order.
- `ElicitationSchema.Properties` holds each field's schema as decoded JSON (`map[string]any`). The driver reads a field into `acp.ElicitationPropertySchema` to see its type.
