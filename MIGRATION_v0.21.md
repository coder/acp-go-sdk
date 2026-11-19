# Migrating to v0.21.0

v0.21.0 makes inbound decoding stricter: the SDK rejects a message that the ACP schema forbids and that the Rust SDK rejects too. The ACP schema stays at 1.32.0, and `acp.Agent` and `acp.Client` do not change, so your code compiles as before.

```bash
go get github.com/coder/acp-go-sdk@v0.21.0
go build ./...
```

**What reaches your handlers changes.** A request that breaks a rule below gets `-32602` (invalid params) and never reaches your method. A notification that breaks one is dropped, and the connection logs it at Error. Check that the peers you talk to send the members that the table below names.

| Change | Section |
| --- | --- |
| Required members are checked at every depth | [Required members](#required-members) |

## Required members

The connection calls `Validate` on every request and notification it reads. In v0.20.0 `Validate` checked only the required members that the message's own schema types as a plain string or an array, and nothing below the message. So a `session/prompt` without `sessionId` reached `Agent.Prompt` with `SessionId == ""` (#82), and an `acp` MCP server without `serverId` passed `session/new` (#125). `Validate` now checks required members at every depth, as the Rust SDK does:

- A required member whose type is a named string type of the schema, such as `SessionId`, `ToolCallId` or the enum `PermissionOptionKind`, must be present and not `""`.
- A required array must be present. `null` counts as missing, except where the schema reads `null` as `[]`, as for `mcpServers`, `headers`, `args` and `env`.
- A required object or union must be present, and is checked in turn. So are each item of an array, each member of a map, and an optional value that is set.
- A required plain string, such as `cwd` or `path`, must not be `""` in the message itself, as in v0.20.0. Below the message a plain string may be `""`, such as the `text` of a text content block or the `value` of an environment variable.
- Inside a lenient member (`x-deserialize-default-on-error` or `x-deserialize-skip-invalid-items` in the schema), such as the `configOptions` of a `config_option_update`, the SDK reads a bad value as the default or drops the item, as the Rust SDK does, and checks nothing more. A required lenient array must still be present.

**An empty ID is rejected.** The Rust SDK accepts `""` for a required ID, such as `"sessionId": ""`. The Go SDK rejects it, because a Go string holds `""` both for a missing member and for an empty one.

**The error names the path of the member.** The `data.error` of the `-32602` error reads, for example, `mcpServers[0]: serverId is required`, `toolCall: toolCallId is required`, or `update: content: ContentBlock must have exactly one variant set`.

### Newly rejected inputs

Each row is a member that v0.20.0 accepted when it was missing, `null` or `""`, and that v0.21.0 rejects. In a member path, `[]` is each item of an array and `{}` each member of a map. The variant column names the variant that holds the member: `key: value` is the discriminator that selects it, and a bare name, such as `valueId`, `session` or `other`, is a variant that has no discriminator.

| Read by | Methods | Member | Rejected when | Variant |
| --- | --- | --- | --- | --- |
| `AgentSideConnection` | `authenticate` | `methodId` | missing or `""` | |
| `AgentSideConnection` | `mcp/message` | `requestId` | missing or `""` | |
| `AgentSideConnection` | `mcp/message` | `serverId` | missing or `""` | |
| `AgentSideConnection` | `providers/disable`, `providers/set` | `providerId` | missing or `""` | |
| `AgentSideConnection` | `providers/set` | `apiType` | missing or `""` | |
| `AgentSideConnection` | `session/archive`, `session/cancel`, `session/close`, `session/delete`, `session/fork`, `session/load`, `session/prompt`, `session/resume`, `session/set_mode`, `session/set_title`, `session/status`, `session/unarchive` | `sessionId` | missing or `""` | |
| `AgentSideConnection` | `session/fork`, `session/load`, `session/new`, `session/resume` | `configOptions[].configId` | missing or `""` | `type: boolean` |
| `AgentSideConnection` | `session/fork`, `session/load`, `session/new`, `session/resume` | `configOptions[].configId` | missing or `""` | `valueId` |
| `AgentSideConnection` | `session/fork`, `session/load`, `session/new`, `session/resume` | `configOptions[].value` | `""` | `valueId` |
| `AgentSideConnection` | `session/fork`, `session/load`, `session/new`, `session/resume` | `mcpServers[].headers` | missing | `type: http` |
| `AgentSideConnection` | `session/fork`, `session/load`, `session/new`, `session/resume` | `mcpServers[].headers` | missing | `type: sse` |
| `AgentSideConnection` | `session/fork`, `session/load`, `session/new`, `session/resume` | `mcpServers[].serverId` | missing or `""` | `type: acp` |
| `AgentSideConnection` | `session/fork`, `session/load`, `session/new`, `session/resume` | `systemPrompt.content` | missing or `null` | |
| `AgentSideConnection` | `session/fork`, `session/load`, `session/new`, `session/resume` | `systemPrompt.content[].resource` | missing | `type: resource` |
| `AgentSideConnection` | `session/fork`, `session/load`, `session/new`, `session/resume` | `systemPrompt.mode` | missing or `""` | |
| `AgentSideConnection` | `session/prompt` | `prompt[].resource` | missing | `type: resource` |
| `AgentSideConnection` | `session/set_config_option` | `configId` | missing or `""` | `type: boolean` |
| `AgentSideConnection` | `session/set_config_option` | `configId` | missing or `""` | `valueId` |
| `AgentSideConnection` | `session/set_config_option` | `sessionId` | missing or `""` | `type: boolean` |
| `AgentSideConnection` | `session/set_config_option` | `sessionId` | missing or `""` | `valueId` |
| `AgentSideConnection` | `session/set_config_option` | `value` | `""` | `valueId` |
| `AgentSideConnection` | `session/set_mode` | `modeId` | missing or `""` | |
| `ClientSideConnection` | `auth/status_update` | `state` | missing or `""` | |
| `ClientSideConnection` | `elicitation/complete` | `elicitationId` | missing or `""` | |
| `ClientSideConnection` | `elicitation/create` | `elicitationId` | missing or `""` | `mode: url` |
| `ClientSideConnection` | `elicitation/create` | `requestedSchema.properties{}.items` | missing | `mode: form`, `type: array` |
| `ClientSideConnection` | `elicitation/create` | `requestedSchema.properties{}.items.enum` | missing or `null` | `mode: form`, `type: array`, `type: string` |
| `ClientSideConnection` | `elicitation/create` | `sessionId` | `""` | `mode: form`, `session` |
| `ClientSideConnection` | `elicitation/create` | `sessionId` | `""` | `mode: url`, `session` |
| `ClientSideConnection` | `elicitation/create` | `sessionId` | `""` | `other`, `session` |
| `ClientSideConnection` | `fs/read_text_file`, `fs/write_text_file`, `session/request_permission`, `session/update`, `terminal/create`, `terminal/kill`, `terminal/output`, `terminal/release`, `terminal/wait_for_exit` | `sessionId` | missing or `""` | |
| `ClientSideConnection` | `mcp/message` | `requestId` | missing or `""` | |
| `ClientSideConnection` | `mcp/message` | `serverId` | missing or `""` | |
| `ClientSideConnection` | `session/request_permission` | `options[].kind` | missing or `""` | |
| `ClientSideConnection` | `session/request_permission` | `options[].optionId` | missing or `""` | |
| `ClientSideConnection` | `session/request_permission` | `toolCall.toolCallId` | missing or `""` | |
| `ClientSideConnection` | `session/update` | `update` | missing | |
| `ClientSideConnection` | `session/update` | `update.availableCommands` | missing | `sessionUpdate: available_commands_update` |
| `ClientSideConnection` | `session/update` | `update.compactionId` | missing or `""` | `sessionUpdate: compaction_summary_chunk` |
| `ClientSideConnection` | `session/update` | `update.compactionId` | missing or `""` | `sessionUpdate: compaction_update` |
| `ClientSideConnection` | `session/update` | `update.configOptions` | missing | `sessionUpdate: config_option_update` |
| `ClientSideConnection` | `session/update` | `update.content` | missing | `sessionUpdate: agent_message_chunk` |
| `ClientSideConnection` | `session/update` | `update.content` | missing | `sessionUpdate: agent_thought_chunk` |
| `ClientSideConnection` | `session/update` | `update.content` | missing | `sessionUpdate: compaction_summary_chunk` |
| `ClientSideConnection` | `session/update` | `update.content` | missing | `sessionUpdate: session_message_chunk` |
| `ClientSideConnection` | `session/update` | `update.content` | missing | `sessionUpdate: user_message_chunk` |
| `ClientSideConnection` | `session/update` | `update.content.resource` | missing | `sessionUpdate: agent_message_chunk`, `type: resource` |
| `ClientSideConnection` | `session/update` | `update.content.resource` | missing | `sessionUpdate: agent_thought_chunk`, `type: resource` |
| `ClientSideConnection` | `session/update` | `update.content.resource` | missing | `sessionUpdate: compaction_summary_chunk`, `type: resource` |
| `ClientSideConnection` | `session/update` | `update.content.resource` | missing | `sessionUpdate: session_message_chunk`, `type: resource` |
| `ClientSideConnection` | `session/update` | `update.content.resource` | missing | `sessionUpdate: user_message_chunk`, `type: resource` |
| `ClientSideConnection` | `session/update` | `update.currentModeId` | missing or `""` | `sessionUpdate: current_mode_update` |
| `ClientSideConnection` | `session/update` | `update.entries` | missing | `sessionUpdate: plan` |
| `ClientSideConnection` | `session/update` | `update.messageId` | missing or `""` | `sessionUpdate: session_message` |
| `ClientSideConnection` | `session/update` | `update.messageId` | missing or `""` | `sessionUpdate: session_message_chunk` |
| `ClientSideConnection` | `session/update` | `update.plan` | missing | `sessionUpdate: plan_update` |
| `ClientSideConnection` | `session/update` | `update.plan.entries` | missing | `sessionUpdate: plan_update`, `type: items` |
| `ClientSideConnection` | `session/update` | `update.plan.planId` | missing or `""` | `sessionUpdate: plan_update`, `type: items` |
| `ClientSideConnection` | `session/update` | `update.plan.planId` | missing or `""` | `sessionUpdate: plan_update`, `type: markdown` |
| `ClientSideConnection` | `session/update` | `update.planId` | missing or `""` | `sessionUpdate: plan_removed` |
| `ClientSideConnection` | `session/update` | `update.sessionId` | missing or `""` | `sessionUpdate: subagent_update` |
| `ClientSideConnection` | `session/update` | `update.severity` | missing or `""` | `sessionUpdate: notice` |
| `ClientSideConnection` | `session/update` | `update.status` | missing or `""` | `sessionUpdate: compaction_update` |
| `ClientSideConnection` | `session/update` | `update.suggestions` | missing or `null` | `sessionUpdate: prompt_suggestions_update` |
| `ClientSideConnection` | `session/update` | `update.toolCallId` | missing or `""` | `sessionUpdate: tool_call` |
| `ClientSideConnection` | `session/update` | `update.toolCallId` | missing or `""` | `sessionUpdate: tool_call_update` |
| `ClientSideConnection` | `terminal/kill`, `terminal/output`, `terminal/release`, `terminal/wait_for_exit` | `terminalId` | missing or `""` | |

The rows come from the generated `Validate` methods of v0.20.0 and v0.21.0, followed from each method that the connection reads. A missing member that already keeps the decoder from choosing a variant is not listed: v0.20.0 rejected it too.

### Validate on values you build

- Every union, and each struct that has one of the checks above, now has a `Validate` method: 112 types more than in v0.20.0, such as `ContentChunk`, `McpServerAcpInline` and `PermissionOption`. A `oneOf` union's `Validate` requires exactly one variant set, and an `anyOf` union's one at least (`McpServer has no variant set`), so `Validate` on a zero union fails.
- `SessionConfigOption.Validate` no longer rejects an empty `name`. The SDK never calls it on its own: the requests and notifications that it reads hold a `SessionConfigOption` only in a lenient member.
- The `Validate` of a response type checks the same rules. The SDK does not call it on a response that it receives.
