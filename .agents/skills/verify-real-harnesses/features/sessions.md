# Sessions

An editor connects to an agent, signs in when the agent asks for it, and opens a chat. Later it lists past chats, closes or deletes one, and reopens one after a restart, either with the history replayed (`session/load`) or without (`session/resume`).

## Sub-features

- `initialize` negotiates protocol version 1 and reads the agent's capabilities and auth methods.
- `authenticate` signs in with the method that the adapter table names.
- `session/new` opens a session in an empty cwd.
- `system-prompt` opens session P with a client system prompt in `append` mode, and checks that a turn follows it.
- `session/list` finds that session by its cwd.
- `session/close` closes it.
- `session/delete` deletes another session and checks that the list drops it.
- `session/load` reopens the session in a new agent process and replays it.
- `session/resume` reopens the session in a new agent process and continues it.

## How to get to it (user POV)

- Open the agent panel: the editor sends `initialize`, then `authenticate` if the agent needs it, then `session/new`.
- Set custom instructions for a chat: the editor sends them as `systemPrompt` in `session/new`, when the agent advertises `sessionCapabilities.systemPrompt`.
- Open the chat history: the editor sends `session/list` for the project folder.
- Close or delete a chat in the history.
- Restart the editor and open an old chat: `session/load` if the editor shows the history, `session/resume` if it does not.

## Driving it with harness

Preconditions:

- `"$V" doctor` exits 0 for the exported `HARNESS_RUN`.

Steps:

- **Run the lifecycle.** Run `"$V" g5 claude`. The lines for `initialize`, `session/new`, `system-prompt`, `session/list`, `session/close`, `session/delete`, `session/load` and `session/resume` print PASS. `authenticate` prints SKIP, because the driver gives the Claude adapter no `-auth`. The driver advertises `auth.terminal`, so the Claude adapter lists two terminal auth methods, `claude-login(terminal)` and `console-login(terminal)`, on the `initialize` line. Without that capability it lists none. `initialize` fails with `decode mismatch in authMethods` when the auth methods that the SDK decoded, encoded again, differ from the wire.
- **Sign in.** Run `"$V" g5 codex`. `authenticate` prints `PASS  authenticate  methodId api-key`.
- **See what was reopened.** Run `jq -c 'select(.conn==2 and .msg.params.update.sessionUpdate) | .msg.params.update.sessionUpdate' "$HARNESS_RUN/evidence/g5-claude/transcript.jsonl"`. The output starts with `user_message_chunk` and `agent_message_chunk` lines, the replay of the earlier turns. The `session/load` predicate reads only the agent chunks for `pong`, because the user's prompt also contains the word.
- **See what the SDK could not decode or route.** Run `jq -r 'select(.level=="ERROR") | [.scenario, .msg, .method // ""] | @tsv' "$HARNESS_RUN/evidence/g5-claude/sdk.log"`. At schema 0.13.5 this prints three `auth/status_update` lines, in `session/new`, `session/load` and `session/resume`.

## Gotchas

- The `initialize` line shows only the capabilities that the SDK decodes. An agent can advertise more, such as `sessionCapabilities.status` from a newer schema. Read the raw response in `transcript.jsonl` to see them. The session scenarios decide from that raw response, so a capability that the SDK loses is a `decode mismatch` failure, not a skip.
- Codex needs no `authenticate` when `config.toml` names a model provider, but the adapter table sends it anyway, so the gate covers the method.
- `session/delete` deletes session B after `session/close`, while `session/close` closes session A. Session A must stay on disk for `session/load` and `session/resume`.
- codex-acp compares the `session/list` cwd filter to the real path of each session's cwd. The driver resolves symlinks in `-cwd`. A cwd that goes through a symlink lists nothing.
