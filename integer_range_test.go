package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// The Rust SDK reads an integer of the schema as the Rust type that its format
// names (u16, u32, u64), so a value out of that type's range fails the
// message. The SDK reads it as the Go type of the same range, so the value
// fails to decode: the client answers -32602 and calls no handler.
func TestClientSideConnection_RejectsIntegersOutOfRange(t *testing.T) {
	t.Parallel()
	read := func(members string) string { return `{"sessionId":"s","path":"/p",` + members + `}` }
	tests := []requestCase{
		{name: "read line -1", method: ClientMethodFsReadTextFile, params: read(`"line":-1`), invalid: true, want: "line of type uint32"},
		{name: "read line 2^32", method: ClientMethodFsReadTextFile, params: read(`"line":4294967296`), invalid: true, want: "line of type uint32"},
		{name: "read limit -1", method: ClientMethodFsReadTextFile, params: read(`"limit":-1`), invalid: true, want: "limit of type uint32"},
		{name: "terminal output byte limit -1", method: ClientMethodTerminalCreate, params: `{"sessionId":"s","command":"ls","outputByteLimit":-1}`, invalid: true, want: "outputByteLimit of type uint64"},

		{name: "read line 2^32-1", method: ClientMethodFsReadTextFile, params: read(`"line":4294967295,"limit":0`), check: func(t *testing.T, p any) {
			if req := p.(ReadTextFileRequest); req.Line == nil || *req.Line != 4294967295 || req.Limit == nil || *req.Limit != 0 {
				t.Errorf("line and limit = %v and %v, want 4294967295 and 0", req.Line, req.Limit)
			}
		}},
		{name: "terminal output byte limit 0", method: ClientMethodTerminalCreate, params: `{"sessionId":"s","command":"ls","outputByteLimit":0}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runRequestCase(t, tt, false, func(called chan<- any, w io.Writer, r io.Reader) {
				NewClientSideConnection(&clientFuncs{
					ReadTextFileFunc: func(_ context.Context, p ReadTextFileRequest) (ReadTextFileResponse, error) {
						called <- p
						return ReadTextFileResponse{}, nil
					},
					CreateTerminalFunc: func(_ context.Context, p CreateTerminalRequest) (CreateTerminalResponse, error) {
						called <- p
						return CreateTerminalResponse{TerminalId: "t"}, nil
					},
				}, w, r)
			})
		})
	}
}

// protocolVersion is a u16 in the Rust SDK.
func TestAgentSideConnection_RejectsIntegersOutOfRange(t *testing.T) {
	t.Parallel()
	tests := []requestCase{
		{name: "protocol version 70000", method: AgentMethodInitialize, params: `{"protocolVersion":70000}`, invalid: true, want: "protocolVersion of type acp.ProtocolVersion"},
		{name: "protocol version -1", method: AgentMethodInitialize, params: `{"protocolVersion":-1}`, invalid: true, want: "protocolVersion of type acp.ProtocolVersion"},

		{name: "protocol version 65535", method: AgentMethodInitialize, params: `{"protocolVersion":65535}`, check: func(t *testing.T, p any) {
			if got := p.(InitializeRequest).ProtocolVersion; got != 65535 {
				t.Errorf("protocolVersion = %d, want 65535", got)
			}
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// The request is the initialize itself.
			runRequestCase(t, tt, false, func(called chan<- any, w io.Writer, r io.Reader) {
				NewAgentSideConnection(agentFuncs{
					InitializeFunc: func(_ context.Context, p InitializeRequest) (InitializeResponse, error) {
						called <- p
						return InitializeResponse{ProtocolVersion: ProtocolVersionNumber}, nil
					},
				}, w, r)
			})
		})
	}
}

// firstUpdate writes each update as a session/update notification to a client
// and returns the first notification that reaches its handler.
func firstUpdate(t *testing.T, updates ...string) SessionNotification {
	t.Helper()
	inR, inW := io.Pipe()
	t.Cleanup(func() { _ = inW.Close() })
	got := make(chan SessionNotification, len(updates))
	NewClientSideConnection(&clientFuncs{SessionUpdateFunc: func(_ context.Context, n SessionNotification) error {
		got <- n
		return nil
	}}, io.Discard, inR)
	var lines string
	for _, u := range updates {
		lines += `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":` + u + `}}` + "\n"
	}
	go func() { _, _ = io.WriteString(inW, lines) }()
	select {
	case n := <-got:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("the handler got no update")
		return SessionNotification{}
	}
}

// used and size of a usage_update are u64 in the Rust SDK, so a negative one
// fails the notification, and the client drops it. The connection handles
// notifications in order, so the first one that the handler gets is the valid
// one after it.
func TestClientSideConnection_DropsAUsageUpdateOutOfRange(t *testing.T) {
	t.Parallel()
	const valid = `{"sessionUpdate":"usage_update","used":0,"size":1}`
	for _, bad := range []string{
		`{"sessionUpdate":"usage_update","used":-1,"size":1}`,
		`{"sessionUpdate":"usage_update","used":1,"size":-1}`,
	} {
		bad := bad
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			u := firstUpdate(t, bad, valid).Update.UsageUpdate
			if u == nil || u.Used != 0 || u.Size != 1 {
				t.Errorf("first update the handler got = %+v, want the valid one with used 0 and size 1", u)
			}
		})
	}
}

// used and size of a usage_update, and outputByteLimit of terminal/create, are
// u64 in the Rust SDK, which reads every value up to 2^64-1. So does the
// client: the handler gets the value, and no digit of it is lost.
func TestClientSideConnection_ReadsAUint64AboveMaxInt64(t *testing.T) {
	t.Parallel()
	const above = "9223372036854775808" // 2^63
	const top = "18446744073709551615"  // 2^64-1
	u := firstUpdate(t, `{"sessionUpdate":"usage_update","used":`+above+`,"size":`+top+`}`).Update.UsageUpdate
	if got, _ := json.Marshal(u); u == nil || string(got) != `{"sessionUpdate":"usage_update","size":`+top+`,"used":`+above+`}` {
		t.Errorf("usage_update reads as %s, want used %s and size %s", got, above, top)
	}
	runRequestCase(t, requestCase{
		method: ClientMethodTerminalCreate,
		params: `{"sessionId":"s","command":"ls","outputByteLimit":` + top + `}`,
		check: func(t *testing.T, p any) {
			if got, _ := json.Marshal(p.(CreateTerminalRequest).OutputByteLimit); string(got) != top {
				t.Errorf("outputByteLimit = %s, want %s", got, top)
			}
		},
	}, false, func(called chan<- any, w io.Writer, r io.Reader) {
		NewClientSideConnection(&clientFuncs{CreateTerminalFunc: func(_ context.Context, p CreateTerminalRequest) (CreateTerminalResponse, error) {
			called <- p
			return CreateTerminalResponse{TerminalId: "t"}, nil
		}}, w, r)
	})
}

// line of a tool call location has x-deserialize-default-on-error, so the Rust
// SDK reads a value out of the u32 range as absent and keeps the location.
func TestClientSideConnection_ReadsALocationLineOutOfRangeAsAbsent(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		line string
		want *uint32
	}{
		{"-1", nil},
		{"4294967296", nil},
		{"4294967295", Ptr[uint32](4294967295)},
		{"0", Ptr[uint32](0)},
	} {
		tt := tt
		t.Run(tt.line, func(t *testing.T) {
			t.Parallel()
			u := firstUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"t","title":"x","locations":[{"path":"/p","line":`+tt.line+`}]}`).Update
			if u.ToolCall == nil || len(u.ToolCall.Locations) != 1 || u.ToolCall.Locations[0].Path != "/p" {
				t.Fatalf("update = %+v, want a tool call with the location of /p", u)
			}
			if line := u.ToolCall.Locations[0].Line; (line == nil) != (tt.want == nil) || line != nil && *line != *tt.want {
				t.Errorf("line %s reads as %v, want %v", tt.line, line, tt.want)
			}
		})
	}
}

// exitCode has x-deserialize-default-on-error too, so a value out of the u32
// range reads as absent. The agent reads it in the result of
// terminal/wait_for_exit and terminal/output. usage of a prompt result has the
// hint as well, so a token count out of the u64 range there drops the usage.
func TestLenientIntegers_UnmarshalJSON_ReadOutOfRangeAsAbsent(t *testing.T) {
	t.Parallel()
	const usage = `"inputTokens":1,"outputTokens":1`
	tests := []struct {
		in     string
		into   any // a pointer to a new value
		absent func(v any) bool
	}{
		{`{"exitCode":-1}`, new(TerminalExitStatus), func(v any) bool { return v.(*TerminalExitStatus).ExitCode == nil }},
		{`{"exitCode":4294967296,"signal":"SIGKILL"}`, new(TerminalExitStatus), func(v any) bool {
			s := v.(*TerminalExitStatus)
			return s.ExitCode == nil && s.Signal != nil
		}},
		{`{"exitCode":-1}`, new(WaitForTerminalExitResponse), func(v any) bool { return v.(*WaitForTerminalExitResponse).ExitCode == nil }},
		{`{"exitCode":4294967296}`, new(WaitForTerminalExitResponse), func(v any) bool { return v.(*WaitForTerminalExitResponse).ExitCode == nil }},
		{`{"output":"o","truncated":false,"exitStatus":{"exitCode":-1}}`, new(TerminalOutputResponse), func(v any) bool {
			s := v.(*TerminalOutputResponse).ExitStatus
			return s != nil && s.ExitCode == nil
		}},
		{`{"stopReason":"end_turn","usage":{"totalTokens":-1,` + usage + `}}`, new(PromptResponse), func(v any) bool { return v.(*PromptResponse).Usage == nil }},
		{`{"stopReason":"end_turn","usage":{"totalTokens":2,"thoughtTokens":-1,` + usage + `}}`, new(PromptResponse), func(v any) bool {
			u := v.(*PromptResponse).Usage
			return u != nil && u.TotalTokens == 2 && u.ThoughtTokens == nil
		}},
	}
	for _, tt := range tests {
		if err := json.Unmarshal([]byte(tt.in), tt.into); err != nil {
			t.Fatalf("unmarshal %s: %v", tt.in, err)
		}
		if !tt.absent(tt.into) {
			got, _ := json.Marshal(tt.into)
			t.Errorf("%s decodes into %T as %s, want the value out of range absent", tt.in, tt.into, got)
		}
	}
}
