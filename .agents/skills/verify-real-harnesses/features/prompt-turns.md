# Prompt turns

A user sends a message and reads the streamed reply. The user can pick another model from the model menu and back, and can press stop while the agent writes.

## Sub-features

- `prompt` runs a turn that must end with `end_turn` and a reply that says `pong`.
- `config-option` switches the `model` option to another model, runs a turn on it, switches back and runs another.
- `config-boolean` turns a boolean option, such as Claude's `fast` or Codex's `fast-mode` (category `model_config`), on and off.
- `cancel` stops a long turn with `session/cancel`.

## How to get to it (user POV)

- Type a message in the agent panel: the editor sends `session/prompt` and renders each `session/update`.
- Pick a model in the model menu: the editor sends `session/set_config_option` with the option's `configId`.
- Flip a toggle such as fast mode: the editor sends `session/set_config_option` with `"type":"boolean"`. The agent offers toggles only to a client that advertises `session.configOptions.boolean`.
- Press stop during a reply: the editor sends `session/cancel`.

## Driving it with harness

Preconditions:

- `"$V" doctor` exits 0, and the adapter's credential variable is set.

Steps:

- **Run the turns.** Run `"$V" g5 claude`. The `prompt` line reads `stopReason end_turn, reply "pong", models [claude-opus-5-5]`, with the adapter's current default model.
- **Check the switch.** The `config-option` line names the models on the wire before the switch, after it and after the switch back, such as `before [claude-opus-5-5], after the switch [claude-fable-5-2 claude-opus-5-5], after the switch back [claude-opus-5-5]`.
- **Check the toggle.** The `config-boolean` line names the option and its values, such as `fast (category model_config) false -> true -> false`, and lists every option that the SDK decoded.
- **Check the stop.** The `cancel` line reads `stopReason cancelled` and says how long after the first streamed update the driver sent `session/cancel`.
- **Read the cost.** Run `jq '.costUsd' "$HARNESS_RUN/evidence/g5-claude/summary.json"`. The value is the sum of the highest `usage_update` cost of each session on each connection.

## Gotchas

- The model ids come from `_meta` on the wire: `_claude/model` in Claude's `usage_update`, and `quota.model_usage[].model` in both adapters' prompt responses. A turn can list two models, because Claude Code makes side calls on its default model. A turn that names no model id fails `config-option`, because nothing then proves the switch.
- The switch target is the first offered model that is not current, not an alias of the current one and not a lesser model. When an adapter reorders or renames its options, check the `config-option` detail for the model that was picked.
- Claude can think for 20 s before its first chunk. The cancel waits up to 90 s for the first streamed update, and sends `session/cancel` then even if none came.
- The essay prompt asks for no tools. If an agent asks for a permission during the cancel turn, the driver answers `cancelled`.
