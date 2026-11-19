package acp

import (
	"encoding/json"
	"testing"
)

// TestMarshalJSON_EmptyOptionalListOrMapMatchesRust decodes each input, encodes
// the value again, and checks the output against what the Rust SDK writes for
// the same input. A property that the schema lets be null keeps an empty list
// or map, and the others leave it out.
func TestMarshalJSON_EmptyOptionalListOrMapMatchesRust(t *testing.T) {
	t.Parallel()
	// want is recorded from agent-client-protocol-schema 1.18.0 on 2026-11-19.
	cases := []struct {
		name     string
		value    func() any
		in, want string
	}{
		{"ann-audience-empty", func() any { return new(Annotations) }, `{"audience":[]}`, `{"audience":[]}`},
		{"ann-audience-bad-items", func() any { return new(Annotations) }, `{"audience":["bogus"]}`, `{"audience":[]}`},
		{"ann-audience-null", func() any { return new(Annotations) }, `{"audience":null}`, `{}`},
		{"ann-audience-wrong", func() any { return new(Annotations) }, `{"audience":5}`, `{}`},
		{"accept-content-empty", func() any { return new(ElicitationAcceptAction) }, `{"content":{}}`, `{"content":{}}`},
		{"elresp-accept-content-empty", func() any { return new(CreateElicitationResponse) }, `{"action":"accept","content":{}}`, `{"action":"accept","content":{}}`},
		{"elresp-accept-content-absent", func() any { return new(CreateElicitationResponse) }, `{"action":"accept"}`, `{"action":"accept"}`},
		{"elresp-accept-content-null", func() any { return new(CreateElicitationResponse) }, `{"action":"accept","content":null}`, `{"action":"accept"}`},
		{"schema-required-empty", func() any { return new(ElicitationSchema) }, `{"type":"object","properties":{},"required":[]}`, `{"type":"object","properties":{},"required":[]}`},
		{"loadresp-config-empty", func() any { return new(LoadSessionResponse) }, `{"configOptions":[]}`, `{"configOptions":[]}`},
		{"loadresp-config-invalid", func() any { return new(LoadSessionResponse) }, `{"configOptions":[{"bogus":true}]}`, `{"configOptions":[]}`},
		{"loadresp-config-wrong", func() any { return new(LoadSessionResponse) }, `{"configOptions":5}`, `{}`},
		{"newresp-config-invalid", func() any { return new(NewSessionResponse) }, `{"sessionId":"s","configOptions":[{"bogus":true}]}`, `{"sessionId":"s","configOptions":[]}`},
		{"resumeresp-config-empty", func() any { return new(ResumeSessionResponse) }, `{"configOptions":[]}`, `{"configOptions":[]}`},
		{"forkresp-config-empty", func() any { return new(UnstableForkSessionResponse) }, `{"sessionId":"s","configOptions":[]}`, `{"sessionId":"s","configOptions":[]}`},
		{"optsresp-config-empty", func() any { return new(UnstableSessionOptionsResponse) }, `{"configOptions":[]}`, `{"configOptions":[]}`},
		{"msps-default-empty", func() any { return new(MultiSelectPropertySchema) }, `{"type":"array","items":{"type":"string","enum":["a"]},"default":[]}`, `{"items":{"type":"string","enum":["a"]},"default":[]}`},
		{"sps-enum-empty", func() any { return new(StringPropertySchema) }, `{"type":"string","enum":[]}`, `{"enum":[]}`},
		{"sps-oneof-empty", func() any { return new(StringPropertySchema) }, `{"type":"string","oneOf":[]}`, `{"oneOf":[]}`},
		{"msg-content-empty", func() any { return new(SessionMessage) }, `{"messageId":"m","content":[]}`, `{"messageId":"m","content":[]}`},
		{"subagent-dirs-empty", func() any { return new(SubagentUpdate) }, `{"sessionId":"s","additionalDirectories":[]}`, `{"sessionId":"s","additionalDirectories":[]}`},
		{"mcpnotif-params-empty", func() any { return new(UnstableMessageMcpNotification) }, `{"serverId":"x","requestId":"r","method":"m","params":{}}`, `{"serverId":"x","requestId":"r","method":"m","params":{}}`},
		{"mcpreq-params-empty", func() any { return new(UnstableMessageMcpRequest) }, `{"serverId":"x","requestId":"r","method":"m","params":{}}`, `{"serverId":"x","requestId":"r","method":"m","params":{}}`},
		{"compaction-summary-empty", func() any { return new(CompactionUpdate) }, `{"compactionId":"c","status":"completed","summary":[]}`, `{"compactionId":"c","status":"completed","summary":[]}`},
		{"tcu-content-empty", func() any { return new(ToolCallUpdate) }, `{"toolCallId":"t","content":[]}`, `{"toolCallId":"t","content":[]}`},
		// Controls: Rust leaves these empty lists and maps out.
		{"ctrl-terminal-args-empty", func() any { return new(AuthMethodTerminal) }, `{"id":"a","name":"n","args":[],"env":{}}`, `{"id":"a","name":"n"}`},
		{"ctrl-createterm-args-empty", func() any { return new(CreateTerminalRequest) }, `{"sessionId":"s","command":"c","args":[],"env":[]}`, `{"sessionId":"s","command":"c"}`},
		{"ctrl-newsess-dirs-empty", func() any { return new(NewSessionRequest) }, `{"cwd":"/w","mcpServers":[],"additionalDirectories":[]}`, `{"cwd":"/w","mcpServers":[]}`},
		{"ctrl-newsess-config-empty", func() any { return new(NewSessionRequest) }, `{"cwd":"/w","mcpServers":[],"configOptions":[]}`, `{"cwd":"/w","mcpServers":[]}`},
		{"ctrl-setprovider-headers-empty", func() any { return new(UnstableSetProviderRequest) }, `{"providerId":"p","apiType":"anthropic","baseUrl":"u","headers":{}}`, `{"providerId":"p","apiType":"anthropic","baseUrl":"u"}`},
		{"ctrl-toolcall-content-empty", func() any { return new(ToolCall) }, `{"toolCallId":"t","title":"x","content":[],"locations":[]}`, `{"toolCallId":"t","title":"x"}`},
		{"ctrl-sessioninfo-dirs-empty", func() any { return new(SessionInfo) }, `{"sessionId":"s","cwd":"/w","additionalDirectories":[]}`, `{"sessionId":"s","cwd":"/w"}`},
		{"ctrl-authstatus-accounts-empty", func() any { return new(UnstableAuthStatusResponse) }, `{"state":"unauthenticated","accounts":[]}`, `{"state":"unauthenticated"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := tc.value()
			if err := json.Unmarshal([]byte(tc.in), v); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.in, err)
			}
			got, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if ok, g, w := equalJSON(got, []byte(tc.want)); !ok {
				t.Errorf("encoded %s as %s, want %s", tc.in, g, w)
			}
		})
	}
}

// TestLoadSessionResponse_MarshalJSON_KeepsEmptyConfigOptions checks that an
// empty list that a caller sets encodes as [], and that a nil one is left out.
func TestLoadSessionResponse_MarshalJSON_KeepsEmptyConfigOptions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"empty list", LoadSessionResponse{ConfigOptions: []SessionConfigOption{}}, `{"configOptions":[]}`},
		{"nil list", LoadSessionResponse{}, `{}`},
		{"empty map", ElicitationAcceptAction{Content: map[string]ElicitationContentValue{}}, `{"content":{}}`},
		{"nil map", ElicitationAcceptAction{}, `{}`},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		if ok, g, w := equalJSON(got, []byte(tc.want)); !ok {
			t.Errorf("%s: encoded as %s, want %s", tc.name, g, w)
		}
	}
}
