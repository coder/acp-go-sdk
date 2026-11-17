# Permissions

The agent wants to create a file and asks the user first. The user allows it, and the file appears, or rejects it, and no file appears.

## Sub-features

- `permission-allow` answers every request with an `allow_once` option, or `allow_always` when no `allow_once` is offered.
- `permission-reject` answers every request with a `reject_once` option, or `reject_always`.

## How to get to it (user POV)

- Put the session in a mode that asks before edits, then ask the agent to create a file. The editor shows the permission prompt from `session/request_permission`, and the user picks an option.

## Driving it with harness

Preconditions:

- `"$V" doctor` exits 0, and the adapter's credential variable is set.
- The adapter table names the mode that asks before edits: `default` for claude, `read-only` for codex.

Steps:

- **Allow and reject.** Run `"$V" g5 claude`. The `permission-allow` line reads `1 permission request(s), answers [allow_once:...]; allow-check.txt exists=true`. The `permission-reject` line reads `answers [reject_once:...]; reject-check.txt exists=false`.
- **See each request and answer.** Run `jq -c '.permissions[]' "$HARNESS_RUN/evidence/g5-claude/summary.json"`. Each line has the scenario, the tool title, the options offered and the option picked.
- **See the tool call end.** The scenario detail lists `tool statuses`. An allowed edit ends `completed`. A rejected edit ends `failed`, or stays `in_progress` when the agent ends the turn.

## Gotchas

- A new Claude session starts in mode `auto`, which edits without asking. Without `-ask-mode default`, both scenarios fail with "no session/request_permission arrived".
- Codex's reject option is `cancel` with kind `reject_once`. It ends the turn with `stopReason cancelled`.
- The session cwd lives under `work/cwd/`, and `cleanup` deletes it. The file check happens during the run, and the scenario detail records it.
- An agent that writes the file without a permission request fails `permission-allow`, even though the file exists. The scenario proves the round trip, not only the file.
