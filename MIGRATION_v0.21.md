# Migrating to v0.21.0

v0.21.0 makes inbound decoding stricter: the SDK rejects a message that the ACP schema forbids and that the Rust SDK rejects too, apart from the few differences in bold below. The ACP schema stays at 1.32.0, and `acp.Agent` and `acp.Client` do not change. The integer fields take the Go type of their schema format, such as `uint32` for `ReadTextFileRequest.Line`. The compiler reports a use that mixes a changed field with a value of its old type. It does not report arithmetic among changed fields, which can now wrap: `u.Size - u.Used` of a `UsageUpdate` with `Size` 1 and `Used` 3 gives 18446744073709551614, where v0.20.0 gave -2. Nor does it report a conversion to a named type that changed: `acp.ProtocolVersion(n)` or `acp.ErrorCodeOther(n)` with an `int` `n` still compiles and now wraps past the new range: with `n := 70000`, `acp.ProtocolVersion(n)` is 4464 (the constant `acp.ProtocolVersion(70000)` does not compile), and `acp.ErrorCodeOther(n)` wraps only past `int32`. See [Integer types and ranges](#integer-types-and-ranges) for how to find them.

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
| Each integer has the Go type of its schema format, such as `uint32` or `uint64`: a value out of its range fails to decode, or reads as unset where the schema tolerates a bad value | [Integer types and ranges](#integer-types-and-ranges) |
| `NewAgentSideConnection`, `NewClientSideConnection` and `NewConnection` take `...ConnectionOption`, and every connection limits what it holds from the peer | [Connection constructors and limits](#connection-constructors-and-limits) |
| Log attributes hold strings and numbers: `err` is the error's text, and peer text is cut to 1 KiB | [Log attributes](#log-attributes) |
| A `null` in a `tool_call_update` field, or in `_meta` of a `session_info_update`, reads as unset, and `PatchNull` there is left out | [Null in a tool call update](#null-in-a-tool-call-update) |
| An object that holds one of its properties twice fails to decode | [Duplicate members](#duplicate-members) |

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

- Every union, and each struct that has one of the checks above, now has a `Validate` method: 114 types more than in v0.20.0, such as `ContentChunk`, `McpServerAcpInline` and `PermissionOption`. A `oneOf` union's `Validate` requires exactly one variant set, and an `anyOf` union's one at least (`McpServer has no variant set`), so `Validate` on a zero union fails.
- `json.Marshal` of a zero union, such as `acp.ContentBlock{}`, fails with `ContentBlock has no variant set`. An `anyOf` union's `Validate` gives the same text. A `oneOf` union's `Validate` gives `ContentBlock must have exactly one variant set`, because it also rejects two variants set. v0.20.0 failed the marshal too: with encoding/json's `unexpected end of JSON input` for 26 unions, and with this same text for the other 5 (`CreateElicitationForm`, `CreateElicitationOther`, `CreateElicitationUrl`, `ElicitationFormMode` and `ElicitationUrlMode`). A send method, such as `SessionUpdate` with no update set, returns it inside its `-32602` error, and nothing is sent. A handler whose result holds a zero union, such as a `RequestPermission` that returns `RequestPermissionResponse{}`, makes the connection reply `-32603` with this text in `data.error`, so the peer reads `RequestPermissionOutcome has no variant set` where v0.20.0 sent `unexpected end of JSON input`.
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

## Integer types and ranges

Each integer now has the Go type of the Rust type that its schema `format` names: `int32`, `int64`, `uint16`, `uint32` or `uint64`. In v0.20.0 an integer of format `uint32` or `uint64` was an `int64`, one of format `int32` or `uint16` was an `int`, and the SDK checked neither range, so `"line": -1` in `fs/read_text_file` reached `Client.ReadTextFile`, and `"protocolVersion": 70000` reached `Agent.Initialize`. The Rust SDK reads each integer as the Rust type that its format names and fails the message, and so does the SDK now, because `encoding/json` fails a value that its Go type cannot hold.

- A value out of the range of its format fails to decode wherever it arrives. A request gets `-32602` and a notification is dropped, with an error such as `json: cannot unmarshal number -1 into Go struct field ReadTextFileRequest.line of type uint32`. A response fails too: the call that sent the request returns the decode error. v0.20.0 checked no response.
- A `uint64`, such as `used` of a `usage_update` or a token count of `Usage`, takes every value up to 2^64-1, as in the Rust SDK. v0.20.0 failed a value above `math.MaxInt64`.
- `-0` in an unsigned integer, and a number with a fraction or an exponent, such as `1.0` or `1e2`, fail to decode, as in the Rust SDK. **A `-0` in a signed integer reads as 0. The Rust SDK rejects it in `ErrorCode`, `Error.code`, `McpError.code`, `RequestId` and `IntegerPropertySchema.minimum` and `.maximum`, reads it as the number `-0.0` in `ElicitationContentValue`, where the SDK reads the integer 0, and leaves the lenient `ResourceLink.size` and `IntegerPropertySchema.default` unset, where the SDK reads 0.**
- A property with `x-deserialize-default-on-error`, such as `line` of a tool call location or an `exitCode`, reads a value out of range as unset and keeps the object around it, as the Rust SDK does. So does a lenient property that holds an object with an integer out of range: `"usage": {"totalTokens": -1, ...}` of a `session/prompt` result reads as no usage.
- A generated field cannot hold a value out of range, so the SDK sends none from one. `RequestError.Code` stays an `int`: a handler that returns a code above `math.MaxInt32` sends it as it is. The Rust SDK fails to read it, and so does this SDK where `int` has 32 bits, such as on 386; on a 64-bit platform this SDK reads it. Keep an error code within `int32`.
- `minLength` and `minItems`, such as a `notice` with `"title": ""` or a prompt suggestion with `"prompt": []`, and the string format `uri` are not checked. The Rust SDK does not check them either.

A TypeScript peer reads a JSON number as a JavaScript number, which holds every integer up to 2^53 exactly. The TypeScript SDK 2.7.0 checks the range of a `uint16`, `uint32` and `int32`, reads a `uint64` as any integer of at least 0 that `JSON.parse` returns, and an `int64` as any integer: `18446744073709551615` reads as 18446744073709552000, which it writes back as such, and which the SDK and the Rust SDK then reject. An `int64` rounds the same way: a `RequestId` of `9223372036854775807` comes back as `9223372036854776000`, which the SDK rejects as above `int64`. Keep a `uint64` or an `int64` that a TypeScript peer must read within 2^53 of 0.

### Field types

| Field or type | v0.20.0 | v0.21.0 |
| --- | --- | --- |
| `ReadTextFileRequest.Line`, `ReadTextFileRequest.Limit` | `*int64` | `*uint32` |
| `ToolCallLocation.Line` | `*int64` | `*uint32` |
| `TerminalExitStatus.ExitCode`, `WaitForTerminalExitResponse.ExitCode` | `*int64` | `*uint32` |
| `StringPropertySchema.MinLength`, `StringPropertySchema.MaxLength` | `*int64` | `*uint32` |
| `ElicitationPropertySchemaString.MinLength`, `ElicitationPropertySchemaString.MaxLength` | `*int64` | `*uint32` |
| `CreateTerminalRequest.OutputByteLimit` | `*int64` | `*uint64` |
| `MultiSelectPropertySchema.MinItems`, `MultiSelectPropertySchema.MaxItems` | `*int64` | `*uint64` |
| `ElicitationPropertySchemaArray.MinItems`, `ElicitationPropertySchemaArray.MaxItems` | `*int64` | `*uint64` |
| `Usage.InputTokens`, `Usage.OutputTokens`, `Usage.TotalTokens` | `int64` | `uint64` |
| `Usage.ThoughtTokens`, `Usage.CachedReadTokens`, `Usage.CachedWriteTokens` | `*int64` | `*uint64` |
| `UsageUpdate.Used`, `UsageUpdate.Size`, `SessionUsageUpdate.Used`, `SessionUsageUpdate.Size` | `int64` | `uint64` |
| `ProtocolVersion` | `int` | `uint16` |
| `ErrorCodeParseError`, `ErrorCodeInvalidRequest`, `ErrorCodeMethodNotFound`, `ErrorCodeInvalidParams`, `ErrorCodeInternalError`, `ErrorCodeRequestCancelled`, `ErrorCodeAuthenticationRequired`, `ErrorCodeResourceNotFound`, `ErrorCodeOther` | `int` | `int32` |
| `UnstableMcpError.Code` | `int` | `int32` |

The fields of format `int64` keep their type: `ResourceLink.Size`, the `Minimum`, `Maximum` and `Default` of `IntegerPropertySchema`, `RequestIdNumber` and `ElicitationContentValueInteger`. `RequestError.Code` stays an `int`.

### Code changes

| v0.20.0 | v0.21.0 |
| --- | --- |
| `Line: acp.Ptr[int64](10)`, `MinLength: acp.Ptr[int64](1)` | `Line: acp.Ptr[uint32](10)`, `MinLength: acp.Ptr[uint32](1)` |
| `OutputByteLimit: acp.Ptr[int64](limit)` with an `int64` limit | `OutputByteLimit: acp.Ptr(uint64(limit))`, after you check that `limit >= 0` |
| `acp.UsageUpdate{Used: int64(used), Size: int64(size)}` | `acp.UsageUpdate{Used: uint64(used), Size: uint64(size)}`, after you check that both are at least 0 |
| `lines[int(max(0, min(*p.Line-1, int64(len(lines))))):]` | `if *p.Line > 0 { start = int(min(uint64(*p.Line-1), uint64(len(lines)))) }`: an unsigned `*p.Line-1` wraps to 4294967295 when `Line` is 0, and an `int(...)` of a `uint32` wraps on a 32-bit target above 2^31 |
| `code := *status.ExitCode` (an `int64`) | `code := *status.ExitCode` (a `uint32`); `os.Exit(int(code))` as before |
| `var total int64 = usage.TotalTokens` | `total := usage.TotalTokens` (a `uint64`), or `int64(usage.TotalTokens)` when you know that it fits |
| `acp.ErrorCodeOther(code)` with an `int` code | the same with an `int32` code, or with an `int` that you checked is within `int32` first: the v0.20.0 form still compiles and wraps |
| `acp.ProtocolVersion(v)` with an `int` v | the same with a `uint16` v, or with an `int` that you checked is between 0 and 65535 first: the v0.20.0 form still compiles and wraps |
| `case int64:` in a type switch on `any(*loc.Line)` or another changed field | `case uint32:`, the field's new type; such code compiles as before and stops matching |
| `reflect.ValueOf(v).Kind() == reflect.Int` for a `ProtocolVersion` or an `ErrorCode*` | `reflect.Uint16` or `reflect.Int32`; such code compiles as before and stops matching. A type switch with `case int:` never matched these named types and still does not, and `acp.ProtocolVersionNumber` stays an untyped constant, so `any(acp.ProtocolVersionNumber)` is still an `int` |

The compiler does not find the conversions, the type switches, the `reflect` checks, or unsigned arithmetic that now wraps, such as `u.Size - u.Used` when `Used` is larger. The `G115` check of gosec finds the conversions: `golangci-lint run --enable-only gosec` with `gosec.includes: [G115]` in `.golangci.yml`, or `gosec -include=G115 ./...`, reports `integer overflow conversion int -> uint16` at `acp.ProtocolVersion(x)`. Find the rest by searching your code for the fields and types in [Field types](#field-types).

Every property whose integer now fails to decode out of range, or reads as unset:

| Type | Property | Allowed | Where it arrives, and what a value out of range does there |
| --- | --- | --- | --- |
| `CreateTerminalRequest` | `outputByteLimit` | between 0 and 18446744073709551615 | `terminal/create` request to the client: -32602, no handler call |
| `ElicitationPropertySchemaArray` | `maxItems` | between 0 and 18446744073709551615 | `elicitation/create` request to the client, in `requestedSchema.properties{}`: -32602, no handler call |
| `ElicitationPropertySchemaArray` | `minItems` | between 0 and 18446744073709551615 | `elicitation/create` request to the client, in `requestedSchema.properties{}`: -32602, no handler call |
| `ElicitationPropertySchemaString` | `maxLength` | between 0 and 4294967295 | `elicitation/create` request to the client, in `requestedSchema.properties{}`: -32602, no handler call |
| `ElicitationPropertySchemaString` | `minLength` | between 0 and 4294967295 | `elicitation/create` request to the client, in `requestedSchema.properties{}`: -32602, no handler call |
| `Error` | `code` | between -2147483648 and 2147483647 | `session/update` notification to the client, in `update.state.error`: `update.state.error` reads as unset |
| `InitializeRequest` | `protocolVersion` | between 0 and 65535 | `initialize` request to the agent: -32602, no handler call |
| `InitializeResponse` | `protocolVersion` | between 0 and 65535 | `initialize` response to the client: `Initialize` returns the decode error |
| `MultiSelectPropertySchema` | `maxItems` | between 0 and 18446744073709551615 | no message carries it; `json.Unmarshal` of a value that holds it fails |
| `MultiSelectPropertySchema` | `minItems` | between 0 and 18446744073709551615 | no message carries it; `json.Unmarshal` of a value that holds it fails |
| `ReadTextFileRequest` | `limit` | between 0 and 4294967295 | `fs/read_text_file` request to the client: -32602, no handler call |
| `ReadTextFileRequest` | `line` | between 0 and 4294967295 | `fs/read_text_file` request to the client: -32602, no handler call |
| `SessionUsageUpdate` | `size` | between 0 and 18446744073709551615 | `session/update` notification to the client, in `update`: dropped, no handler call |
| `SessionUsageUpdate` | `used` | between 0 and 18446744073709551615 | `session/update` notification to the client, in `update`: dropped, no handler call |
| `StringPropertySchema` | `maxLength` | between 0 and 4294967295 | no message carries it; `json.Unmarshal` of a value that holds it fails |
| `StringPropertySchema` | `minLength` | between 0 and 4294967295 | no message carries it; `json.Unmarshal` of a value that holds it fails |
| `TerminalExitStatus` | `exitCode` | between 0 and 4294967295 | `terminal/output` response to the agent, in `exitStatus`: `exitStatus.exitCode` reads as unset |
| `ToolCallLocation` | `line` | between 0 and 4294967295 | `session/request_permission` request to the client, in `toolCall.locations[]`: `toolCall.locations[].line` reads as unset; `session/update` notification to the client, in `update.locations[]`: `update.locations[].line` reads as unset |
| `UnstableMcpError` | `code` | between -2147483648 and 2147483647 | `mcp/message` response to the agent, in `error`: `UnstableMessageMcp` returns the decode error |
| `Usage` | `cachedReadTokens` | between 0 and 18446744073709551615 | `session/prompt` response to the client, in `usage`: `usage.cachedReadTokens` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage.cachedReadTokens` reads as unset |
| `Usage` | `cachedWriteTokens` | between 0 and 18446744073709551615 | `session/prompt` response to the client, in `usage`: `usage.cachedWriteTokens` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage.cachedWriteTokens` reads as unset |
| `Usage` | `inputTokens` | between 0 and 18446744073709551615 | `session/prompt` response to the client, in `usage`: `usage` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage` reads as unset |
| `Usage` | `outputTokens` | between 0 and 18446744073709551615 | `session/prompt` response to the client, in `usage`: `usage` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage` reads as unset |
| `Usage` | `thoughtTokens` | between 0 and 18446744073709551615 | `session/prompt` response to the client, in `usage`: `usage.thoughtTokens` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage.thoughtTokens` reads as unset |
| `Usage` | `totalTokens` | between 0 and 18446744073709551615 | `session/prompt` response to the client, in `usage`: `usage` reads as unset; `session/update` notification to the client, in `update.state.usage`: `update.state.usage` reads as unset |
| `UsageUpdate` | `size` | between 0 and 18446744073709551615 | no message carries it; `json.Unmarshal` of a value that holds it fails |
| `UsageUpdate` | `used` | between 0 and 18446744073709551615 | no message carries it; `json.Unmarshal` of a value that holds it fails |
| `WaitForTerminalExitResponse` | `exitCode` | between 0 and 4294967295 | `terminal/wait_for_exit` response to the agent: `exitCode` reads as unset |

The rows list each property whose format is narrower than `int64`, from the generated code, followed from each method that the connection reads.

## Connection constructors and limits

`NewAgentSideConnection`, `NewClientSideConnection` and `NewConnection` take options after the reader: `opts ...ConnectionOption`. A call compiles as before. Code that stores one of them in a variable or a field of the v0.20.0 function type, such as `var newConn func(acp.Agent, io.Writer, io.Reader) *acp.AgentSideConnection = acp.NewAgentSideConnection`, stops compiling: wrap the call in a function literal.

Every connection now limits the requests that it holds from the peer. A request that does not fit the byte budget (`WithByteBudget`, 64 MiB by default) or that arrives while the connection holds 1024 requests (`WithMaxInboundRequests`) gets a `-32603` reply, and its handler does not run. A notification that does not fit the byte budget closes the connection. `WithMaxQueuedNotifications` sets the queue of waiting notifications, which v0.20.0 fixed at 1024.

## Log attributes

Every value that the connection logs is now a string or a number, so a peer cannot make one log record grow without bound.

- **An `err` attribute holds the error's text, not the `error` value.** A `slog.Handler` that type-asserts `error`, or calls `errors.Is` or `errors.As` on the attribute, no longer matches. Match on the record's message or on the text instead.
- Text from the peer, such as a raw line, a method, an id or an error message, is cut to 1 KiB and ends with `... (N bytes)`, where N is its full length. Numbers, booleans, durations and times keep their type.

## Null in a tool call update

ACP v1 defines no clear for the fields of a `ToolCallUpdate` and for `_meta` of a `SessionInfoUpdate`: a `null` there leaves the stored value unchanged, as an absent member does. The protocol docs say so for `name`, `rawInput` and `rawOutput`, the v2 migration guide says that v1 treats `null` like omission, and the Rust SDK reads every such `null` as absent. v0.20.0 read it as `IsNull()` and asked you to treat that like `IsUnset()`. v0.21.0 reads it as unset, and the API does not change:

- A `null` member leaves the field as it is, as an absent member does: decoded into a new struct, it reads as `IsUnset()`, and `IsNull()` is false; a struct that already holds a value or a `PatchNull` keeps it. This holds for every field of `ToolCallUpdate`, the `toolCall` of `session/request_permission`, the `tool_call_update` variant of `SessionUpdate` (`SessionToolCallUpdate`), and `_meta` of `SessionInfoUpdate` and of its variant `SessionSessionInfoUpdate`. Code that treated `IsNull()` like `IsUnset()` there needs no change.
- `acp.PatchNull[T]()` in these fields is left out of the JSON, where v0.20.0 wrote `null`. ACP v1 reads both as "unchanged", and the Rust SDK leaves the member out too.
- To clear `content` or `locations`, send an empty list, as in v0.20.0: `acp.PatchSet([]acp.ToolCallContent{})` or `WithUpdateContent([]acp.ToolCallContent{})`.
- `title` and `updatedAt` of `SessionInfoUpdate`, and the patch fields of `CompactionUpdate`, `SubagentUpdate` and `SessionMessage`, whose schema says that `null` clears them, still read and write `null` as a clear.

## Duplicate members

v0.20.0 read an object that holds the same member twice as `encoding/json` does: the last value won. The Rust SDK fails it with `duplicate field`, and v0.21.0 fails the decode too (#141), when the member is a property of the object's type, at any depth: a field of a message, `_meta` itself, the discriminator of a union, such as `type` or `sessionUpdate`, a field of a variant, and the `id` or `method` of an envelope type, such as `ClientRequest`, that you decode yourself. A key counts by its name without escapes, so `"t\u0079pe"` repeats `"type"`. The error is a `*json.UnmarshalTypeError` that names the struct, the path of the property, and the type of its field:

```text
json: cannot unmarshal duplicate member into Go struct field SessionNotification.sessionId of type acp.SessionId
json: cannot unmarshal duplicate member into Go struct field RequestPermissionRequest.toolCall.toolCallId of type acp.ToolCallId
```

A request that repeats one gets `-32602` invalid params, with that error in `data.error`, and your handler is not called. A notification that repeats one is dropped, and the connection logs it at Error (`failed to handle notification`). A response that repeats one makes your call, such as `ClientSideConnection.NewSession`, return a `-32603` error with that error in `data.error`, as for any result that does not decode.

These still decode as in v0.20.0, and the last value wins, as in the Rust SDK:

- A member that is no property of the type, such as an unknown `"x"`, or one that `additionalProperties` keeps.
- A key of a map: a key of `_meta`, or of the `content` of an accepted elicitation.
- The discriminator of a catch-all variant, such as `"state"` of a state update whose state the schema does not name. The Rust SDK reads a catch-all into a map.

Inside a lenient property (`x-deserialize-default-on-error` or `x-deserialize-skip-invalid-items`), a value that repeats a property reads as the default, or its item is dropped, as the Rust SDK does: an `initialize` whose `clientInfo` holds `name` twice reads as one without `clientInfo`. Inside a union variant the union reports `invalid variant payload`, as for a missing member, and tries its next variant.

To find the cause in a peer's output, take the struct and the path from the error, such as `toolCall.toolCallId` of `RequestPermissionRequest`, and look in the raw JSON-RPC line for that key twice in one object. For `invalid variant payload`, check each key of the union's object.

**Some repeats still differ from the Rust SDK:**

- The Rust SDK reads an agent authentication method, one without `type`, and the scope (`sessionId`, `toolCallId`, `requestId`) of an elicitation whose `mode` the schema does not name through a map, so it keeps the last of a repeated key there. The Go SDK rejects both.
- The JSON-RPC envelope that the connection reads first, the `jsonrpc`, `id`, `method`, `params`, `result` and `error` of each line, still keeps the last of a repeated key.
- A key in another case, such as `"sessionId"` and then `"SessionId"`, is no repeat. `encoding/json` reads both into the field, so the last wins, and the Rust SDK skips the second as unknown, so the first wins.
