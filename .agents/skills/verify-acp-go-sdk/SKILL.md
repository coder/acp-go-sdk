---
name: verify-acp-go-sdk
description: Drive the ACP Go SDK through its example agent and example client the way an editor and a developer use them, and capture proof (JSON-RPC transcripts over stdio, CLI output, exit codes, agent logs). Use when a change to connection.go, agent or client code, generated handlers or the examples must be shown to work for a real prompt turn, cancel, error reply or CLI run, or when you reproduce a bug report against real binaries. For the notification barrier's interleavings, use verify-notification-barrier instead.
---

# Verify the ACP Go SDK by driving it

This repository is a library, so a user touches it in two ways:

1. An editor (Zed, or any ACP client) starts an agent built on the SDK and talks newline-delimited JSON-RPC to it over stdin and stdout. `example/agent` is that agent. The `acpwire` helper plays the editor.
2. A developer runs a client built on the SDK. `example/client` is that client: it starts the agent, runs one prompt turn and asks for permission on its terminal.

`go test ./...` covers the library API. The bridges in `example/claude-code` and `example/gemini` need external CLIs, network access and paid model credentials, so the map does not cover them yet.

There is no server and no port. Each drive starts its own agent process with private pipes, so runs never share state, and two agents can verify side by side. A run is a folder with the built binaries (`work/`) and the proof (`evidence/`).

All commands run from the repository root. Below, `$V` is the helper:

```bash
V=.agents/skills/verify-acp-go-sdk/scripts/acp-verify
```

## Launch

```bash
export ACP_VERIFY_ROOT="${XUM_SCRATCH_DIR:-${TMPDIR:-/tmp}}/acp-verify"   # optional; default ${TMPDIR:-/tmp}/acp-verify
"$V" launch
```

`launch` builds `acp-example-agent`, `acp-example-client` and `acpwire` from the current checkout into a new run folder. It needs only Go. Its last line is `export ACP_VERIFY_RUN=<run folder>`. Shell state does not carry over between tool calls, so start every later command with both variables:

```bash
V=.agents/skills/verify-acp-go-sdk/scripts/acp-verify; export ACP_VERIFY_RUN=<run folder from launch>
```

The run is ready when `doctor` exits 0. Nothing else needs to start, migrate or seed: every drive starts a fresh agent.

## Doctor

Run it first, and again whenever a result looks wrong:

```bash
"$V" doctor
```

It reads the run folder and checks four things: the three binaries exist, they were built from the current checkout, the agent starts and exits 0 when its input closes, and no process that this run started is still running. The checkout is shown as `<HEAD>+<hash>`, where the hash covers uncommitted changes to Go files and to this skill. `+e3b0c44298fc` (the hash of nothing) means no uncommitted changes. Exit 1 means a check failed. If the binaries are stale, run `launch` again and use the new run.

## Drive

Read [features/README.md](features/README.md) first. Each feature file gives the exact commands and the end state that proves it.

Drive the agent with a journey, as an editor would:

```bash
"$V" wire prompt-allow.jsonl prompt-allow     # journey file in journeys/, evidence label
```

`acpwire` starts the agent, plays the journey, answers the agent's requests (such as `session/request_permission`) from rules in the journey, and prints one PASS or FAIL line per step. Exit 1 means a step failed. A journey is one JSON object per line. The comment at the top of `scripts/acpwire/main.go` lists every step type. Request ids count up from 1 in the order of the `send` steps, so a `$/cancel_request` can name an earlier request by its number.

Drive the example client, as a developer at a terminal would:

```bash
"$V" client client-allow '1\n'     # evidence label, then the keys typed at its prompts
```

The client reads its permission choice from stdin, so `client` gives it the keys from a file. It stops the client after `ACP_VERIFY_TIMEOUT` seconds (default 60, exit 124) or after 1 MiB of output (exit 141).

## Evidence

Each drive writes `evidence/<label>/` in the run folder:

- `wire`: `transcript.jsonl` (every line in both directions, with a timestamp and direction), `agent.stderr.log` (the agent's log, each line prefixed with milliseconds since `acpwire` started), `summary.json`, `result.txt` (the step lines and `exit=`), `journey.jsonl` and `command.txt`.
- `client`: `stdout.txt`, `stderr.txt`, `stdin.txt`, `result.txt` (`exit=` and duration) and `command.txt`.
- `build.env`: the source state and Go version that the binaries came from.

Proof standards:

- Drive the real path: the built example binaries over real pipes. Do not call SDK functions from a test as proof that a user path works.
- Capture the action and the result. A turn's proof is its transcript, not only the final `stopReason`.
- Check order and side effects, not only the last message: count the updates before the response, read the agent log for errors, and check the exit code after the input closes.
- No mocks. `acpwire` is a real client on the wire, and each answer it gives the agent is written in the journey.
- A failed step is a result. Report it with its evidence, and do not change the journey until it passes.

## Cleanup

```bash
"$V" cleanup
```

`cleanup` stops only the processes that this run recorded, and their direct children. It never kills by process name. Then it deletes `work/` (binaries and process list) and keeps `evidence/`. It prints the evidence path. A run that was cleaned up cannot be driven again: launch a new one.

After cleanup, confirm that the proof is still there, for example with `ls "$ACP_VERIFY_RUN/evidence"`.

## Helpers

| Helper | Invocation | Purpose |
| --- | --- | --- |
| `scripts/acp-verify` | `"$V" launch`, `doctor`, `wire JOURNEY LABEL`, `client LABEL KEYS`, `cleanup` | Run lifecycle, process tracking, evidence layout. |
| `scripts/acpwire/main.go` | Built by `launch`. Directly: `"$ACP_VERIFY_RUN/work/bin/acpwire" -agent BIN -journey FILE -out DIR` | Plays an ACP client against any stdio agent binary. Go standard library only. |
| `journeys/*.jsonl` | `"$V" wire NAME.jsonl LABEL` | The scripted editor sessions that the feature files use. |

`acpwire` works with any ACP agent binary, not only the example. To drive another agent, run the binary directly with `-agent`, and use a journey whose answers fit that agent.

## Known defects that the map reports

These are current failures. Each feature file says how its journey shows them. Do not weaken a journey to hide one.

- `$/cancel_request` does not stop a turn of the example agent, because its `Prompt` ignores the request ctx ([cancel.md](features/cancel.md)).
- The example client shows a tool call status as a pointer address, such as `0x29c44f7ab480`, not `completed` ([client-cli.md](features/client-cli.md)).
- The example client can show the permission prompt before the tool call that it asks about, because the SDK runs incoming requests concurrently with the notification queue ([client-cli.md](features/client-cli.md)).
- At end of input, the example client prints `Choose an option:` in a loop without end ([client-cli.md](features/client-cli.md)).
- A notification handler that makes a request on the same connection never gets its result. `verify-notification-barrier` proves it, and no example reaches it.

To keep the map true as the app changes, use the pstack plugin's `maintain-verification-skill` skill. It is hidden from the default skill list, so load it by name.
