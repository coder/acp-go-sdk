# Errors and extensions

An editor that sends something the agent cannot serve gets a JSON-RPC error, and the connection keeps working. Methods whose names start with `_` are ACP extension methods: an unknown extension request gets "Method not found", and an unknown extension notification is ignored without a log line. A malformed input line is logged and skipped. When the editor closes the agent's input, the agent exits 0.

## Sub-features

- `err-unknown-method` answers an unknown request with `-32601`.
- `err-ext-request` answers an `_` request without a handler with `-32601`.
- `err-ext-notify` ignores an `_` notification and logs nothing about it.
- `err-unknown-notify` logs `failed to handle notification` for an unknown notification without `_`.
- `err-invalid-params` answers `session/new` without `cwd` with `-32602` and `cwd is required`.
- `err-malformed` logs `failed to parse incoming message` for a line that is not JSON, and keeps serving.
- `err-eof` exits 0 when its input closes.

## How to get to it (user POV)

- An editor sends a method that the agent does not implement, such as a newer ACP method or a vendor extension like `_example.com/hello`.
- An editor sends incomplete params or a broken line.
- The editor quits, which closes the agent's stdin.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.

Steps:

- **Play all error cases.** Run `"$V" wire errors.jsonl errors`. Each step prints PASS. `unknown method` and `extension request without a handler` print `error -32601 Method not found`. `invalid params` prints `error -32602 Invalid params`. `connection still works` prints no error, and `close stdin` prints `agent exit code 0`.
- **Agent log.** Run `cat "$ACP_VERIFY_RUN/evidence/errors/agent.stderr.log"`. It has exactly two `ERROR` lines: `failed to handle notification method=foo/notify` and `failed to parse incoming message ... raw="{not json"`. No line names `_example.com/progress`.
- **Only requests got replies.** Run `jq -r 'select(.dir=="agent->client") | .msg.error.code // "result"' "$ACP_VERIFY_RUN/evidence/errors/transcript.jsonl" | paste -sd' '`. The output is `result -32601 -32601 -32602 result`: one reply for each of the five requests, and none for the notifications or the broken line.

## Gotchas

- The example agent has no `HandleExtensionMethod`, so no example shows a successful extension call. The SDK tests in `acp_test.go` cover that path.
- `stderrLacks` checks the log once, at that moment. Keep the `sleep` before it, or the check can run before the agent has read the notification.
- The log format comes from `slog.Default()` in the example agent. Match the message text, not the timestamp.
