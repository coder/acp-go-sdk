# Example client CLI

A developer runs the example client in a terminal. It starts the example agent, connects, opens a session, prints the streamed turn, asks the developer for the database host of an edit, asks to allow or skip the edit, asks to open a review page after an allowed edit, and prints `✅ Agent completed` after the last update of the turn.

## Sub-features

- `cli-connect` prints `✅ Connected to agent (protocol v1)` and `📝 Created session: sess_...`.
- `cli-config` prints the session's config options after `📝 Created session:`, see [config-options.md](./config-options.md).
- `cli-stream` prints each chunk and each tool call with its status.
- `cli-host` asks `Database host [new-host]:` before the permission prompt. Enter takes `new-host`, and a typed host goes into the edit, which the permission prompt prints as JSON.
- `cli-allow` applies the edit after `1`, and prints ` Perfect! I've successfully updated the configuration. The changes have been applied.`
- `cli-review` asks `Open this URL? [y/N]:` after an allowed edit. `y` prints `[Client] Opening https://example.com/acp-demo/review` and then `✅ Elicitation elic_... complete`. Any other answer declines, and no completion follows.
- `cli-reject` skips the edit after `2`, and prints ` I understand you prefer not to make that change. I'll skip the configuration update.`
- `cli-invalid` prints `Invalid option. Please try again.` after an unknown choice and asks again.
- `cli-done` prints `✅ Agent completed` after the last chunk, and exits 0.
- `cli-default-agent` starts the agent with `go run` when no agent path is given.
- `cli-eof` cancels the host question and answers `cancelled` at the permission prompt when its input ends, and exits 0.

## How to get to it (user POV)

- Run `go run ./example/client` from the repository root (the README entry point). It runs `go run` on `example/agent`.
- Run a built client with an agent command: `acp-example-client <agent binary> [args...]`.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.
- Each run takes about 5.3 s.
- The client reads one line per prompt, in this order: the host (Enter for `new-host`), the permission choice, and after an allow the review answer.

Steps:

- **Allow.** Press Enter at the host, type `1` at the permission prompt and `y` at the review. Run `"$V" client client-allow '\n1\ny\n'`. Exit 0. `stdout.txt` has, in this order: `✅ Connected to agent (protocol v1)`, `📝 Created session: sess_`, `🔧 Reading project files (pending)`, `📝 Which database host should the configuration use?`, `Database host [new-host]:`, `🔐 Permission requested: Modifying critical configuration file`, `Choose an option:`, ` Perfect! I've successfully updated the configuration. The changes have been applied.`, `🔗 Review the configuration change in your browser.`, `Open this URL? [y/N]:`, `[Client] Opening https://example.com/acp-demo/review`, `✅ Elicitation elic_`, `✅ Agent completed`.
- **Typed host.** Run `"$V" client client-host 'db.internal\n1\nn\n'`. Exit 0. The line after `🔐 Permission requested:` is `   {"content":"{\"database\": {\"host\": \"db.internal\"}}","path":"/project/config.json"}`, and `stdout.txt` has no `✅ Elicitation` line.
- **Tool call status.** Run `grep 'Tool call' "$ACP_VERIFY_RUN/evidence/client-allow/stdout.txt"`. Expected: `` 🔧 Tool call `call_1` updated: completed ``, then `` 🔧 Tool call `call_2` updated: completed ``. A pointer address such as `0x29c44f7ab480` instead of `completed` is a FAIL. See Gotchas.
- **Reject after a wrong key.** Press Enter at the host, then type `9`, then `2`. Run `"$V" client client-reject '\n9\n2\n'`. Exit 0. `stdout.txt` has `Invalid option. Please try again.`, then ` I understand you prefer not to make that change. I'll skip the configuration update.`, then `✅ Agent completed`.
- **Done after the last chunk.** Run `tail -n 3 "$ACP_VERIFY_RUN/evidence/client-allow/stdout.txt"`. The last line is `✅ Agent completed`, and no chunk text comes after it.
- **Tool call before its permission prompt.** Run `grep -n 'Modifying critical configuration file' "$ACP_VERIFY_RUN/evidence/client-allow/stdout.txt"`. Expected: the `🔧 ... (pending)` line comes before the `🔐 Permission requested` line in every run. A wrong order in any run is a defect. See Gotchas.
- **Default agent.** Run `"$V" client-default client-default '\n1\ny\n'`. Exit 0. `stdout.txt` ends with `✅ Agent completed`, and `result.txt` has `leftover=none`. A `LEFTOVER:` line (exit 1) lists each process that the client started and that still runs 3 s after it exited. See Gotchas.
- **End of input.** Give no keys. Run `ACP_VERIFY_TIMEOUT=15 "$V" client client-eof ''`. Exit 0 in about 4.3 s. `stdout.txt` has `Database host [new-host]:` once, `Choose an option:` once, then `✅ Agent completed`. It has neither the allow chunk ` Perfect! I've successfully updated the configuration.` nor the reject chunk ` I understand you prefer not to make that change.` An earlier chunk, ` Now I understand the project structure.`, is in every run, so match the full chunk text. Exit 141 and `OUTPUT CAP` is a FAIL. See Gotchas.

## Gotchas

- The tool call status in a `ToolCallUpdate` is a `*ToolCallStatus`. The client prints the value it points to, and an empty string when the agent sends no status. An address in the line means the pointer is printed again.
- The SDK starts the handler of an incoming request such as `session/request_permission` only after the handlers of the `session/update` notifications that arrived before it, unless the handler that runs waits on the agent's answer to its own request. The example client's handlers make no requests. Before that wait, the permission prompt printed before its tool call in 10 of 60 runs of `client-allow`, 6 at a time. A single run rarely shows a regression, so run the step many times in parallel after a change to `receive`. `verify-notification-barrier` checks the order as `InboundHandlerAfterNotes`.
- At end of input, the client cancels the host question, and the agent keeps `new-host`. It then answers the permission request with the `cancelled` outcome. The example agent then ends the turn without more chunks, so `✅ Agent completed` follows `Choose an option:`. Input that ends after a wrong key also cancels. Run the client without keys only through `"$V" client`, which stops it at 1 MiB of output if the loop comes back.
- `Choose an option:`, `Database host [new-host]:` and `Open this URL? [y/N]:` have no newline, so the next printed text starts on the same line. Match text with `grep -F`, not by line position.
- `go run ./example/client` compiles the agent on its first run, so the first run can take much longer than 5.3 s.
- With no agent path, the client starts `go run` on `example/agent`, and at the end it kills only that `go run`. The agent binary runs from the Go build cache under the name `agent`, and it exits when it reads end of input. `client-default` finds it, and every other process that the client started, by a tag in their environment. Do not use `ps | grep example/agent`: it misses the agent binary, and it matches any shell whose command line contains that text, also the shell that runs the check.
