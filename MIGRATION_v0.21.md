# Migrating to v0.21.0

v0.21.0 makes inbound decoding stricter: the SDK rejects a message that the ACP schema forbids and that the Rust SDK rejects too, apart from the few differences in bold below. The ACP schema stays at 1.32.0, and `acp.Agent` and `acp.Client` do not change, so your code compiles as before.

```bash
go get github.com/coder/acp-go-sdk@v0.21.0
go build ./...
```

**What reaches your handlers changes.** A request that breaks a rule below gets `-32602` (invalid params) and never reaches your method. A notification that breaks one is dropped, and the connection logs it at Error. Check that the peers you talk to send the members that the table below names.

| Change | Section |
| --- | --- |
| Required members are checked at every depth | [Required members](#required-members) |
| A `null` item of a string array, or a `null` value of a string map, fails to decode | [Null items and map values](#null-items-and-map-values) |
| A required member that is missing, or `null` where the schema does not allow it, fails to decode | [Missing and null required members](#missing-and-null-required-members) |
| An integer out of the range of its schema fails `Validate`, or reads as unset where the schema tolerates a bad value | [Integer ranges](#integer-ranges) |

## Required members

The connection calls `Validate` on every request and notification it reads. In v0.20.0 `Validate` checked only the required members that the message's own schema types as a plain string or an array, and nothing below the message. So a `session/prompt` without `sessionId` reached `Agent.Prompt` with `SessionId == ""` (#82), and an `acp` MCP server without `serverId` passed `session/new` (#125). `Validate` now checks required members at every depth, as the Rust SDK does:

- A required member whose type is a named string type of the schema, such as `SessionId`, `ToolCallId` or the enum `PermissionOptionKind`, must be present and not `""`.
- A required array must be present. `null` counts as missing, except where the schema reads `null` as `[]`, as for `mcpServers`, `headers`, `args` and `env`.
- A required object or union must be present, and is checked in turn. So are each item of an array, each member of a map, and an optional value that is set.
- A required plain string, such as `cwd` or `path`, must not be `""` in the message itself, as in v0.20.0. Below the message a plain string may be `""`, such as the `text` of a text content block or the `value` of an environment variable, but it must be there ([Missing and null required members](#missing-and-null-required-members)).
- Inside a lenient member (`x-deserialize-default-on-error` or `x-deserialize-skip-invalid-items` in the schema), such as the `configOptions` of a `config_option_update`, the SDK reads a bad value as the default or drops the item, as the Rust SDK does, and checks nothing more. A required lenient array must still be present.

**An empty ID is rejected.** The Rust SDK accepts `""` for a required ID, such as `"sessionId": ""`. The Go SDK rejects it, because a Go string holds `""` both for a missing member and for an empty one.

**The error names the path of the member.** For an empty ID the `data.error` of the `-32602` error reads, for example, `mcpServers[0]: serverId is required` or `toolCall: toolCallId is required`. A member that is missing fails before `Validate` runs, when the message decodes ([Missing and null required members](#missing-and-null-required-members)).

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
- `json.Marshal` of a zero union, such as `acp.ContentBlock{}`, fails with `ContentBlock has no variant set`. An `anyOf` union's `Validate` gives the same text. A `oneOf` union's `Validate` gives `ContentBlock must have exactly one variant set`, because it also rejects two variants set. v0.20.0 failed the marshal too, with encoding/json's `unexpected end of JSON input`. A send method, such as `SessionUpdate` with no update set, returns it inside its `-32602` error, and nothing is sent. A handler whose result holds a zero union, such as a `RequestPermission` that returns `RequestPermissionResponse{}`, makes the connection reply `-32603` with this text in `data.error`, so the peer reads `RequestPermissionOutcome has no variant set` where v0.20.0 sent `unexpected end of JSON input`.
- `SessionConfigOption.Validate` no longer rejects an empty `name`. The SDK never calls it on its own: the requests and notifications that it reads hold a `SessionConfigOption` only in a lenient member.
- The `Validate` of a response type checks the same rules. The SDK does not call it on a response that it receives.

## Null items and map values

`encoding/json` reads a `null` item of a `[]string`, or a `null` value of a `map[string]string`, as `""`, so v0.20.0 read `"args": ["-v", null]` as `["-v", ""]`. The schema allows no `null` there, and the Rust SDK fails the message, so v0.21.0 fails the decode with a `*json.UnmarshalTypeError` that names the struct and the property:

```text
json: cannot unmarshal null into Go struct field NewSessionRequest.additionalDirectories of type string
```

A request that holds one gets `-32602` invalid params, and your handler is not called. A notification that holds one is dropped and logged. Inside a union, such as the stdio variant of `McpServer`, the error is the union's `invalid variant payload`.

These still decode as in v0.20.0:

- A whole `null`. `"args": null` reads as the default, which is `[]` for `args` of a stdio MCP server, and `"additionalDirectories": null` reads as absent.
- A property with `x-deserialize-skip-invalid-items` or `x-deserialize-default-on-error`, such as `additionalDirectories` of `SessionInfo`. A `null` item there is dropped, or the whole value reads as unset.

When such a lenient property holds a type from the table below, the item that holds the `null` is dropped, as the Rust SDK drops it. A terminal auth method with `"env": {"A": null}` is left out of `authMethods` of an `initialize` response, where v0.20.0 kept it with `env` `{"A": ""}`.

Every property that now rejects a `null` item or value:

| Type | Property | Where it arrives, and what a `null` item or value does there |
| --- | --- | --- |
| `AuthMethodTerminal` | `args`, `env` | no message carries it; it fails when you decode it yourself |
| `AuthMethodTerminalInline` | `args`, `env` | `initialize` response to the client, in `authMethods`: the `authMethods` item that holds it is dropped |
| `CreateTerminalRequest` | `args` | `terminal/create` request to the client: -32602, no handler call |
| `ElicitationContentValueStringArray` | the array itself, a variant of `ElicitationContentValue` | `elicitation/create` response to the agent, in `content` of an accepted answer: no variant matches, so the response fails to decode and the call returns an error |
| `ElicitationPropertySchemaString` | `enum` | `elicitation/create` request to the client, in `requestedSchema.properties`: -32602, no handler call |
| `ElicitationSchema` | `required` | `elicitation/create` request to the client, in `requestedSchema`: -32602, no handler call |
| `LoadSessionRequest` | `additionalDirectories` | `session/load` request to the agent: -32602, no handler call |
| `McpServerStdio` | `args` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `mcpServers`: -32602, no handler call |
| `MultiSelectItemsString` | `enum` | `elicitation/create` request to the client, in `requestedSchema.properties.items`: -32602, no handler call |
| `NewSessionRequest` | `additionalDirectories` | `session/new` request to the agent: -32602, no handler call |
| `ResumeSessionRequest` | `additionalDirectories` | `session/resume` request to the agent: -32602, no handler call |
| `SessionSubagentUpdate` | `additionalDirectories` | `session/update` notification to the client, in `update`: dropped, no handler call |
| `StringMultiSelectItems` | `enum` | no message carries it; it fails when you decode it yourself |
| `StringPropertySchema` | `enum` | no message carries it; it fails when you decode it yourself |
| `SubagentUpdate` | `additionalDirectories` | no message carries it; it fails when you decode it yourself |
| `UnstableForkSessionRequest` | `additionalDirectories` | `session/fork` request to the agent: -32602, no handler call |
| `UnstableSessionOptionsRequest` | `additionalDirectories` | `session/options` request to the agent: -32602, no handler call |
| `UnstableSetProviderRequest` | `headers` | `providers/set` request to the agent: -32602, no handler call |

## Missing and null required members

`encoding/json` leaves a member that the JSON object lacks, or that is `null`, at its Go zero value, and `Validate` cannot tell that from a present `""`, `0`, `false` or `{}`. So the checks above let a text block without `text`, an environment variable without `name`, or an `initialize` without `protocolVersion` through. v0.21.0 fails the decode instead, as the Rust SDK does: each required member of an object must be present, and may be `null` only where the schema allows `null` or reads it as the default, as it does for `mcpServers`, `headers`, `args` and `env`. The error is a `*json.UnmarshalTypeError` that names the struct and the property:

```text
json: cannot unmarshal missing member into Go struct field InitializeRequest.protocolVersion of type acp.ProtocolVersion
json: cannot unmarshal null into Go struct field NewSessionRequest.cwd of type string
```

A request that lacks one gets `-32602` invalid params, and your handler is not called. A notification that lacks one is dropped and logged. A response that lacks one makes your call, such as `ClientSideConnection.NewSession`, return a `-32603` error, where v0.20.0 returned the value with the member at its zero value.

**A missing required member fails to decode even inside a union variant.** The union then tries its next variant, as the Rust SDK does, and reports `invalid variant payload` when none fits: an `sse` MCP server without `url`, or a `tool_call` update without `title`, fails the whole message. An `http` entry that lacks `url` but holds the stdio members, such as `{"type":"http","name":"n","command":"c","args":[],"env":[]}`, reads as a stdio server, as in Rust.

These still decode as in v0.20.0:

- A present zero value, such as `"text": ""`, `"protocolVersion": 0`, `"value": false` or `"used": 0`. The checks above still reject an empty ID.
- `null` where the schema reads it as the default, such as `"mcpServers": null`, or where it allows `null`.
- A member inside a lenient property (`x-deserialize-default-on-error` or `x-deserialize-skip-invalid-items`), such as an item of `configOptions` in a `config_option_update`. v0.20.0 already dropped the item, or read the value as unset, when it lacked a required member.
- A key in another case, such as `"Title"`, which counts as present because `encoding/json` reads it into the field. The Rust SDK rejects it.

A JSON `null` where the schema wants an object with required members, such as an item of `env` or the `params` of `initialize`, has none of them, so it fails too.

Every member that the rules above let through when it was missing or `null`, and that v0.21.0 rejects. In a path, `[]` is each item of an array and `{}` each member of a map. A label in parentheses names the variant that the type is, and `of` the variants on the way to it:

| Type | Member: rejected when | Where it arrives |
| --- | --- | --- |
| `ContentBlockAudio` (`type: audio`) | `data`: missing or `null`; `mimeType`: missing or `null` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `systemPrompt.content[]`; `session/prompt` request to the agent, in `prompt[]`; `session/update` notification to the client, in `update.content` of `sessionUpdate: agent_message_chunk`, `sessionUpdate: agent_thought_chunk`, `sessionUpdate: compaction_summary_chunk`, `sessionUpdate: session_message_chunk`, `sessionUpdate: user_message_chunk` |
| `ContentBlockImage` (`type: image`) | `data`: missing or `null`; `mimeType`: missing or `null` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `systemPrompt.content[]`; `session/prompt` request to the agent, in `prompt[]`; `session/update` notification to the client, in `update.content` of `sessionUpdate: agent_message_chunk`, `sessionUpdate: agent_thought_chunk`, `sessionUpdate: compaction_summary_chunk`, `sessionUpdate: session_message_chunk`, `sessionUpdate: user_message_chunk` |
| `ContentBlockResourceLink` (`type: resource_link`) | `name`: missing or `null`; `uri`: missing or `null` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `systemPrompt.content[]`; `session/prompt` request to the agent, in `prompt[]`; `session/update` notification to the client, in `update.content` of `sessionUpdate: agent_message_chunk`, `sessionUpdate: agent_thought_chunk`, `sessionUpdate: compaction_summary_chunk`, `sessionUpdate: session_message_chunk`, `sessionUpdate: user_message_chunk` |
| `ContentBlockText` (`type: text`) | `text`: missing or `null` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `systemPrompt.content[]`; `session/prompt` request to the agent, in `prompt[]`; `session/update` notification to the client, in `update.content` of `sessionUpdate: agent_message_chunk`, `sessionUpdate: agent_thought_chunk`, `sessionUpdate: compaction_summary_chunk`, `sessionUpdate: session_message_chunk`, `sessionUpdate: user_message_chunk` |
| `CreateElicitationForm` (`mode: form`) | `requestedSchema`: missing or `null` | `elicitation/create` request to the client |
| `CreateElicitationUrl` (`mode: url`) | `url`: missing or `null` | `elicitation/create` request to the client |
| `CreateTerminalResponse` | `terminalId`: missing or `null` | `terminal/create` response to the agent |
| `EnumOption` | `const`: missing or `null`; `title`: missing or `null` | `elicitation/create` request to the client, in `requestedSchema.properties{}.items.anyOf[]` of `mode: form` › `type: array` › `titled`; `elicitation/create` request to the client, in `requestedSchema.properties{}.oneOf[]` of `mode: form` › `type: string` |
| `EnvVariable` | `name`: missing or `null`; `value`: missing or `null` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `mcpServers[].env[]` of `stdio`; `terminal/create` request to the client, in `env[]` |
| `HttpHeader` | `name`: missing or `null`; `value`: missing or `null` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `mcpServers[].headers[]` of `type: http`, `type: sse` |
| `InitialSessionConfigOptionBoolean` (`type: boolean`) | `value`: missing or `null` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `configOptions[]` |
| `InitializeRequest` | `protocolVersion`: missing or `null` | `initialize` request to the agent |
| `InitializeResponse` | `protocolVersion`: missing or `null` | `initialize` response to the client |
| `ListSessionsResponse` | `sessions`: missing | `session/list` response to the client |
| `McpServerAcpInline` (`type: acp`) | `name`: missing or `null` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `mcpServers[]` |
| `McpServerHttpInline` (`type: http`) | `name`: missing or `null`; `url`: missing or `null` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `mcpServers[]` |
| `McpServerSseInline` (`type: sse`) | `name`: missing or `null`; `url`: missing or `null` | `session/fork`, `session/load`, `session/new`, `session/resume` requests to the agent, in `mcpServers[]` |
| `NewSessionResponse` | `sessionId`: missing or `null` | `session/new` response to the client |
| `PermissionOption` | `name`: missing or `null` | `session/request_permission` request to the client, in `options[]` |
| `PlanUpdateContentMarkdown` (`type: markdown`) | `content`: missing or `null` | `session/update` notification to the client, in `update.plan` of `sessionUpdate: plan_update` |
| `PromptResponse` | `stopReason`: missing or `null` | `session/prompt` response to the client |
| `ReadTextFileResponse` | `content`: missing or `null` | `fs/read_text_file` response to the agent |
| `RequestPermissionOutcomeSelected` (`outcome: selected`) | `optionId`: missing or `null` | `session/request_permission` response to the agent, in `outcome` |
| `RequestPermissionResponse` | `outcome`: missing | `session/request_permission` response to the agent |
| `SessionUpdateNotice` (`sessionUpdate: notice`) | `title`: missing or `null` | `session/update` notification to the client, in `update` |
| `SessionUpdateToolCall` (`sessionUpdate: tool_call`) | `title`: missing or `null` | `session/update` notification to the client, in `update` |
| `SessionUsageUpdate` (`sessionUpdate: usage_update`) | `size`: missing or `null`; `used`: missing or `null` | `session/update` notification to the client, in `update` |
| `SetSessionConfigOptionBoolean` (`type: boolean`) | `value`: missing or `null` | `session/set_config_option` request to the agent |
| `SetSessionConfigOptionResponse` | `configOptions`: missing | `session/set_config_option` response to the client |
| `TerminalOutputResponse` | `output`: missing or `null`; `truncated`: missing or `null` | `terminal/output` response to the agent |
| `UnstableAuthStatusResponse` | `state`: missing or `null` | `auth/status` response to the client |
| `UnstableForkSessionResponse` | `sessionId`: missing or `null` | `session/fork` response to the client |
| `UnstableListProvidersResponse` | `providers`: missing or `null` | `providers/list` response to the client |
| `UnstableMcpError` | `code`: missing or `null`; `message`: missing or `null` | `mcp/message` response to the agent, in `error` of `error` |
| `UnstableProviderCurrentConfig` | `apiType`: missing or `null`; `baseUrl`: missing or `null` | `providers/list` response to the client, in `providers[].current` |
| `UnstableProviderInfo` | `providerId`: missing or `null`; `required`: missing or `null`; `supported`: missing | `providers/list` response to the client, in `providers[]` |
| `UnstableSessionStatusResponse` | `status`: missing or `null` | `session/status` response to the client |
| `UnstableSetSessionTitleResponse` | `title`: missing or `null` | `session/set_title` response to the client |

The rows come from the generated decoders of this release and of the checks above: each `requireMember` call on a path from a message that a connection reads, without the members that `Validate` or the choice of a union variant already rejected, and without lenient properties.

## Integer ranges

Since v0.20.0 an integer of format `uint32` or `uint64` is an `int64`, and one of format `int32` or `uint16` is an `int`, and the SDK checked neither `format` nor `minimum` and `maximum`. So `"line": -1` in `fs/read_text_file` reached `Client.ReadTextFile`, and `"protocolVersion": 70000` reached `Agent.Initialize`. The Rust SDK reads each integer as the Rust type that its format names (`i32`, `u16`, `u32`, `u64`) and fails the message, so v0.21.0 checks the range: the format's range, narrowed by `minimum` and `maximum`.

- `Validate` checks every integer that a field holds, required, optional, an item or a map member, also through a named type such as `ProtocolVersion`. A request out of range gets `-32602` and a notification is dropped, with an error such as `line must be between 0 and 4294967295`, `outputByteLimit must be at least 0` or `update: used must be at least 0`.
- A property with `x-deserialize-default-on-error`, such as `line` of a tool call location or an `exitCode`, reads a value out of range as unset and keeps the object around it, as the Rust SDK does. So does a lenient property that holds an object with an integer out of range: `"usage": {"totalTokens": -1, ...}` of a `session/prompt` result reads as no usage.
- An integer of format `int64`, such as `size` of a resource link, needs no check: an `int64` holds every value.
- A `uint64` above `math.MaxInt64` still fails to decode, as in v0.20.0, because an `int64` cannot hold it. The Rust SDK accepts it.
- `minLength` and `minItems`, such as a `notice` with `"title": ""` or a prompt suggestion with `"prompt": []`, and the string format `uri` are not checked. The Rust SDK does not check them either.

Every property that now rejects an integer out of range, or reads it as unset:

| Type | Property | Allowed | Where it arrives, and what a value out of range does there |
| --- | --- | --- | --- |
| `CreateTerminalRequest` | `outputByteLimit` | at least 0 | `terminal/create` request to the client: -32602, no handler call |
| `ElicitationPropertySchemaArray` | `maxItems` | at least 0 | `elicitation/create` request to the client, in `requestedSchema.properties{}`: -32602, no handler call |
| `ElicitationPropertySchemaArray` | `minItems` | at least 0 | `elicitation/create` request to the client, in `requestedSchema.properties{}`: -32602, no handler call |
| `ElicitationPropertySchemaString` | `maxLength` | between 0 and 4294967295 | `elicitation/create` request to the client, in `requestedSchema.properties{}`: -32602, no handler call |
| `ElicitationPropertySchemaString` | `minLength` | between 0 and 4294967295 | `elicitation/create` request to the client, in `requestedSchema.properties{}`: -32602, no handler call |
| `Error` | `code` | between -2147483648 and 2147483647 | `session/update` notification to the client, in `update.state.error`: `update.state.error` reads as unset |
| `InitializeRequest` | `protocolVersion` | between 0 and 65535 | `initialize` request to the agent: -32602, no handler call |
| `InitializeResponse` | `protocolVersion` | between 0 and 65535 | `initialize` response to the client: not checked, because the SDK does not call `Validate` on a response |
| `MultiSelectPropertySchema` | `maxItems` | at least 0 | no message carries it; `Validate` reports it when you call it |
| `MultiSelectPropertySchema` | `minItems` | at least 0 | no message carries it; `Validate` reports it when you call it |
| `ReadTextFileRequest` | `limit` | between 0 and 4294967295 | `fs/read_text_file` request to the client: -32602, no handler call |
| `ReadTextFileRequest` | `line` | between 0 and 4294967295 | `fs/read_text_file` request to the client: -32602, no handler call |
| `SessionUsageUpdate` | `size` | at least 0 | `session/update` notification to the client, in `update`: dropped, no handler call |
| `SessionUsageUpdate` | `used` | at least 0 | `session/update` notification to the client, in `update`: dropped, no handler call |
| `StringPropertySchema` | `maxLength` | between 0 and 4294967295 | no message carries it; `Validate` reports it when you call it |
| `StringPropertySchema` | `minLength` | between 0 and 4294967295 | no message carries it; `Validate` reports it when you call it |
| `TerminalExitStatus` | `exitCode` | between 0 and 4294967295 | `terminal/output` response to the agent, in `exitStatus`: `exitStatus.exitCode` reads as unset |
| `ToolCallLocation` | `line` | between 0 and 4294967295 | `session/request_permission` request to the client, in `toolCall.locations[]`: `toolCall.locations[].line` reads as unset; `session/update` notification to the client, in `update.locations[]`: `update.locations[].line` reads as unset |
| `UnstableMcpError` | `code` | between -2147483648 and 2147483647 | `mcp/message` response to the agent, in `error`: not checked, because the SDK does not call `Validate` on a response |
| `Usage` | `cachedReadTokens` | at least 0 | `session/prompt` response to the client, in `usage`: `usage.cachedReadTokens` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage.cachedReadTokens` reads as unset |
| `Usage` | `cachedWriteTokens` | at least 0 | `session/prompt` response to the client, in `usage`: `usage.cachedWriteTokens` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage.cachedWriteTokens` reads as unset |
| `Usage` | `inputTokens` | at least 0 | `session/prompt` response to the client, in `usage`: `usage` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage` reads as unset |
| `Usage` | `outputTokens` | at least 0 | `session/prompt` response to the client, in `usage`: `usage` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage` reads as unset |
| `Usage` | `thoughtTokens` | at least 0 | `session/prompt` response to the client, in `usage`: `usage.thoughtTokens` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage.thoughtTokens` reads as unset |
| `Usage` | `totalTokens` | at least 0 | `session/prompt` response to the client, in `usage`: `usage` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage` reads as unset |
| `UsageUpdate` | `size` | at least 0 | no message carries it; `Validate` reports it when you call it |
| `UsageUpdate` | `used` | at least 0 | no message carries it; `Validate` reports it when you call it |
| `WaitForTerminalExitResponse` | `exitCode` | between 0 and 4294967295 | `terminal/wait_for_exit` response to the agent: `exitCode` reads as unset |

The rows come from the generated code: the range checks of each `Validate`, and the ranges in the shapes of the lenient decoders, followed from each method that the connection reads.

These types get a `Validate` method, because they check a range or hold a type that does: `ElicitationPropertySchemaString`, `SessionUsageUpdate`, `StringPropertySchema`, `UnstableMcpError`, `UnstableMessageMcpError`, `UsageUpdate`, `Usage`.
