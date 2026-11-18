# Config options

A user picks the model of a chat, and turns a model setting on or off, from the editor's session toolbar. The agent offers its settings as config options in the `session/new` result, and the editor changes one with `session/set_config_option`. The example agent offers a `model` select option (category `model`) and a `web_search` boolean option (category `model_config`). It offers the boolean only to a client that sets `clientCapabilities.session.configOptions.boolean` in `initialize`. Boolean options and the `model_config` category are stable from schema 1.18.0 and 1.16.0.

## Sub-features

- `config-new` lists `model` at `demo-fast` and `web_search` at `false` in the `session/new` result, each with its category.
- `config-select` sets `model` to `demo-deep` and back, and each result lists every option with its new value.
- `config-boolean` sets `web_search` to `true` with `"type":"boolean"`, and the model keeps its value.
- `config-invalid` answers an unknown model, and a boolean value sent without `"type":"boolean"`, with `-32602`.
- `config-no-boolean` offers only `model` to a client without the boolean capability, and answers a boolean set with `-32602`.
- `config-cli` prints each option of the new session in the example client: `⚙️  Model: demo-fast` and `⚙️  Web search: false`.

## How to get to it (user POV)

- Open a new chat: the editor shows the options of the `session/new` result.
- Pick another model, or flip a toggle: the editor sends `session/set_config_option` and redraws the options from the result.
- Run `go run ./example/client`: it advertises boolean options and prints the options after `📝 Created session:`.

## Driving it with acp-verify

Preconditions:

- `"$V" doctor` exits 0 for the exported `ACP_VERIFY_RUN`.

Steps:

- **Set options.** Run `"$V" wire config-options.jsonl config-options`. Each step prints PASS. `unknown model` and `boolean without its type` print `error -32602 Invalid params`, and `close stdin` prints `agent exit code 0`.
- **No boolean capability.** Run `"$V" wire config-options-no-boolean.jsonl config-options-no-boolean`. Each step prints PASS, and `boolean refused` prints `error -32602 Invalid params`.
- **CLI.** After the `client-allow` drive of [client-cli.md](./client-cli.md), run `grep -F '⚙️' "$ACP_VERIFY_RUN/evidence/client-allow/stdout.txt"`. The output is the two lines of `config-cli`, in that order.
- **Agent log.** Run `cat "$ACP_VERIFY_RUN/evidence/config-options/agent.stderr.log"`. It is empty.

## Gotchas

- The `expect` values compare whole JSON values, so `result.configOptions` must list every option with every field, in the agent's order.
- A `session/set_config_option` request without `"type":"boolean"` is a select value. A JSON boolean in `value` then fails to decode, and the SDK answers `-32602` before the agent's handler runs.
- The example agent keeps the options in memory, and the turn does not read them.
