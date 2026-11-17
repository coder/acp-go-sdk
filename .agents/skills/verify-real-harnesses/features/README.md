# ACP Go SDK live verification map

This folder lists what the live gates prove about the ACP Go SDK as a client of real agents. Read this index before you drive anything. Then use the matching feature file as the recipe.

## Baseline preconditions

- Run every command from the repository root, with `V=.agents/skills/verify-real-harnesses/scripts/harness`.
- Build a run with `"$V" launch` and export the `HARNESS_RUN` line that it prints. Set `V` and `HARNESS_RUN` again at the top of each later command, because shell state does not carry over between tool calls.
- `"$V" doctor` exits 0, and its `binaries match the checkout` line names the commit under test.
- For G5, the adapter's credential variable is set: `ANTHROPIC_API_KEY` for claude, `OPENAI_API_KEY` for codex. Check it by name with `doctor`. Never print it.

## Driving conventions

- Each `g5` run starts new agent processes with private config folders, so runs never share sessions.
- The adapters and models are whatever is latest and default when `launch` runs. Never pin either one to make a result pass.
- Treat every command as literal. Keep scenario names, method names and option ids unchanged.
- Prefer protocol handles over reply text: method names, `sessionUpdate` kinds, `stopReason`, error codes. The only reply texts that the predicates read are `pong` and the marker lines in the written files.

## Proof and skip reporting

- A G5 proof is the label's `result.txt`, `transcript.jsonl`, `sdk.log` and `agent-<conn>.stderr.log`. `summary.json` has the models, cost and permission answers.
- A failed scenario is a failure, also when the skill's known results predict it. Report it with its evidence.
- A `SKIP` detail starts with its reason. SKILL.md lists the documented reasons, and the `known-skips` row fails the run on any other, such as a scenario blocked by an earlier failure or stopped by the budget. A budget skip is not a pass.
- What the agent advertises comes from the raw `initialize` and `session/new` results, not from the SDK's decoded types. A `decode mismatch` failure means that the SDK read the wire wrong, which is a finding against the SDK, not a reason to skip.

## Feature entry contract

Each feature file starts with an H1 title and one paragraph about what an editor user sees. Then it has exactly four H2 sections in this order:

1. `Sub-features` lists the scenario names, one line each.
1. `How to get to it (user POV)` lists what the user does in an editor.
1. `Driving it with harness` starts with `Preconditions:` and pairs each action with an exact command and the result that a reader can observe.
1. `Gotchas` lists traps that waste a run or make its result wrong.

## Features

- [Sessions](./sessions.md): connect, authenticate, and create, list, close, delete, load and resume sessions.
- [Prompt turns](./prompt-turns.md): a plain turn, a model switch, and a cancelled turn.
- [Permissions](./permissions.md): an edit that the user allows or rejects.
- [Cross-SDK](./cross-sdk.md): the official TypeScript SDK against this SDK, in both directions.
