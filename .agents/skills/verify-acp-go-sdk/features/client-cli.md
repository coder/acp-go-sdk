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
- `cli-eof` stops when its input ends at the permission prompt.

## How to get to it (user POV)

- Run `go run ./example/client` from the repository root (the README entry point). It runs `go run` on `example/agent`.
- Run a built client with an agent command: `acp-example-client <agent binary> [args...]`.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.
- Each run takes about 5.3 s.

Steps:

- **Allow.** Type `1` at the prompt. Run `"$V" client client-allow '1\n'`. Exit 0. `stdout.txt` has, in this order: `✅ Connected to agent (protocol v1)`, `📝 Created session: sess_`, `🔧 Reading project files (pending)`, `🔐 Permission requested: Modifying critical configuration file`, `Choose an option:`, ` Perfect! I've successfully updated the configuration. The changes have been applied.`, `✅ Agent completed`.
- **Tool call status.** Run `grep 'Tool call' "$ACP_VERIFY_RUN/evidence/client-allow/stdout.txt"`. Expected: `` 🔧 Tool call `call_1` updated: completed ``. Current result: FAIL, the status shows as a pointer address such as `0x29c44f7ab480`. See Gotchas.
- **Reject after a wrong key.** Type `9`, then `2`. Run `"$V" client client-reject '9\n2\n'`. Exit 0. `stdout.txt` has `Invalid option. Please try again.`, then ` I understand you prefer not to make that change. I'll skip the configuration update.`, then `✅ Agent completed`.
- **Done after the last chunk.** Run `tail -n 3 "$ACP_VERIFY_RUN/evidence/client-allow/stdout.txt"`. The last line is `✅ Agent completed`, and no chunk text comes after it.
- **Tool call before its permission prompt.** Run `grep -n 'Modifying critical configuration file' "$ACP_VERIFY_RUN/evidence/client-allow/stdout.txt"`. Expected: the `🔧 ... (pending)` line comes before the `🔐 Permission requested` line. This holds in most runs and fails in some. See Gotchas.
- **Default agent.** Run `printf '1\n' | timeout 120 go run ./example/client > "$ACP_VERIFY_RUN/evidence/client-default.txt" 2>&1; echo "exit=$?"`. Exit 0, and the file ends with `✅ Agent completed`. Then run `ps -eo pid,cmd | grep '[e]xample/agent'` and confirm that it prints nothing.
- **End of input.** Give no keys. Run `ACP_VERIFY_TIMEOUT=15 "$V" client client-eof ''`. Expected: the client stops. Current result: FAIL, exit 141 and `OUTPUT CAP`, because `stdout.txt` fills with `Choose an option:`. See Gotchas.

## Gotchas

- The tool call status is a `*ToolCallStatus`, and the client prints it with `%v`, so the line shows an address. Assert the status in a wire transcript instead (prompt-turn.md), and report the CLI line as a defect.
- The SDK handles an incoming request such as `session/request_permission` in its own goroutine, while `session/update` notifications wait in an ordered queue. So the permission prompt can print before the tool call that it asks about. A run of `client-reject` showed `Choose an option:` before `🔧 Modifying critical configuration file (pending)`. Report each run where the order is wrong.
- At end of input, `ReadString` returns an empty string forever, and the client loops on `Choose an option:`. Never run the client without keys on stdin outside `"$V" client`, which stops it at 1 MiB of output.
- `Choose an option:` has no newline, so the next printed text starts on the same line. Match text with `grep -F`, not by line position.
- `go run ./example/client` compiles the agent on its first run, so the first run can take much longer than 5.3 s.
