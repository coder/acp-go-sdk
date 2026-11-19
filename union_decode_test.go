package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestUnion_UnmarshalJSON_RejectsAnObjectThatMatchesNoVariant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		into any
		in   string
	}{
		{"embedded resource without text or blob", new(EmbeddedResourceResource), `{"uri":"file:///a"}`},
		{"tool call content of an unknown type", new(ToolCallContent), `{"type":"bogus"}`},
		{"MCP server of an unknown type without a command", new(McpServer), `{"type":"bogus","name":"n"}`},
		{"form elicitation without a scope", new(CreateElicitationRequest), `{"mode":"form","message":"m","requestedSchema":{"type":"object","properties":{}}}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := json.Unmarshal([]byte(tt.in), tt.into); err == nil {
				t.Fatalf("unmarshal %s gives %+v, want an error", tt.in, tt.into)
			}
		})
	}
}

func TestToolCall_UnmarshalJSON_DropsContentThatMatchesNoVariant(t *testing.T) {
	t.Parallel()
	var call ToolCall
	in := `{"toolCallId":"1","title":"x","content":[{"type":"bogus"},{"type":"terminal","terminalId":"t"}]}`
	if err := json.Unmarshal([]byte(in), &call); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(call.Content) != 1 || call.Content[0].Terminal == nil {
		out, _ := json.Marshal(call.Content)
		t.Fatalf("content = %s, want only the terminal", out)
	}
}

func TestUnion_UnmarshalJSON_RejectsAnUnknownDiscriminator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		into any
		in   string
	}{
		{"permission outcome", new(RequestPermissionOutcome), `{"outcome":"bogus"}`},
		{"content block with the keys of a text block", new(ContentBlock), `{"type":"bogus","text":"x"}`},
		{"content block without a type", new(ContentBlock), `{"text":"x"}`},
		{"session update", new(SessionUpdate), `{"sessionUpdate":"bogus_update"}`},
		{"session notification", new(SessionNotification), `{"sessionId":"s","update":{"sessionUpdate":"bogus_update"}}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := json.Unmarshal([]byte(tt.in), tt.into); err == nil {
				t.Fatalf("unmarshal %s gives %+v, want an error", tt.in, tt.into)
			}
		})
	}
}

func TestUnion_UnmarshalJSON_VariantWithoutAConstTakesAnUnknownDiscriminator(t *testing.T) {
	t.Parallel()
	var method AuthMethod
	if err := json.Unmarshal([]byte(`{"type":"bogus","id":"a","name":"n"}`), &method); err != nil || method.Agent == nil {
		t.Errorf("auth method with the keys of the agent variant = %+v (error %v), want the agent variant", method, err)
	}
	var server McpServer
	if err := json.Unmarshal([]byte(`{"type":"bogus","name":"n","command":"c","args":[],"env":[]}`), &server); err != nil || server.Stdio == nil {
		t.Errorf("MCP server with the keys of the stdio variant = %+v (error %v), want the stdio variant", server, err)
	}
}

func TestUnion_UnmarshalJSON_NullRequiredKeySelectsNoVariant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		into any
		in   string
	}{
		{"stdio MCP server with a null command", new(McpServer), `{"name":"n","command":null,"args":[],"env":[]}`},
		{"embedded resource with a null text", new(EmbeddedResourceResource), `{"uri":"file:///a","text":null}`},
		{"agent auth method with a null name", new(AuthMethod), `{"id":"a","name":null}`},
		{"select options with a null name", new(SessionConfigSelectOptions), `[{"value":"v","name":null}]`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := json.Unmarshal([]byte(tt.in), tt.into); err == nil {
				t.Fatalf("unmarshal %s gives %+v, want an error", tt.in, tt.into)
			}
		})
	}
}

func TestUnion_UnmarshalJSON_NullRequiredKeyLeavesTheOtherVariants(t *testing.T) {
	t.Parallel()
	var resource EmbeddedResourceResource
	if err := json.Unmarshal([]byte(`{"uri":"file:///a","text":null,"blob":"b"}`), &resource); err != nil || resource.BlobResourceContents == nil {
		t.Errorf("resource with a null text and a blob = %+v (error %v), want the blob variant", resource, err)
	}
	var mode ElicitationFormMode
	in := `{"requestedSchema":{"type":"object","properties":{}},"requestId":null}`
	if err := json.Unmarshal([]byte(in), &mode); err != nil || mode.Request == nil {
		t.Errorf("form mode with a null requestId, which accepts null, = %+v (error %v), want the request scope", mode, err)
	}
}

func TestUnion_UnmarshalJSON_NullSelectsOnlyANullVariant(t *testing.T) {
	t.Parallel()
	var id RequestId
	if err := json.Unmarshal([]byte(`null`), &id); err != nil || id.Null == nil {
		t.Errorf("request id null = %+v (error %v), want the null variant", id, err)
	}
	for _, in := range []string{`{}`, `{"a":1}`} {
		if err := json.Unmarshal([]byte(in), &id); err == nil {
			t.Errorf("request id %s = %+v, want an error", in, id)
		}
	}
	for name, into := range map[string]any{
		"error code":     new(ErrorCode),
		"select options": new(SessionConfigSelectOptions),
		"content block":  new(ContentBlock),
	} {
		if err := json.Unmarshal([]byte(`null`), into); err == nil {
			t.Errorf("%s null = %+v, want an error", name, into)
		}
	}
}

func TestUnion_UnmarshalJSON_SelectsAVariantByExactKeys(t *testing.T) {
	t.Parallel()
	// The upstream Rust types read keys exactly too.
	for name, tt := range map[string]struct {
		into any
		in   string
	}{
		"discriminator in another case": {new(ContentBlock), `{"Type":"text","text":"hi"}`},
		"required keys in another case": {new(McpServer), `{"Name":"n","Command":"c","Args":[],"Env":[]}`},
		"item keys in another case":     {new(SessionConfigSelectOptions), `[{"Value":"v","Name":"n"}]`},
	} {
		if err := json.Unmarshal([]byte(tt.in), tt.into); err == nil {
			t.Errorf("%s: unmarshal %s gives %+v, want an error", name, tt.in, tt.into)
		}
	}
}

func TestErrorCode_UnmarshalJSON_SelectsTheVariantOfTheCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		variant string
		set     func(ErrorCode) bool
	}{
		{"-32700", "ParseError", func(c ErrorCode) bool { return c.ParseError != nil }},
		{"-32600", "InvalidRequest", func(c ErrorCode) bool { return c.InvalidRequest != nil }},
		{"-32601", "MethodNotFound", func(c ErrorCode) bool { return c.MethodNotFound != nil }},
		{"-32602", "InvalidParams", func(c ErrorCode) bool { return c.InvalidParams != nil }},
		{"-32603", "InternalError", func(c ErrorCode) bool { return c.InternalError != nil }},
		{"-32000", "AuthenticationRequired", func(c ErrorCode) bool { return c.AuthenticationRequired != nil }},
		{"-32002", "ResourceNotFound", func(c ErrorCode) bool { return c.ResourceNotFound != nil }},
		{"7", "Other", func(c ErrorCode) bool { return c.Other != nil }},
	}
	for _, tt := range tests {
		var code ErrorCode
		if err := json.Unmarshal([]byte(tt.in), &code); err != nil || !tt.set(code) {
			t.Errorf("%s decodes to %+v (error %v), want the variant %s", tt.in, code, err, tt.variant)
			continue
		}
		if out, err := json.Marshal(code); err != nil || string(out) != tt.in {
			t.Errorf("%s decodes and encodes to %s (error %v), want it unchanged", tt.in, out, err)
		}
	}
}

func TestToolCall_UnmarshalJSON_DropsContentOfAnUnknownType(t *testing.T) {
	t.Parallel()
	var call ToolCall
	in := `{"toolCallId":"1","title":"x","content":[{"type":"bogus","content":{"type":"text","text":"x"}},{"type":"terminal","terminalId":"t"}]}`
	if err := json.Unmarshal([]byte(in), &call); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(call.Content) != 1 || call.Content[0].Terminal == nil {
		out, _ := json.Marshal(call.Content)
		t.Fatalf("content = %s, want only the terminal", out)
	}
}

// TestUnion_UnmarshalJSON_DecodesLikeTheRustSDK decodes, validates and encodes
// each input, as the Rust SDK reads and writes it. When the variant that an
// input selects does not decode, the next variant that fits gets a turn, as in
// a serde untagged enum. want is the Rust encoding, recorded from
// agent-client-protocol-schema 1.18.0 on 2026-11-19, or "" where Rust rejects
// the input.
func TestUnion_UnmarshalJSON_DecodesLikeTheRustSDK(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		into any
		in   string
		want string
	}{
		{"emb-text-bad-blob-ok", new(EmbeddedResourceResource), `{"uri":"file:///a","text":5,"blob":"Zm9v"}`, `{"blob":"Zm9v","uri":"file:///a"}`},
		{"emb-both-ok", new(EmbeddedResourceResource), `{"uri":"file:///a","text":"t","blob":"Zm9v"}`, `{"text":"t","uri":"file:///a"}`},
		{"emb-text-bad-only", new(EmbeddedResourceResource), `{"uri":"file:///a","text":5}`, ""},
		{"embres-text-bad-blob-ok", new(EmbeddedResource), `{"resource":{"uri":"file:///a","text":5,"blob":"Zm9v"}}`, `{"resource":{"blob":"Zm9v","uri":"file:///a"}}`},
		{"cb-resource-text-bad-blob-ok", new(ContentBlock), `{"type":"resource","resource":{"uri":"file:///a","text":5,"blob":"Zm9v"}}`, `{"type":"resource","resource":{"blob":"Zm9v","uri":"file:///a"}}`},
		{"init-bool-string", new(InitialSessionConfigOption), `{"configId":"c","type":"boolean","value":"yes"}`, `{"configId":"c","value":"yes"}`},
		{"init-bool-number", new(InitialSessionConfigOption), `{"configId":"c","type":"boolean","value":5}`, ""},
		{"init-bool-ok", new(InitialSessionConfigOption), `{"configId":"c","type":"boolean","value":true}`, `{"configId":"c","type":"boolean","value":true}`},
		{"init-unknown-type-string", new(InitialSessionConfigOption), `{"configId":"c","type":"future","value":"v"}`, `{"configId":"c","value":"v"}`},
		{"set-bool-string", new(SetSessionConfigOptionRequest), `{"sessionId":"s","configId":"c","type":"boolean","value":"yes"}`, `{"sessionId":"s","configId":"c","value":"yes"}`},
		{"set-bool-number", new(SetSessionConfigOptionRequest), `{"sessionId":"s","configId":"c","type":"boolean","value":5}`, ""},
		{"newsess-init-bool-string", new(NewSessionRequest), `{"cwd":"/w","mcpServers":[],"configOptions":[{"configId":"c","type":"boolean","value":"yes"}]}`, `{"cwd":"/w","mcpServers":[],"configOptions":[{"configId":"c","value":"yes"}]}`},
		{"mcp-http-bad-stdio-ok", new(McpServer), `{"type":"http","name":"n","url":5,"headers":[],"command":"c","args":[],"env":[]}`, `{"name":"n","command":"c","args":[],"env":[]}`},
		{"mcp-http-bad-only", new(McpServer), `{"type":"http","name":"n","url":5,"headers":[]}`, ""},
		{"mcp-sse-bad-stdio-ok", new(McpServer), `{"type":"sse","name":"n","url":"u","headers":5,"command":"c","args":[],"env":[]}`, `{"name":"n","command":"c","args":[],"env":[]}`},
		{"auth-terminal-bad-agent-keys", new(AuthMethod), `{"type":"terminal","id":"a","name":"n","args":5}`, ""},
		{"auth-terminal-bad-env", new(AuthMethod), `{"type":"terminal","id":"a","name":"n","env":5}`, ""},
		{"auth-unknown-agent", new(AuthMethod), `{"type":"bogus","id":"a","name":"n"}`, `{"id":"a","name":"n"}`},
		{"msi-string-bad-titled-ok", new(MultiSelectItems), `{"type":"string","enum":5,"anyOf":[{"const":"a","title":"A"}]}`, `{"anyOf":[{"const":"a","title":"A"}]}`},
		{"msi-string-bad-only", new(MultiSelectItems), `{"type":"string","enum":5}`, ""},
		{"msi-other-ok", new(MultiSelectItems), `{"type":"future","limit":2}`, `{"type":"future","limit":2}`},
		{"state-idle-bad-stop", new(StateUpdate), `{"state":"idle","stopReason":"error","error":5}`, `{"state":"idle","stopReason":"error"}`},
		{"state-running-bad-meta", new(StateUpdate), `{"state":"running","_meta":5}`, `{"state":"running"}`},
		{"idle-error-bad", new(IdleStateUpdate), `{"stopReason":"error","error":5}`, `{"stopReason":"error"}`},
		{"elresp-accept-bad", new(CreateElicitationResponse), `{"action":"accept","content":5}`, ""},
		{"elresp-accept-badvalue", new(CreateElicitationResponse), `{"action":"accept","content":{"a":{"x":1}}}`, ""},
		{"eps-string-bad", new(ElicitationPropertySchema), `{"type":"string","minLength":"x"}`, ""},
		{"elreq-form-session-bad-request-ok", new(CreateElicitationRequest), `{"mode":"form","sessionId":5,"requestId":"r","message":"m","requestedSchema":{"type":"object","properties":{}}}`, `{"mode":"form","requestId":"r","requestedSchema":{"type":"object","properties":{}},"message":"m"}`},
		{"elreq-form-bad", new(CreateElicitationRequest), `{"mode":"form","sessionId":"s","message":5,"requestedSchema":{"type":"object","properties":{}}}`, ""},
		{"elreq-url-session-bad-request-ok", new(CreateElicitationRequest), `{"mode":"url","sessionId":5,"requestId":"r","message":"m","elicitationId":"e","url":"https://x"}`, `{"mode":"url","requestId":"r","elicitationId":"e","url":"https://x","message":"m"}`},
		{"cb-text-bad", new(ContentBlock), `{"type":"text","text":5}`, ""},
		{"su-chunk-bad", new(SessionUpdate), `{"sessionUpdate":"agent_message_chunk","content":5}`, ""},
		{"selopts-ungrouped-bad-grouped-ok", new(SessionConfigSelectOptions), `[{"value":5,"name":"n","group":"g","options":[]}]`, `[{"group":"g","name":"n","options":[]}]`},
		{"selopts-ungrouped-ok", new(SessionConfigSelectOptions), `[{"value":"v","name":"n","group":"g","options":[]}]`, `[{"value":"v","name":"n"}]`},
		{"perm-selected-bad", new(RequestPermissionOutcome), `{"outcome":"selected","optionId":5}`, ""},
		{"tcc-diff-bad", new(ToolCallContent), `{"type":"diff","path":5,"newText":"x"}`, ""},
		{"rid-float", new(RequestId), `1.5`, ""},
		{"ecv-int", new(ElicitationContentValue), `2`, `2`},
		{"ecv-float", new(ElicitationContentValue), `2.0`, `2.0`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := json.Unmarshal([]byte(tt.in), tt.into)
			if v, ok := tt.into.(interface{ Validate() error }); ok && err == nil {
				err = v.Validate()
			}
			if tt.want == "" {
				if err == nil {
					t.Fatalf("%s decodes to %+v, want an error", tt.in, tt.into)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode %s: %v", tt.in, err)
			}
			out, err := json.Marshal(tt.into)
			if err != nil {
				t.Fatalf("encode the value of %s: %v", tt.in, err)
			}
			var got, want any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("parse %s: %v", out, err)
			}
			if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
				t.Fatalf("parse %s: %v", tt.want, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s decodes and encodes to %s, want %s", tt.in, out, tt.want)
			}
		})
	}
}
