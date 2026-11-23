---
name: verify-tck
description: Run the ACP Test Compatibility Kit (acp-tck, pinned to one commit) against a conformance agent built on the SDK, and fail when any requirement's result drifts from the checked-in baseline.json. The agent advertises every capability that a v1 requirement of the pinned TCK checks, so those rows run instead of skipping. Use after any change to the agent side of the SDK (agent.go, agent_gen.go, connection.go, decoding, the generated types), after a schema bump, when you move the TCK pin, and when the CI tck job fails or a PR changes baseline.json.
---

# Verify against the ACP TCK

[acp-tck](https://github.com/agentclientprotocol/acp-tck) launches an agent as a stdio subprocess, a fresh process per test, drives it through the protocol and reports a status for each requirement: `PASS`, `FAIL`, `SKIPPED` or `NOT_TESTED`. This gate runs its v1 suite against `agent/`, an agent built on this SDK, and compares the report with `baseline.json`. The gate passes when every result is the one the baseline expects. It fails on a regression and also on an improvement, so the baseline always says what the SDK does today, and a PR that changes a result shows it in `baseline.json`.

| Path | What it is |
| --- | --- |
| `runner/` | A uv project that pins acp-tck to one commit (`pyproject.toml`) with its Python dependencies locked (`uv.lock`). |
| `agent/` | The conformance agent (package main), with offline tests. |
| `tckcheck/` | Compares a report with the baseline, or rewrites the baseline with `-update` (package main), with offline tests. |
| `baseline.json` | The expected tier and status of every requirement, with a reason for each one that does not pass. |
| `run.sh` | Builds the agent and tckcheck, runs the pinned TCK, writes the report and runs tckcheck. |

`go.mod` makes this folder its own module, so it stays out of the SDK's module zip. `make test` vets and tests `agent/` and `tckcheck/` offline; it never runs the TCK.

## Run it

From the repository root:

```bash
make tck          # run the TCK and check the baseline
make tck-update   # run the TCK and rewrite the baseline from the report
```

Both call `run.sh` (`run.sh --update` for the second). It needs Go with cgo and a C compiler (for `-race`) and uv; uv fetches Python 3.14 and the pinned TCK on the first run, so that run needs the network. `run.sh` builds the agent with `-race` and fails when any agent process reports a data race; the TCK starts a fresh process per test and writes its first line at once, which reaches races that the SDK's own tests miss. A run takes about 200 seconds (about 70 without `-race`). On a Coder workspace, set the toolchain first, and name uv's binary to skip the mise shim's lockfile warning:

```bash
export PATH=$HOME/.local/share/mise/installs/go/1.26.3/bin:$PATH; unset GOROOT
export UV=$HOME/.local/share/mise/installs/uv/0.11.17/uv-x86_64-unknown-linux-musl/uv
```

`TCK_OUT` sets where the build, the agent's working directory and the report go (default `${TMPDIR:-/tmp}/verify-tck`). The report is `$TCK_OUT/report.json`: the TCK's full JSON report. A failed test carries its wire transcript and the agent's stderr when the TCK recorded them; a failure the TCK judges from its own probe can have neither.

CI runs `make tck` in the `tck` job of `.github/workflows/ci.yaml`, next to `check`, on every pull request and every push to `main` that changes more than `README.md` (the workflow's `paths-ignore`), and uploads the report as the `tck-report` artifact even when the job fails.

## Read a drift

tckcheck prints one line per drift and a summary, and exits 1 when there is any:

```text
ACP-ERROR-005 ADVISORY: expected FAIL, got PASS
ACP-ERROR-003 ADVISORY: expected PASS, got FAIL: AssertionError: session/prompt for an unknown session: expected code -32002, got -32603; ...
tck 0.5.0, spec 3ea94133: 134 requirements, 2 drifted
```

After `expected`/`got` comes the first line that says why the first test of the requirement did not pass: pytest's first `E` line for a failure, or the reason of a skip. The report has the rest.

| Line | Meaning | What to do |
| --- | --- | --- |
| `expected PASS, got FAIL` (or `SKIPPED`) | A regression, or the agent no longer advertises the capability. | Fix the SDK or the agent. Accept it only with a reason in the baseline. |
| `expected FAIL, got PASS` (or from `SKIPPED`) | An improvement. | `make tck-update`, then commit `baseline.json` with the change. |
| `expected no entry, got ...` | The TCK has a requirement the baseline lacks (a new pin). | `make tck-update` and write its reason. |
| `expected ..., got no requirement` | The baseline names a requirement the TCK no longer has, or lists a `flaky` id the TCK no longer has (`flaky: expected a flaky requirement`). | `make tck-update` drops it. |
| `expected tier X, got Y` | The TCK moved the requirement to another tier. | `make tck-update`. |
| `expected a reason, got TODO` (or `none`) | A requirement that does not pass, or a flaky one, has no reason. | Write one in `baseline.json`. |
| `expected no reason, got a reason for a PASS requirement` | A requirement that passes still has an old reason. | `make tck-update` drops it. |
| `expected a tested requirement, got NOT_TESTED` | No test ran for the requirement: the agent died, a test was deselected or the TCK broke. | Never baselined. Find out why. |
| `run: expected a run that authenticated, got blocked_by_auth` | The TCK could not authenticate, so it skipped every session test. | Never baselined. Check `--auth-method` in `run.sh` against `authMethod` in `agent/agent.go`. |
| `run: expected a run that negotiated the protocol version, got blocked_by_version_mismatch` | The agent did not answer protocol version 1. | Never baselined. |
| `run: expected conformant, got not conformant` (or the reverse) | The TCK's verdict changed. Not reported when a `flaky` requirement changed status, since that alone can flip it. | Follows from the other lines. |
| `run: expected tck 0.5.0, got ...` (or `spec`) | The report comes from another TCK pin than the baseline. | Rerun `make tck-update` after a pin bump. |

`make tck-update` rewrites `baseline.json` from the report. It keeps the reason of every requirement still there, drops the reason of a requirement that now passes, writes `"reason": "TODO"` for one that needs a reason and has none, and then checks the new baseline, so it exits 1 until each TODO has a reason. Review the diff of `baseline.json` before you commit it.

## The baseline

```json
{
  "tckVersion": "0.5.0",
  "schemaRevision": "3ea94133bb4e7cfdb1105df75660ff83e66acf62",
  "conformant": false,
  "flaky": ["ACP-EXAMPLE-001"],
  "requirements": {
    "ACP-CANCEL-001": {"tier":"MANDATORY","status":"PASS"},
    "ACP-ERROR-005": {"tier":"ADVISORY","status":"FAIL","reason":"..."}
  }
}
```

One line per requirement, so a change of one result is one changed line. `reason` says why a requirement does not pass: which SDK or schema gap, or why the row does not apply to this agent. `flaky` (optional) lists requirements whose status changes from run to run; tckcheck accepts any status for them but `NOT_TESTED`, and each needs a reason that says what makes it flip. Prefer fixing the race to listing it. `conformant` is the TCK's verdict: a `CAPABILITY` row that fails makes the run not conformant, and the baseline may accept that with a reason, such as a row that needs a draft field newer than the SDK's schema.

## The agent

The agent keeps its sessions in memory. It advertises the same capabilities whatever the client advertises, because the TCK reads them from an `initialize` of its own with no client capabilities.

| Capability | What the agent does |
| --- | --- |
| `loadSession` | Replays the session's history (user prompts, agent messages, tool calls, plans) before it answers. |
| `promptCapabilities.image`, `audio`, `embeddedContext` | Accepts the blocks and describes them back. |
| `auth.logout`, `auth.status`, auth method `tck` | Logs in and out in memory, so `--auth-method tck --allow-logout` revokes nothing real. After a logout, `session/new` and `session/fork` get -32000 until the next `authenticate`. |
| `sessionCapabilities.list` | One page in creation order, filtered by `cwd` and by `archived`. It issues no cursor, so any cursor gets -32602. |
| `close`, `delete` | Cancel the running turn and stop the session's MCP servers. Both succeed for a session that does not exist. |
| `resume`, `fork` | Resume without replay; fork copies the history and config. It issues no cursor, so a fork at any `cursorId` gets -32602. |
| `archive` | Archive and unarchive; archiving closes the session. |
| `options` | `session/options` and initial `configOptions` on new, load, resume and fork: an option named twice gets -32602, an unknown option or value is skipped. |
| `additionalDirectories`, `systemPrompt` (append) | Accepts absolute directories and text system prompts; rejects anything else with -32602. |
| modes, config options | Modes `ask` and `code`, a `model` select, and a `web_search` boolean only for a client that accepts booleans. |
| stdio MCP servers | Starts each one with its `env` as environment variables and sends MCP `initialize`, then `notifications/initialized`. Stopping one closes its stdin and kills it if it has not exited 2 s later (`mcpStopGrace`). |

The client capabilities it honors: `plan` (a markdown `plan_update` with an absolute path, else baseline `plan` entries), `session.promptSuggestions` (a suggestion after each turn, cleared at the next prompt, never replayed), `subagents` (the two subagent prompts below). It never calls `fs/*`, `terminal/*` or `elicitation/create`.

Unknown sessions get -32002 with the `sessionId` in `data`. A subagent's session gets -32602: the client did not create it and cannot address it.

Its turns are keyed on the prompt text (`behaviorsFor` in `agent/turns.go`). `run.sh` holds the three texts it passes to both the TCK and the agent; the other three are the TCK's own prompt texts, copied from its source.

| Prompt | Turn |
| --- | --- |
| `--cancel-prompt` | Streams a word every 200 ms until cancelled, at most 20 s (`streamLimit`, which must stay below `run.sh`'s `--timeout`). |
| `--subagent-prompt` | Announces a child, exchanges a task and a result with it through `session_message` and `session_message_chunk`, and lets it finish. |
| `--subagent-cancel-prompt` | Announces a child with `capabilities.cancel` that runs until the client cancels it by its id; the parent runs on until it is cancelled itself. |
| `Create a file named tck-permission-probe.txt ...` | Asks permission and writes the file only for the allow option. |
| `Make a step-by-step plan ...` | Reports a plan. |
| `Read the file README.md ...` | Reads it itself and reports a read tool call with an absolute location. |
| anything else | Echoes the prompt. |

It does not advertise MCP over http, sse or acp (no v1 row sends one), providers, `session/status` or `session/set_title`. It does not persist sessions, so the rows that load a session in a second process skip.

## Move the TCK pin

1. Pick a tag or commit of acp-tck and put its full commit SHA in the URL in `runner/pyproject.toml`.
2. In `runner/`, run `uv lock`, and commit `uv.lock` with `pyproject.toml`.
3. Compare the TCK's prompt texts for the permission, plan and README rows with the constants at the top of `agent/turns.go`, and its CLI options with `run.sh`.
4. `make tck-update`. Read every changed line of `baseline.json`: write a reason for each `TODO`, and check that a row that stopped passing is not an SDK bug.
5. `make tck` twice, so a flaky row shows before it lands.

The TCK checks v1 against the draft (unstable) schema at the spec commit it pins (`schema_revision` in the report), so rows for draft fields newer than the SDK's `schema/version` fail or skip until a schema bump brings the field; their reasons say so. After a schema bump, run `make tck` and update the baseline for the rows the bump fixed.

## Scope

- v1 only. The TCK's v2 suite (`--protocol-version 2`, 296 requirements) needs an agent that speaks protocol version 2, which waits for a v2 package in the SDK.
- The agent side only. acp-tck tests agents; it has no client or proxy suite, so `ClientSideConnection` and the proxy are not covered here.
- A report says what this agent does with the SDK, not what every agent built on it does: an agent gets the SDK's wire handling, and its own behavior is its own.
