package acp

import (
	"encoding/json"
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
