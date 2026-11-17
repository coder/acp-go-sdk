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
		{"form elicitation without a scope", new(UnstableCreateElicitationRequest), `{"mode":"form","message":"m","requestedSchema":{"type":"object","properties":{}}}`},
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
