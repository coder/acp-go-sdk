# Example client CLI

A developer runs the example client in a terminal. It starts the example agent, connects, opens a session, prints the streamed turn, asks the developer to allow or skip an edit, and prints `✅ Agent completed` after the last update of the turn.

## Sub-features

- `cli-connect` prints `✅ Connected to agent (protocol v1)` and `📝 Created session: sess_...`.
- `cli-stream` prints each chunk and each tool call with its status.
- `cli-allow` applies the edit after `1`, and prints ` Perfect! I've successfully updated the configuration. The changes have been applied.`
- `cli-reject` skips the edit after `2`, and prints ` I understand you prefer not to make that change. I'll skip the configuration update.`
- `cli-invalid` prints `Invalid option. Please try again.` after an unknown choice and asks again.
- `cli-done` prints `✅ Agent completed` after the last chunk, and exits 0.
- `cli-default-agent` starts the agent with `go run` when no agent path is given.
- `cli-eof` answers `cancelled` when its input ends at the permission prompt, and exits 0.

## How to get to it (user POV)

- Run `go run ./example/client` from the repository root (the README entry point). It runs `go run` on `example/agent`.
- Run a built client with an agent command: `acp-example-client <agent binary> [args...]`.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.
- Each run takes about 5.3 s.

Steps:

- **Allow.** Type `1` at the prompt. Run `"$V" client client-allow '1\n'`. Exit 0. `stdout.txt` has, in this order: `✅ Connected to agent (protocol v1)`, `📝 Created session: sess_`, `🔧 Reading project files (pending)`, `🔐 Permission requested: Modifying critical configuration file`, `Choose an option:`, ` Perfect! I've successfully updated the configuration. The changes have been applied.`, `✅ Agent completed`.
- **Tool call status.** Run `grep 'Tool call' "$ACP_VERIFY_RUN/evidence/client-allow/stdout.txt"`. Expected: `` 🔧 Tool call `call_1` updated: completed ``, then `` 🔧 Tool call `call_2` updated: completed ``. A pointer address such as `0x29c44f7ab480` instead of `completed` is a FAIL. See Gotchas.
- **Reject after a wrong key.** Type `9`, then `2`. Run `"$V" client client-reject '9\n2\n'`. Exit 0. `stdout.txt` has `Invalid option. Please try again.`, then ` I understand you prefer not to make that change. I'll skip the configuration update.`, then `✅ Agent completed`.
- **Done after the last chunk.** Run `tail -n 3 "$ACP_VERIFY_RUN/evidence/client-allow/stdout.txt"`. The last line is `✅ Agent completed`, and no chunk text comes after it.
- **Tool call before its permission prompt.** Run `grep -n 'Modifying critical configuration file' "$ACP_VERIFY_RUN/evidence/client-allow/stdout.txt"`. Expected: the `🔧 ... (pending)` line comes before the `🔐 Permission requested` line in every run. A wrong order in any run is a defect. See Gotchas.
- **Default agent.** Run `"$V" client-default client-default '1\n'`. Exit 0. `stdout.txt` ends with `✅ Agent completed`, and `result.txt` has `leftover=none`. A `LEFTOVER:` line (exit 1) lists each process that the client started and that still runs 3 s after it exited. See Gotchas.
- **End of input.** Give no keys. Run `ACP_VERIFY_TIMEOUT=15 "$V" client client-eof ''`. Exit 0 in about 4.3 s. `stdout.txt` has `Choose an option:` once, then `✅ Agent completed`. It has neither the allow chunk ` Perfect! I've successfully updated the configuration.` nor the reject chunk ` I understand you prefer not to make that change.` An earlier chunk, ` Now I understand the project structure.`, is in every run, so match the full chunk text. Exit 141 and `OUTPUT CAP` is a FAIL. See Gotchas.

## Gotchas

- The tool call status in a `ToolCallUpdate` is a `*ToolCallStatus`. The client prints the value it points to, and an empty string when the agent sends no status. An address in the line means the pointer is printed again.
- The SDK starts the handler of an incoming request such as `session/request_permission` only after the handlers of the `session/update` notifications that arrived before it, unless the handler that runs waits on the agent's answer to its own request. The example client's handlers make no requests. Before that wait, the permission prompt printed before its tool call in 10 of 60 runs of `client-allow`, 6 at a time. A single run rarely shows a regression, so run the step many times in parallel after a change to `receive`. `verify-notification-barrier` checks the order as `InboundHandlerAfterNotes`.
- At end of input, the client answers the permission request with the `cancelled` outcome. The example agent then ends the turn without more chunks, so `✅ Agent completed` follows `Choose an option:`. Input that ends after a wrong key also cancels. Run the client without keys only through `"$V" client`, which stops it at 1 MiB of output if the loop comes back.
- `Choose an option:` has no newline, so the next printed text starts on the same line. Match text with `grep -F`, not by line position.
- `go run ./example/client` compiles the agent on its first run, so the first run can take much longer than 5.3 s.
- With no agent path, the client starts `go run` on `example/agent`, and at the end it kills only that `go run`. The agent binary runs from the Go build cache under the name `agent`, and it exits when it reads end of input. `client-default` finds it, and every other process that the client started, by a tag in their environment. Do not use `ps | grep example/agent`: it misses the agent binary, and it matches any shell whose command line contains that text, also the shell that runs the check.
