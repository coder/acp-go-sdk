---
name: verify-real-harnesses
description: Prove the ACP Go SDK against real agents. Gate G5 drives Zed's Claude Code and Codex ACP adapters, at their latest npm versions and on each adapter's own default model, with a client built on this checkout. Gate G4 drives the SDK against the official TypeScript SDK in both directions. Proof is JSON-RPC transcripts, SDK logs, agent stderr, the models seen on the wire and the reported cost. Use before and after a schema bump, or after a change to connection.go, the generated types or the client side, when a unit test cannot show that a real agent still works. For the example binaries alone, use verify-acp-go-sdk.
---

# Verify the ACP Go SDK against real agents

Unit tests and `verify-acp-go-sdk` drive this repository's own example agent. Real agents send messages that the example never sends: `auth/status_update`, `usage_update` with a cost, config options in new categories, and the session lifecycle methods. A schema bump can break any of them, and only a real agent shows it. This skill runs the SDK as the client of the real adapters on real models, and checks that the SDK and the official TypeScript SDK understand each other.

| Gate | Client -> agent | Model | Cost per run |
| --- | --- | --- | --- |
| G4 | TypeScript SDK client -> `example/agent`, and `acpharness` -> the TypeScript SDK's example agent | none | free |
| G5 | `acpharness` -> `@agentclientprotocol/claude-agent-acp` | the adapter's default (`claude-opus-5-5` on 2026-11-17) | about $0.80 |
| G5 | `acpharness` -> `@agentclientprotocol/codex-acp` | the adapter's default (`gpt-6.1-sol` on 2026-11-17) | not reported |

`acpharness` is the driver in `scripts/acpharness`: an ACP client built on the SDK in this checkout. It imports the SDK by module path, so it tests the code in the checkout and not a released version.

The skill never pins an adapter or a model. `launch` resolves the latest npm versions when it runs, and each adapter picks its own default model. Do not point a run at a cheaper or older model to save money. The gate exists to find what breaks on the models that users run.

You need Go, Node 22.18 or later (it runs the TypeScript example agent without a build step), npm, curl, jq and git, plus network access to npm and `raw.githubusercontent.com`. G5 claude needs `ANTHROPIC_API_KEY`, and G5 codex needs `OPENAI_API_KEY`. The skill passes a proxy in `ANTHROPIC_BASE_URL` or `OPENAI_BASE_URL` on to the adapter. The scripts check these variables by name and never print their values. They also need `sha256sum`, `timeout` and `pgrep`, so they run on Linux, and on macOS once GNU coreutils is installed. `launch` checks for every tool.

All commands run from the repository root. Below, `$V` is the helper:

```bash
V=.agents/skills/verify-real-harnesses/scripts/harness
```

## Launch

```bash
export HARNESS_ROOT="${XUM_SCRATCH_DIR:-${TMPDIR:-/tmp}}/acp-harness"   # optional; default ${TMPDIR:-/tmp}/acp-harness
"$V" launch
```

`launch` makes a private run folder and fills `work/` in it:

1. It builds `acpharness` and `acp-example-agent` from the checkout.
1. It runs `npm view` for `@agentclientprotocol/claude-agent-acp`, `@agentclientprotocol/codex-acp` and `@agentclientprotocol/sdk`, and installs each latest version into its own folder under `work/npm/`. Each adapter keeps the TypeScript SDK version that it pins.
1. It downloads `src/examples/agent.ts` from the TypeScript SDK's git tag for the installed version, and points its import at the npm package.
1. It writes `evidence/versions.json`: the checkout, the schema version, Go and Node, each adapter version, the Claude agent SDK and Codex CLI versions bundled in them, and the TypeScript SDK version with the example agent's URL and SHA-256.

The root must be a directory that you own and that other users cannot write to, outside the checkout unless git ignores it. The last line is `export HARNESS_RUN=<run folder>`. Shell state does not carry over between tool calls, so start every later command with both variables:

```bash
V=.agents/skills/verify-real-harnesses/scripts/harness; export HARNESS_RUN=<run folder from launch>
```

## Doctor

Run it first, and again whenever a result looks wrong:

```bash
"$V" doctor
```

It checks that the binaries exist and match the checkout (`<HEAD>+<hash of uncommitted changes>`), that both adapters and the TypeScript SDK are installed at the versions that `versions.json` records, that the TypeScript files are there, and that no process from this run still runs, including a tagged process that an agent left behind in its process group. A `WARN` line says that npm now has a newer adapter than the run, or that a credential variable is unset. Exit 1 means that a check failed. After any change to the checkout, run `launch` again.

## Run the gates

```bash
"$V" g4            # free, about 10 s
"$V" g5 claude     # real model, about 30 s
"$V" g5 codex      # real model, about 30 s
"$V" report        # writes evidence/report.md and prints it
"$V" scan          # exit 1 when a credential value is in evidence/
```

`g4` runs two drives. First, `scripts/g4/ts-client.mjs` uses the TypeScript SDK to run one prompt turn against `acp-example-agent`, and answers its permission request with allow. It advertises form and url elicitation, answers the agent's form with `ts-host` and accepts its url, and checks that the edit carries `ts-host` and that the agent completes the url elicitation with its id. Second, `acpharness -profile smoke` runs `initialize`, `session/new` and one prompt turn against the TypeScript example agent. A drive fails when protocol version 1 is not negotiated, when the turn does not end with `end_turn`, when the agent's permission request does not arrive or is not allowed, when an error response or a non-JSON line crosses the wire, when the SDK logs a "Method not found" or decode error, when the agent does not exit 0 after its input closes, when the agent's stderr has an error or warning line, or when the TypeScript client cannot write its transcript. Each `g4` replaces its two evidence folders, so a failed rerun leaves no stale PASS behind, and `report` shows a drive as PASS only after `g4` has scanned its stderr files.

`g5 ADAPTER` runs every scenario in the next section (`g5 ADAPTER SCENARIO,...` runs some of them, see [Rerun some scenarios](#rerun-some-scenarios)), under a 1800 s limit (`HARNESS_G5_TIMEOUT`) that ends a driver stuck on an agent that stopped reading. After the driver exits, `g5` stops any agent process that it left running. A driver that dies, for example on a Go panic in a generated decoder, exits with a code other than 0, 1 and 64. `g5` then keeps the run's evidence, the panic's trace in `driver.stderr.log` included, prints `INCOMPLETE` and passes on the exit code. Ctrl-C or SIGTERM during `g5` stops the driver and its agents, keeps the evidence, writes `leftovers.txt` and exits 130. Each adapter gets a private config folder in `work/home/`:

| Adapter | Private config | Driver flags |
| --- | --- | --- |
| `claude` | `CLAUDE_CONFIG_DIR=work/home/claude` | `-ask-mode default` |
| `codex` | `CODEX_HOME=work/home/codex` | `-auth api-key -ask-mode read-only` |

Codex ignores `OPENAI_BASE_URL`. When the variable is set, `g5 codex` writes `work/home/codex/config.toml` with a `model_provider` whose `base_url` is the variable's value, `env_key = "OPENAI_API_KEY"` and `wire_api = "responses"`. That file stays in `work/` and never goes into the evidence. Run one `g5` at a time per run folder. A second one refuses to start, because both would read the same remaining budget. Running `g5` again for the same adapter writes a new label, such as `g5-claude.2`. `report` shows a label whose driver did not finish, or left a partial `summary.json` or `result.txt`, as `INCOMPLETE`, with the first line of the driver's stderr, and then exits 1.

### Cost and budget

The Claude adapter reports a cumulative session cost in each `usage_update`. The driver reads that cost from the raw lines on the wire, so the budget holds even when the SDK fails to decode the cost, and `no-decode-errors` fails when the SDK's total differs. It adds the highest cost of each session on each connection, and the total is an upper bound: a resumed session that restores its old cost is counted twice. `HARNESS_BUDGET_USD` (default 25) caps the total that the `g5` runs of one run folder report. `g5` reads the spend so far from the transcripts, so a run that died before its summary still counts. Before each scenario, the driver checks the cost so far. Once the cost reaches the cap, every remaining scenario is a `SKIP` with a budget detail, and the driver exits 1. The cap counts USD only. A cost in another currency goes to `otherCosts` in the summary and the report, outside the cap. The Codex adapter reports no cost, so the cap cannot stop a Codex run. The summary records its tokens instead: the highest usage of each session across all connections, because the schema defines prompt usage as totals across a session's turns. `report` takes the total cost from the transcripts, so it includes a run that died before its summary.

## Scenarios and pass predicates

The driver plays an editor. Connection 1 creates session A for the plain turns, session E for the elicitation turn and session B for the permission and cancel turns. Connections 2 and 3 are new agent processes that reopen session A, the way an editor does after a restart. The client offers no file system and no terminal, so the agent writes files itself. It advertises boolean config options, terminal auth (`auth.terminal`) and elicitation in both modes. It cancels every elicitation outside the `elicitation` scenario and every url elicitation. Each scenario prints one `PASS`, `FAIL` or `SKIP` line.

Whether the agent advertises a session method or a model option comes from the raw JSON of its `initialize` and `session/new` results, as `transcript.jsonl` has them, and never from the SDK's decoded types, because those types are the code under test. When the SDK decodes a capability or a config option differently from the wire, the scenario is a `FAIL` with `decode mismatch`. A renamed JSON tag in the generated types then fails the run instead of skipping the method.

| Scenario | Conn | Pass when |
| --- | --- | --- |
| `initialize` | 1 | The response has protocol version 1, and the auth methods that the SDK decoded, encoded again, equal the wire's `authMethods`. The detail lists `agentInfo`, the auth methods with their types and the session capabilities that the SDK decoded. |
| `authenticate` | 1 | With `-auth`, the agent advertises that method and `authenticate` succeeds. Without `-auth`, it is a `SKIP` (`no -auth`). |
| `session/new` | 1 | Session A gets a session id in its own empty cwd. |
| `prompt` | 1 | Session A answers "Reply with exactly the single word pong" with `end_turn` and a reply that contains `pong`. |
| `config-option` | 1 | The `model` select option that the SDK decoded from `session/new` matches the wire: id, current value and every value in order. The driver sets the `model` option to another model, runs a pong turn, sets it back and runs another. Each response reports the new value. Both pong turns end with `end_turn`, say `pong`, and name their model ids on the wire. The turn after the switch runs on a model id that the first turn did not use, and the turn after the switch back does not run on it. |
| `config-boolean` | 1 | Every config option that the SDK decoded from session A's `session/new` matches the wire: type, id, category and current value, in order. The driver sets the first boolean option in the `model_config` category (else the first boolean option) to the other value and back, and each response reports the new value. The client advertises `session.configOptions.boolean` in `initialize`, so the agent may offer boolean options. |
| `elicitation` | 1 | Session E, in its own cwd, asks the agent to ask the user to choose red or blue, and to reply with the chosen color in capital letters. At least one `elicitation/create` arrives, each one equals a request on the wire once the SDK's decoded request is encoded again, and the client accepts each as a form with the choice that names blue. The turn ends with `end_turn`, and the reply contains `BLUE` and not `RED`. |
| `permission-allow` | 1 | Session B, in the `-ask-mode` mode (set through the `mode` select option when the wire has one, else through `session/set_mode` from the session modes; either must decode as it is on the wire and must offer that mode), asks to write `allow-check.txt`. At least one `session/request_permission` arrives, every request gets an `allow_*` option, and the file exists with its marker line afterwards. |
| `permission-reject` | 1 | The same with `reject-check.txt` and a `reject_*` option for every request. The file does not exist afterwards. Any stop reason passes. |
| `cancel` | 1 | Session B starts a long essay. The driver sends `session/cancel` after the first streamed update, or after 90 s without one while the turn still runs. The turn ends with `cancelled` within 60 s of the cancel. The detail says which case happened. |
| `session/list` | 1 | `session/list` for session A's cwd, through every page, contains session A. |
| `session/close` | 1 | `session/close` for session A succeeds. |
| `session/delete` | 1 | If the agent advertises `session/close`, closing session B succeeds. Session B is in the `session/list` result for its cwd before the delete. Then `session/delete` for it succeeds, and session B is no longer in that list. When the agent does not advertise `session/list`, nothing confirms the delete, and the scenario is a `SKIP` (`not advertised`). |
| `session/load` | 2 | A new process loads session A. Before the response, the agent replays user chunks and agent chunks, and the agent text replayed after the first user chunk contains `pong`. The prompt itself says pong, so a replay of the user's chunks alone fails. |
| `session/resume` | 3 | A new process resumes session A without a replay: no message chunk arrives before the first prompt. A turn that asks for "the single word you replied with" ends with `end_turn` and gets `pong`. |
| `agent-exit` | all | Every agent process exits 0 within 10 s after the driver closes its stdin. |
| `no-method-not-found` | all | The SDK logged no "Method not found" (`-32601`) and sent no `-32601` reply to the agent. |
| `no-decode-errors` | all | The SDK logged no parse, unmarshal or invalid-params error, and no other error-level record except the "Method not found" records that the row above counts, such as a message with neither id nor method. It sent no `-32602` or `-32700` reply, it decoded the same total cost as the raw `usage_update` lines report, and it finished reading and routing each agent's output within 5 s after the agent ended. |
| `no-agent-errors` | all | The agent sent no JSON-RPC error response, including one that the SDK drops because no request waits for it. |
| `no-refused-calls` | all | The agent called no `fs/*` or `terminal/*` method. The client offers neither, so it refuses such a call, and `summary.json` lists it under `refusedCalls`. |
| `known-skips` | all | Every `SKIP` has one of the documented reasons below. |

The model switch never picks a lesser model. It takes the first option, in the agent's order, that is not the current value, not `default`, not named in the current option's name or description (Claude's `default` says `Opus`, so `opus` is an alias), and has no word such as `legacy`, `older`, `fast`, `affordable` or `efficient` in its name or description. `pickSwitchTarget` in `scripts/acpharness/scenarios.go` holds the rule.

A failed scenario is a result. Report it with its evidence. Do not change a prompt or a predicate until the scenario passes, and do not hide a known failure.

### Skip reasons

A `SKIP` detail starts with its reason. These are the documented reasons:

| Reason | Scenarios | When |
| --- | --- | --- |
| `no -auth` | `authenticate` | The adapter table gives the adapter no `-auth` flag (claude). |
| `not advertised` | `session/list`, `session/close`, `session/delete`, `session/load`, `session/resume` | The raw `initialize` result lacks the capability, or has it as `null` or `false`. `session/delete` also skips after a successful delete when the agent does not advertise `session/list`, because nothing confirms the delete. |
| `no model option` | `config-option` | The raw `session/new` result has no select option with category or id `model`. |
| `no other model` | `config-option` | Every other model is the current one, an alias of it or a lesser model, by the rule above. |
| `no boolean option` | `config-boolean` | The raw `session/new` result has no option of type `boolean`. |
| `not selected by -only` | all but `initialize`, `authenticate` and `session/new` | The scenario list of `g5 ADAPTER SCENARIO,...` left the scenario out. |

Any other `SKIP` fails `known-skips`, so the run exits 1: a scenario blocked by an earlier failure (`blocked: ...`), or a scenario that the budget stopped (`budget: ...`).

### Rerun some scenarios

Name the scenarios after the adapter, separated by commas:

```bash
"$V" g5 claude cancel
"$V" g5 codex prompt,session/list
```

The run gets its own label, such as `g5-claude.2`, and `g5` tracks, budgets and reports it like a full run. The driver rejects a name that is not a scenario of the live profile with exit 64, and `g5` then records no run and exits 64. The setup scenarios (`initialize`, `authenticate`, `session/new`) always run, and every other scenario is a `SKIP` with `not selected by -only`. A scenario that needs the state of a left-out one can fail, or skip with `blocked`, which fails `known-skips`. For example, `config-option` without `prompt` has no model to compare with, `session/delete` without a permission or cancel scenario has no session B, and codex-acp does not list session A in `session/list` until `prompt` has run a turn in it.

Do not run `acpharness` by hand against a real adapter. Outside `g5`, nothing records its process groups for `cleanup`, passes it the budget that is left, or counts its cost in `report`.

## Evidence

Each gate writes a folder in `evidence/` of the run:

- `g5-<adapter>/`: `transcript.jsonl` (every JSON-RPC line in both directions, with `t_ms`, `conn`, `scenario` and `dir`), `sdk.log` (the SDK's slog records as JSON, tagged with `conn` and `scenario`), `agent-<conn>.stderr.log` (each line prefixed with milliseconds and the scenario), `summary.json`, `result.txt`, `command.txt`, `budget.txt`, `driver.stderr.log` (the driver's own stderr, empty unless something went wrong, such as a panic) and `leftovers.txt` (the processes that `g5` had to stop after the driver exited). The driver exits 1 when it cannot write any of these files, because a gate must not pass without its proof.
- `g4-ts-client-go-agent/`: the TypeScript client's `transcript.jsonl`, `summary.json` and `result.txt`, plus `agent.stderr.log` and `client.stderr.log`.
- `g4-go-client-ts-agent/`: the same files as a `g5` folder.
- `versions.json` and, after `report`, `report.md`.

`summary.json` holds the results, the global-predicate findings, the methods that the agent sent by count, the model ids seen on the wire, the cost, the tokens, every permission request with its answer, every elicitation with its answer, and the exit of each agent process.

To read one scenario's wire traffic:

```bash
jq -c 'select(.scenario=="cancel") | [.dir, .msg.method // "response", .msg.params.update.sessionUpdate // ""]' "$HARNESS_RUN/evidence/g5-claude/transcript.jsonl"
```

Proof standards:

- Drive the real path: the latest published adapters over real pipes, on the model that each adapter picks.
- Check the side effect, not only the reply. The permission scenarios check the file in the cwd.
- Read `sdk.log` and the transcript for errors, not only the scenario lines.
- Run `"$V" scan` before you paste any evidence into a pull request. It checks the evidence for the values of `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `OPENAI_API_KEY` and `OPENAI_BASE_URL`, and for strings shaped like an API key. It prints file names and variable names, never the values. It fails when `grep` cannot read part of the evidence.

## Cleanup

```bash
"$V" cleanup
```

`cleanup` stops only the processes that this run recorded. It checks each start time and, on Linux, the run's `ACP_HARNESS_TAG` first, so it leaves a reused PID alone, and it never kills by process name. Each agent runs in its own process group, and `cleanup` stops a group only while its recorded leader still runs. When a leader is gone but processes that it started still run in its group, `cleanup` stops them too. `g4` and `g5` give every process that they start the environment variable `ACP_HARNESS_TAG=<run folder name>`, and `cleanup` stops a leftover process only when it carries that tag, so a reused group id never leads it to another user's processes. The tag check reads `/proc`, so on macOS `cleanup` does not stop leftovers. A process or group that still runs 10 s after SIGTERM gets SIGKILL, so no adapter keeps calling a paid model after `cleanup` returns. Then it deletes `work/` (binaries, npm folders, the private agent config and the session cwds) and keeps `evidence/`. Confirm that the proof is still there with `ls "$HARNESS_RUN/evidence"`. A run that was cleaned up cannot be driven again. `report` and `scan` still work on it.

## Helpers

| Helper | Invocation | Purpose |
| --- | --- | --- |
| `scripts/harness` | `"$V" launch`, `doctor`, `g4`, `g5 claude`, `g5 codex`, `report`, `scan`, `cleanup` | Run lifecycle, adapter table, budget, report, credential scan. |
| `scripts/acpharness/` | Built by `launch`, run by `g4` and `g5`: `acpharness [-profile live\|smoke] [-ask-mode MODE] [-auth METHOD] [-budget-usd N] [-only a,b] [-pgids FILE] -out DIR -cwd DIR -- AGENT [ARGS...]` | The ACP client on this checkout's SDK. The comment at the top of `main.go` lists the flags. Use `g5 ADAPTER SCENARIO,...` instead of a direct run against a real adapter. |
| `scripts/g4/ts-client.mjs` | Copied into `work/npm/ts` by `launch`. `node ts-client.mjs --out DIR --cwd DIR -- AGENT [ARGS...]` | One prompt turn with the official TypeScript SDK as the client. |

`./...` skips dot directories, so `make test` names the driver by path. Its unit tests need no network and take about a second. To run them alone:

```bash
go build -o /dev/null ./.agents/skills/verify-real-harnesses/scripts/acpharness/
go test ./.agents/skills/verify-real-harnesses/scripts/acpharness/
```

The driver uses the SDK's generated types. A schema bump that renames a type breaks this build, as schema 1.13.7 did when `session/delete` became stable and `UnstableDeleteSession` became `DeleteSession`. Fix the driver in the same pull request, because the driver is part of the gate.

## Known results at schema 0.13.5

Run on 2026-11-17 with the SDK of `main` at `492745b`, again at `a0c38c3` after #90 regenerated `types_gen.go`, again at `68c27b5` with the wire checks and the `no-refused-calls` and `known-skips` rows, and again at `84bbc4c` after #93 regenerated the decoders in `types_gen.go`, with the same results (schema 0.13.5): claude-agent-acp 0.108.0 (claude-agent-sdk 0.3.326, ACP TypeScript SDK 2.7.0), codex-acp 2.7.0 (Codex CLI 0.166.0, ACP TypeScript SDK 2.6.0), TypeScript SDK 2.7.0.

| Scenario | claude | codex |
| --- | --- | --- |
| `initialize` | PASS | PASS |
| `authenticate` | SKIP | PASS |
| `session/new` | PASS | PASS |
| `prompt` | PASS | PASS |
| `config-option` | PASS | PASS |
| `permission-allow` | PASS | PASS |
| `permission-reject` | PASS | PASS |
| `cancel` | PASS | PASS |
| `session/list` | PASS | PASS |
| `session/close` | PASS | PASS |
| `session/delete` | PASS | PASS |
| `session/load` | PASS | PASS |
| `session/resume` | PASS | PASS |
| `agent-exit` | PASS | PASS |
| `no-method-not-found` | FAIL | PASS |
| `no-decode-errors` | PASS | PASS |
| `no-agent-errors` | PASS | PASS |
| `no-refused-calls` | PASS | PASS |
| `known-skips` | PASS | PASS |

At schema 1.20.0 (`e330c1e`, 2026-11-18), with the same adapter versions, the driver advertises boolean config options, and both adapters then offer them in category `model_config`: Claude `fast` and `ultracode`, Codex `fast-mode`. `config-boolean` passes on both, and every other row is as above.

At schema 1.21.0 (`449d328`, 2026-11-18), with the same adapter versions, the driver advertises `auth.terminal` and elicitation. Claude then lists two terminal auth methods, `claude-login` and `console-login`, with `args`; it lists none without the capability. Codex lists three agent methods (`api-key`, `chat-gpt`, `chat-gpt-device-code`) and no terminal method. Both adapters send `elicitation/create` in `form` mode for the `elicitation` prompt, and the scenario passes on both: Claude for its AskUserQuestion tool, tied to the tool call, with fields `question_0` and `question_0_custom`; Codex as an async question (`_meta.codex.delivery` `async`) with fields `question_0` and `question_0_note`. Neither sends a `url` elicitation or `elicitation/complete`. Every other row is as at 1.20.0. Claude cost $0.88.

Both G4 drives pass. Claude ran on `claude-opus-5-5`, and on `claude-fable-5-2` after the model switch. Codex ran on `gpt-6.1-sol`, and on `gpt-6-astra` after the switch. Claude reported $0.46 to $0.80. Codex reported no cost and used about 46,500 tokens. In four runs at `68c27b5` and three at `84bbc4c`, `g4` took 10 to 11 s, `g5 claude` 17 to 34 s and `g5 codex` 24 to 36 s, most of it model time.

What the results mean:

- **Known SDK failure.** The Claude adapter sends the `auth/status_update` notification on every `session/new`, `session/load` and `session/resume`. The SDK at schema 0.13.5 has no handler for it, so it logs `failed to handle notification method=auth/status_update` with `-32601` three times per run. A schema bump that adds the method must turn `no-method-not-found` to PASS. Both adapters also send `_auth/status_update`. That name starts with `_`, so it is an extension notification, and the SDK ignores it without a log line, as the spec says.
- **A decode bug fails the gate.** With the JSON key `list` renamed to `probeList` in `types_gen.go`, both in the `SessionCapabilities.List` struct tag and in the raw field that `SessionCapabilities.UnmarshalJSON` reads (renaming only the struct tag no longer changes decoding), `g5 codex` fails `session/list` and `session/delete` with `decode mismatch on connection 1: agentCapabilities.sessionCapabilities.list is {} in the initialize result on the wire, but the SDK decoded it as absent`, and exits 1. Before the wire checks, the same probe made `session/list` a `SKIP` and the run exit 0. With `Cost.UnmarshalJSON` made to drop the amount, `g5 claude prompt` fails `no-decode-errors` with `1 (in cost); first: the usage_update costs on the wire add up to $0.0409, but the SDK decoded $0.0000`.
- **A panic in the SDK keeps its proof.** With `SessionUsageUpdate.UnmarshalJSON` in `types_gen.go` made to panic on a non-null `cost`, the driver of `g5 claude prompt` dies with exit 2 in the first turn. `g5` keeps `transcript.jsonl`, `sdk.log`, the agent's stderr and the panic in `driver.stderr.log`, stops the agents, writes `leftovers.txt` and prints `INCOMPLETE`. `report` lists `g5-claude` as `INCOMPLETE` with `panic: probe: SDK panic while decoding Cost`, counts the $0.04 that its transcript reports, and exits 1. Before the driver's usage errors moved to exit 64, `g5` read that exit 2 as rejected arguments and deleted the run's evidence.
- **Claude's default mode does not ask.** A new Claude session starts in mode `auto`, which writes files without `session/request_permission`. The driver sets mode `default` on session B.
- **Codex's reject option ends the turn.** Codex offers `cancel` (kind `reject_once`) as its reject option, and the turn then ends with `cancelled`. The reject predicate accepts any stop reason.
- **Codex and symlinked cwds.** codex-acp reports each session's cwd as a real path. With a cwd that goes through a symlink, `session/list` filtered by that cwd returns nothing, and an approved edit fails with "Failed to write file". The driver resolves symlinks in `-cwd` to stay clear of this adapter bug. Keep `HARNESS_ROOT` free of symlinks anyway.
- **Claude resumes on the last model.** A resumed Claude session runs on the model of its last turn, not on the session's `default` option.
- **Codex leaves a git fetch running.** In four of the eleven full runs that checked for leftovers, codex-acp's process group still had three processes 3 s after codex-acp exited 0. The last run named them: `git ls-remote https://github.com/openai/plugins.git HEAD` and its `git remote-https` helper, which Codex starts to check its plugin repository. Once, a process in claude-agent-acp's group still ran right after the adapter exited, before `g5` waited 3 s. `g5` stops such processes, and `leftovers.txt` and `report` list them with their command lines.

To keep this skill true as the adapters change, use the pstack plugin's `maintain-verification-skill` skill. It is hidden from the default skill list, so load it by name. The [feature map](features/README.md) has one recipe per area.
