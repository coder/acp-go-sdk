package acp

import (
	"encoding/json"
	"testing"
)

func TestUnmarshalJSON_HonorsDeserializeHints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		into func() any
		in   string
		want string
	}{
		{
			name: "tool call update with optional values of the wrong shape",
			into: func() any { return new(ToolCallUpdate) },
			in:   `{"toolCallId":"t","title":"run","kind":7,"status":{"x":1},"content":"oops","locations":5}`,
			want: `{"toolCallId":"t","title":"run"}`,
		},
		{
			name: "tool call update with a kind and a status that the schema does not define",
			into: func() any { return new(ToolCallUpdate) },
			in:   `{"toolCallId":"t","kind":"teleport","status":"paused"}`,
			want: `{"toolCallId":"t","kind":"other"}`,
		},
		{
			name: "tool call update inside a session notification",
			into: func() any { return new(SessionNotification) },
			in:   `{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"t","kind":"teleport","locations":[{"path":"/a"},"oops"]}}`,
			want: `{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"t","kind":"other","locations":[{"path":"/a"}]}}`,
		},
		{
			name: "plan with one entry of the wrong shape",
			into: func() any { return new(Plan) },
			in:   `{"entries":[{"content":"a","priority":"high","status":"pending"},"oops",null]}`,
			want: `{"entries":[{"content":"a","priority":"high","status":"pending"}]}`,
		},
		{
			name: "text content block with annotations of the wrong shape",
			into: func() any { return new(ContentBlock) },
			in:   `{"type":"text","text":"hi","annotations":"oops"}`,
			want: `{"type":"text","text":"hi"}`,
		},
		{
			name: "annotations with an audience that the schema does not define",
			into: func() any { return new(Annotations) },
			in:   `{"audience":["user","robot"],"priority":1}`,
			want: `{"audience":["user"],"priority":1}`,
		},
		{
			name: "session list with one session of the wrong shape",
			into: func() any { return new(ListSessionsResponse) },
			in:   `{"sessions":[{"sessionId":"s","cwd":"/work","title":7},"oops"]}`,
			want: `{"sessions":[{"sessionId":"s","cwd":"/work"}]}`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := tt.into()
			if err := json.Unmarshal([]byte(tt.in), v); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.in, err)
			}
			got, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if ok, got, want := equalJSON(got, []byte(tt.want)); !ok {
				t.Fatalf("%s decoded to\n got: %s\nwant: %s", tt.in, got, want)
			}
		})
	}
}

func TestInitializeResponse_UnmarshalJSON_BadAuthMethodsReadAsDefault(t *testing.T) {
	t.Parallel()
	var resp InitializeResponse
	if err := json.Unmarshal([]byte(`{"protocolVersion":1,"agentInfo":"oops","authMethods":"oops"}`), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.AgentInfo != nil {
		t.Errorf("agentInfo = %+v, want nil for a value of the wrong shape", resp.AgentInfo)
	}
	if resp.AuthMethods == nil || len(resp.AuthMethods) != 0 {
		t.Errorf("authMethods = %#v, want the default empty array", resp.AuthMethods)
	}

	if err := json.Unmarshal([]byte(`{"protocolVersion":1,"authMethods":[{"id":"a","name":"A"},7]}`), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.AuthMethods) != 1 {
		t.Fatalf("authMethods has %d items, want the one valid item", len(resp.AuthMethods))
	}
}

func TestToolCallUpdate_UnmarshalJSON_KeepsFieldsThatTheJSONOmits(t *testing.T) {
	t.Parallel()
	var update ToolCallUpdate
	for _, in := range []string{
		`{"toolCallId":"t","title":"run","status":"pending"}`,
		`{"toolCallId":"t","status":"completed"}`,
	} {
		if err := json.Unmarshal([]byte(in), &update); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
	}
	if update.Title == nil || *update.Title != "run" {
		t.Errorf("title = %v after an update without one, want it kept as %q", update.Title, "run")
	}
	if update.Status == nil || *update.Status != ToolCallStatusCompleted {
		t.Errorf("status = %v, want %q from the second update", update.Status, ToolCallStatusCompleted)
	}
}

func TestToolKind_UnmarshalJSON_ReadsAnUnknownKindAsOther(t *testing.T) {
	t.Parallel()
	var call ToolCall
	if err := json.Unmarshal([]byte(`{"toolCallId":"t","title":"x","kind":"teleport"}`), &call); err != nil {
		t.Fatalf("unmarshal a tool call with an unknown kind: %v", err)
	}
	if call.Kind != ToolKindOther {
		t.Errorf("kind = %q, want %q for a kind that the schema does not list", call.Kind, ToolKindOther)
	}
	if err := json.Unmarshal([]byte(`{"toolCallId":"t","title":"x","kind":"edit"}`), &call); err != nil || call.Kind != ToolKindEdit {
		t.Errorf("kind = %q (error %v), want the listed kind %q kept", call.Kind, err, ToolKindEdit)
	}
	var kind ToolKind
	if err := json.Unmarshal([]byte(`7`), &kind); err == nil {
		t.Errorf("a number decoded as the kind %q without an error", kind)
	}
}

func TestUnmarshalJSON_StaysStrictWithoutDeserializeHints(t *testing.T) {
	t.Parallel()
	var update ToolCallUpdate
	if err := json.Unmarshal([]byte(`{"toolCallId":5}`), &update); err == nil {
		t.Errorf("a number as toolCallId decoded without an error")
	}
	var entry PlanEntry
	if err := json.Unmarshal([]byte(`{"content":"a","priority":"urgent","status":"pending"}`), &entry); err != nil {
		t.Fatalf("unmarshal a plan entry with an unknown priority: %v", err)
	}
	if entry.Priority != "urgent" {
		t.Errorf("priority = %q, want the unknown value %q kept", entry.Priority, "urgent")
	}
}

// Schema 1.25.0 drops the default-on-error hints from properties that a
// receiver acts on, such as a terminal's command line or a session's MCP
// servers, so a malformed value fails the decode instead of reading as unset.
func TestUnmarshalJSON_RejectsMalformedValuesThatReceiversActOn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		into func() any
		in   string
	}{
		{"terminal args", func() any { return new(CreateTerminalRequest) }, `{"sessionId":"s","command":"ls","args":"-la"}`},
		{"terminal cwd", func() any { return new(CreateTerminalRequest) }, `{"sessionId":"s","command":"ls","cwd":5}`},
		{"terminal env", func() any { return new(CreateTerminalRequest) }, `{"sessionId":"s","command":"ls","env":{"A":"1"}}`},
		{"terminal output byte limit", func() any { return new(CreateTerminalRequest) }, `{"sessionId":"s","command":"ls","outputByteLimit":"1kb"}`},
		{"read line", func() any { return new(ReadTextFileRequest) }, `{"sessionId":"s","path":"/a","line":"10"}`},
		{"read limit", func() any { return new(ReadTextFileRequest) }, `{"sessionId":"s","path":"/a","limit":"all"}`},
		{"new session mcp servers", func() any { return new(NewSessionRequest) }, `{"cwd":"/w","mcpServers":"none"}`},
		{"new session additional directories", func() any { return new(NewSessionRequest) }, `{"cwd":"/w","mcpServers":[],"additionalDirectories":"/x"}`},
		{"load session mcp servers", func() any { return new(LoadSessionRequest) }, `{"sessionId":"s","cwd":"/w","mcpServers":{}}`},
		{"resume session additional directories", func() any { return new(ResumeSessionRequest) }, `{"sessionId":"s","cwd":"/w","additionalDirectories":[5]}`},
		{"terminal auth args", func() any { return new(AuthMethodTerminal) }, `{"id":"login","name":"Log in","args":"--login"}`},
		{"terminal auth env", func() any { return new(AuthMethodTerminal) }, `{"id":"login","name":"Log in","env":[]}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := tt.into()
			if err := json.Unmarshal([]byte(tt.in), v); err == nil {
				got, _ := json.Marshal(v)
				t.Errorf("%s decoded without an error as %s", tt.in, got)
			}
		})
	}
}
