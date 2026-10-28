# ACP Go SDK verification map

This folder is the maintained source for verifying what users of the ACP Go SDK see. Read this index before you drive anything. Then use the matching feature file as the recipe.

## Baseline preconditions

- Run every command from the repository root, with `V=.agents/skills/verify-acp-go-sdk/scripts/acp-verify`.
- Build a run with `"$V" launch` and export the `ACP_VERIFY_RUN` line that it prints. Set `V` and `ACP_VERIFY_RUN` again at the top of each later command, because shell state does not carry over between tool calls.
- `"$V" doctor` exits 0, and its `binaries match the checkout` line names the commit under test.
- Each drive starts its own example agent. No other state exists, so there is no seed data to restore.
- Never drive a run folder that `launch` did not make. The helper refuses folders without its marker.

## Driving conventions

- Start each recipe from a fresh agent. `wire` and `client` always start one.
- Drive the agent with journeys in `../journeys/`. Each journey plays an editor session and answers the agent's requests from rules in the file.
- Drive the client CLI with `"$V" client LABEL KEYS`. KEYS are the characters typed at its prompts, with `\n` for Enter.
- Treat every command as literal. Keep method names, option ids and journey names unchanged.
- Use a new evidence label for each drive, so that no proof overwrites another.
- Prefer protocol handles over printed text: method names, `sessionUpdate` kinds, `toolCallId`, `stopReason`, error codes.

## Proof and skip reporting

- A wire proof is `result.txt` (one PASS or FAIL line per step), `transcript.jsonl` and `agent.stderr.log`.
- A CLI proof is `stdout.txt`, `stderr.txt`, the keys in `stdin.txt` and the exit code in `result.txt`.
- Prove order from the transcript. The feature files print what the agent sent, in order, with this `jq` filter on a label's `transcript.jsonl`: `select(.dir=="agent->client") | .msg.params.update.sessionUpdate // .msg.method // ("response " + (.msg.id|tostring))`. Each feature step gives the full command.
- Record the feature file, sub-feature id and entry point with each artifact.
- A step that fails is a failure, also when a gotcha predicts it. Report it with its evidence.
- Report an entry point that you could not reach with the command that you tried and the missing precondition. Do not report it as verified through another entry point.

## Feature entry contract

Each feature file starts with an H1 title and one paragraph about the behavior that a user sees. Then it has exactly four H2 sections in this order:

1. `Sub-features` lists short ids, one line each.
1. `How to get to it (user POV)` lists every way a user reaches the feature.
1. `Driving it with acp-verify` starts with `Preconditions:` and pairs each user action with an exact command and the result that a reader can observe.
1. `Gotchas` lists traps that waste a run or make its result wrong.

Keep implementation details out of the map, except where a gotcha needs one to explain a known failure.

## Features

- [Prompt turn](./prompt-turn.md): an editor runs a turn with streamed updates and a permission request, allowed or rejected.
- [Cancel](./cancel.md): `session/cancel` ends a turn, and `$/cancel_request` cancels a request.
- [Errors and extensions](./errors-and-extensions.md): error replies, `_` extension methods, malformed input and end of input.
- [Example client CLI](./client-cli.md): a developer runs `example/client` and answers its permission prompt.

## Not mapped yet

- `example/claude-code` and `example/gemini` bridge to external agent CLIs. They need `npx` or the `gemini` binary, network access and model credentials that cost money. Map them when a run can provide those.
