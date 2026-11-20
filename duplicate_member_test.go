package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

const chunkUpdate = `"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`

// A struct property that repeats fails the decode, as the Rust SDK fails with
// "duplicate field". The error names the struct, the property and the type of
// its field, as a missing member does.
func TestUnmarshalJSON_RejectsARepeatedProperty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
		into           any
	}{
		{
			name: "known field", into: new(SessionNotification),
			in:   `{"sessionId":"a","sessionId":"b",` + chunkUpdate + `}`,
			want: "json: cannot unmarshal duplicate member into Go struct field SessionNotification.sessionId of type acp.SessionId",
		},
		{
			name: "_meta itself", into: new(SessionNotification),
			in:   `{"_meta":{"k":1},"_meta":{"j":2},"sessionId":"a",` + chunkUpdate + `}`,
			want: "json: cannot unmarshal duplicate member into Go struct field SessionNotification._meta of type acp.Meta",
		},
		{
			name: "a key with an escape repeats its name", into: new(SessionNotification),
			in:   `{"sessionId":"a","session\u0049d":"b",` + chunkUpdate + `}`,
			want: "json: cannot unmarshal duplicate member into Go struct field SessionNotification.sessionId of type acp.SessionId",
		},
		{
			name: "null and then a value", into: new(SessionNotification),
			in:   `{"sessionId":null,"sessionId":"a",` + chunkUpdate + `}`,
			want: "json: cannot unmarshal duplicate member into Go struct field SessionNotification.sessionId of type acp.SessionId",
		},
		{
			name: "request envelope id", into: new(ClientRequest),
			in:   `{"id":0,"id":1,"method":"initialize","params":{"protocolVersion":1}}`,
			want: "json: cannot unmarshal duplicate member into Go struct field ClientRequest.id of type acp.RequestId",
		},
		{
			name: "notification envelope method", into: new(ClientNotification),
			in:   `{"method":"session/cancel","method":"session/cancel","params":{"sessionId":"s"}}`,
			want: "json: cannot unmarshal duplicate member into Go struct field ClientNotification.method of type string",
		},
		{
			name: "lenient property itself", into: new(InitializeResponse),
			in:   `{"protocolVersion":1,"authMethods":[],"authMethods":[]}`,
			want: "json: cannot unmarshal duplicate member into Go struct field InitializeResponse.authMethods of type []acp.AuthMethod",
		},
		{
			name: "struct without another check", into: new(ElicitationAcceptAction),
			in:   `{"content":{},"content":{"a":"x"}}`,
			want: "json: cannot unmarshal duplicate member into Go struct field ElicitationAcceptAction.content of type map[string]acp.ElicitationContentValue",
		},
		{
			name: "named property of a struct with additional properties", into: new(UnstableMcpError),
			in:   `{"code":1,"code":2,"message":"m"}`,
			want: "json: cannot unmarshal duplicate member into Go struct field UnstableMcpError.code of type int",
		},
		{
			name: "nested struct", into: new(RequestPermissionRequest),
			in:   `{"sessionId":"s","toolCall":{"toolCallId":"t","toolCallId":"u"},"options":[]}`,
			want: "json: cannot unmarshal duplicate member into Go struct field RequestPermissionRequest.toolCall.toolCallId of type acp.ToolCallId",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := json.Unmarshal([]byte(tt.in), tt.into)
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) || typeErr.Value != "duplicate member" || err.Error() != tt.want {
				t.Errorf("unmarshal %s: error %v, want %s", tt.in, err, tt.want)
			}
		})
	}
}

// A union whose variant holds a repeated property, its discriminator
// included, selects no variant, as in the Rust SDK.
func TestUnion_UnmarshalJSON_RejectsARepeatedProperty(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, in string
		into     any
	}{
		{"discriminator with the same value", `{"type":"text","type":"text","text":"hi"}`, new(ContentBlock)},
		{"discriminator with another value", `{"type":"text","type":"image","text":"hi","data":"eA==","mimeType":"image/png"}`, new(ContentBlock)},
		{"discriminator with an escape", `{"type":"text","t\u0079pe":"text","text":"hi"}`, new(ContentBlock)},
		{"variant field", `{"type":"text","text":"a","text":"b"}`, new(ContentBlock)},
		{"_meta of a variant", `{"type":"text","text":"hi","_meta":{},"_meta":{}}`, new(ContentBlock)},
		{"item of an array", `{"sessionId":"s","prompt":[{"type":"text","text":"a","text":"b"}]}`, new(PromptRequest)},
		{"discriminator of a session update", `{"sessionId":"a","update":{"sessionUpdate":"agent_message_chunk","sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}`, new(SessionNotification)},
		{"patch field", `{"sessionId":"a","update":{"sessionUpdate":"tool_call_update","toolCallId":"t","title":"x","title":"y"}}`, new(SessionNotification)},
		{"untagged variant", `{"name":"x","command":"c","args":[],"env":[],"name":"y"}`, new(McpServer)},
		{"field of the union itself", `{"mode":"form","message":"a","message":"b","sessionId":"s","requestedSchema":{"type":"object","properties":{}}}`, new(CreateElicitationRequest)},
		{"field of a scope variant", `{"mode":"form","message":"a","sessionId":"s","sessionId":"t","requestedSchema":{"type":"object","properties":{}}}`, new(CreateElicitationRequest)},
		{"field of a catch-all", `{"action":"custom","_meta":{},"_meta":{}}`, new(CreateElicitationResponse)},
		{"field of the parent of a catch-all", `{"stopReason":"custom","usage":null,"usage":null}`, new(IdleStateUpdate)},
	} {
		if err := json.Unmarshal([]byte(tt.in), tt.into); err == nil {
			t.Errorf("%s: unmarshal %s gives %+v, want an error", tt.name, tt.in, tt.into)
		}
	}
}

// A member that is no property of the struct may repeat, as may a key of a
// map, and the last value wins, as in the Rust SDK. So may the discriminator
// of a catch-all variant, which the Rust SDK reads into a map.
func TestUnmarshalJSON_KeepsTheLastOfARepeatedMemberThatIsNoProperty(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, in, out string
		into          any
	}{
		{
			"unknown member", `{"sessionId":"a","x":1,"x":2,` + chunkUpdate + `}`,
			`{"sessionId":"a",` + chunkUpdate + `}`, new(SessionNotification),
		},
		{
			"key of _meta", `{"_meta":{"k":1,"k":2},"sessionId":"a",` + chunkUpdate + `}`,
			`{"_meta":{"k":2},"sessionId":"a",` + chunkUpdate + `}`, new(SessionNotification),
		},
		{
			// encoding/json reads a key in another case into the field, and
			// the Rust SDK skips it as unknown. Neither is a repeat.
			"key in another case", `{"sessionId":"a","SessionId":"b",` + chunkUpdate + `}`,
			`{"sessionId":"b",` + chunkUpdate + `}`, new(SessionNotification),
		},
		{"key of a typed map", `{"action":"accept","content":{"a":"x","a":"y"}}`, `{"action":"accept","content":{"a":"y"}}`, new(CreateElicitationResponse)},
		{"member of a struct without properties", `{"x":1,"x":2}`, `{}`, new(NoticeCapabilities)},
		{"additional property", `{"code":1,"message":"m","x":1,"x":2}`, `{"code":1,"message":"m","x":2}`, new(UnstableMcpError)},
		{"additional property of a catch-all", `{"state":"custom","x":1,"x":2}`, `{"state":"custom","x":2}`, new(StateUpdate)},
		{"discriminator of a catch-all", `{"state":"custom","state":"other"}`, `{"state":"other"}`, new(StateUpdate)},
		{"discriminator of a nested catch-all", `{"stopReason":"custom","stopReason":"other"}`, `{"stopReason":"other"}`, new(IdleStateUpdate)},
		{"action of a catch-all", `{"action":"accept","action":"custom"}`, `{"action":"custom"}`, new(CreateElicitationResponse)},
	} {
		if err := json.Unmarshal([]byte(tt.in), tt.into); err != nil {
			t.Errorf("%s: unmarshal %s: %v", tt.name, tt.in, err)
			continue
		}
		got, err := json.Marshal(tt.into)
		if err != nil {
			t.Fatal(err)
		}
		if !jsonEqual(t, got, []byte(tt.out)) {
			t.Errorf("%s: unmarshal %s reads %s, want %s", tt.name, tt.in, got, tt.out)
		}
	}
}

// Inside a property with x-deserialize-default-on-error or
// x-deserialize-skip-invalid-items, a value with a repeated property reads as
// the default, or its item is dropped, as in the Rust SDK.
func TestUnmarshalJSON_LenientPropertyDropsAValueWithARepeatedProperty(t *testing.T) {
	t.Parallel()
	var req InitializeRequest
	if err := json.Unmarshal([]byte(`{"protocolVersion":1,"clientInfo":{"name":"a","name":"b","version":"1"}}`), &req); err != nil || req.ClientInfo != nil {
		t.Errorf("clientInfo with a repeated name: %+v, error %v, want no clientInfo", req.ClientInfo, err)
	}
	var n SessionNotification
	in := `{"sessionId":"a","update":{"sessionUpdate":"tool_call_update","toolCallId":"t","locations":[{"path":"/a","path":"/b"}],` +
		`"content":[{"type":"content","content":{"type":"text","text":"a","text":"b"}},{"type":"content","content":{"type":"text","text":"ok"}}]}}`
	if err := json.Unmarshal([]byte(in), &n); err != nil {
		t.Fatalf("unmarshal %s: %v", in, err)
	}
	u := n.Update.ToolCallUpdate
	if u == nil {
		t.Fatalf("unmarshal %s reads %+v, want a tool call update", in, n.Update)
	}
	locations, _ := u.Locations.Value()
	content, _ := u.Content.Value()
	if len(locations) != 0 || len(content) != 1 || content[0].Content == nil || content[0].Content.Content.Text == nil || content[0].Content.Content.Text.Text != "ok" {
		t.Errorf("unmarshal %s reads locations %+v and content %+v, want no location and only the content ok", in, locations, content)
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

// A request with a repeated property gets -32602 and its handler does not
// run; a notification with one is dropped.
func TestAgentSideConnection_RejectsRepeatedProperties(t *testing.T) {
	t.Parallel()
	tests := []inboundCase{
		{name: "valid prompt", method: AgentMethodSessionPrompt, params: `{"sessionId":"s",` + promptText + `}`, valid: true},
		{name: "prompt with sessionId twice", method: AgentMethodSessionPrompt, params: `{"sessionId":"s","sessionId":"s",` + promptText + `}`},
		{name: "prompt with a text block whose type repeats", method: AgentMethodSessionPrompt, params: `{"sessionId":"s","prompt":[{"type":"text","type":"text","text":"hi"}]}`},
		{name: "initialize with protocolVersion twice", method: AgentMethodInitialize, params: `{"protocolVersion":1,"protocolVersion":1}`},
		{name: "valid prompt with an unknown member twice", method: AgentMethodSessionPrompt, params: `{"sessionId":"s","x":1,"x":2,` + promptText + `}`, valid: true},
		{name: "cancel with sessionId twice", method: AgentMethodSessionCancel, params: `{"sessionId":"s","sessionId":"t"}`, notification: true},
		{name: "valid cancel", method: AgentMethodSessionCancel, params: `{"sessionId":"s"}`, notification: true, valid: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handled := make(chan string, 4)
			_, peer := newCancelPeer(t, agentFuncs{
				PromptFunc: func(context.Context, PromptRequest) (PromptResponse, error) {
					handled <- AgentMethodSessionPrompt
					return PromptResponse{StopReason: StopReasonEndTurn}, nil
				},
				CancelFunc: func(context.Context, CancelNotification) error {
					handled <- AgentMethodSessionCancel
					return nil
				},
				InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
					handled <- AgentMethodInitialize
					return InitializeResponse{}, nil
				},
			})
			checkInbound(t, peer, handled, tt, `{"jsonrpc":"2.0","id":2,"method":"authenticate","params":{"methodId":"m"}}`)
		})
	}
}

// The -32602 error of a request with a repeated property names it.
func TestAgentSideConnection_RepeatedPropertyErrorNamesIt(t *testing.T) {
	t.Parallel()
	_, peer := newCancelPeer(t, agentFuncs{})
	peer.send(`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s","sessionId":"t",` + promptText + `}}`)
	reply := peer.await("the reply to session/prompt", replyTo(1))
	const want = "json: cannot unmarshal duplicate member into Go struct field PromptRequest.sessionId of type acp.SessionId"
	if reply.Error == nil || reply.Error.Code != -32602 || !strings.Contains(string(reply.Error.Data), want) {
		t.Errorf("reply %+v, want -32602 with %q", reply.Error, want)
	}
}

// A notification with a repeated property is logged at Error, with the error
// that names it.
func TestAgentSideConnection_LogsANotificationWithARepeatedProperty(t *testing.T) {
	t.Parallel()
	logs := &syncBuffer{}
	peer := newRawPeer(t, func(peerInput io.Writer, peerOutput io.Reader) {
		conn := NewAgentSideConnection(agentFuncs{}, peerInput, peerOutput)
		conn.SetLogger(slog.New(slog.NewTextHandler(logs, nil)))
	})
	peer.send(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s","sessionId":"t"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"authenticate","params":{"methodId":"m"}}`)
	peer.await("the reply to the sync request", replyTo(2))
	got := logs.String()
	if !strings.Contains(got, "level=ERROR") || !strings.Contains(got, "failed to handle notification") ||
		!strings.Contains(got, "CancelNotification.sessionId") || !strings.Contains(got, "duplicate member") {
		t.Errorf("logs %q, want an error that names CancelNotification.sessionId as a duplicate member", got)
	}
}

func TestClientSideConnection_RejectsRepeatedProperties(t *testing.T) {
	t.Parallel()
	tests := []inboundCase{
		{name: "valid update", method: ClientMethodSessionUpdate, params: `{"sessionId":"s",` + chunkUpdate + `}`, notification: true, valid: true},
		{name: "update with update twice", method: ClientMethodSessionUpdate, params: `{"sessionId":"s",` + chunkUpdate + `,` + chunkUpdate + `}`, notification: true},
		{name: "update whose sessionUpdate repeats", method: ClientMethodSessionUpdate, params: `{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}`, notification: true},
		{name: "permission request whose toolCallId repeats", method: ClientMethodSessionRequestPermission, params: `{"sessionId":"s","toolCall":{"toolCallId":"t","toolCallId":"t"},"options":[]}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handled := make(chan string, 4)
			client := &clientFuncs{
				RequestPermissionFunc: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
					handled <- ClientMethodSessionRequestPermission
					return RequestPermissionResponse{Outcome: NewRequestPermissionOutcomeCancelled()}, nil
				},
				SessionUpdateFunc: func(context.Context, SessionNotification) error {
					handled <- ClientMethodSessionUpdate
					return nil
				},
			}
			peer := newRawPeer(t, func(peerInput io.Writer, peerOutput io.Reader) {
				conn := NewClientSideConnection(client, peerInput, peerOutput)
				conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
			})
			checkInbound(t, peer, handled, tt, `{"jsonrpc":"2.0","id":2,"method":"fs/read_text_file","params":{"sessionId":"s","path":"/p"}}`)
		})
	}
}

// A result with a repeated property fails the call with -32603, as a result
// that does not decode does.
func TestClientSideConnection_ResultWithARepeatedPropertyFails(t *testing.T) {
	t.Parallel()
	var conn *ClientSideConnection
	peer := newRawPeer(t, func(peerInput io.Writer, peerOutput io.Reader) {
		conn = NewClientSideConnection(&clientFuncs{}, peerInput, peerOutput)
		conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	done := make(chan error, 1)
	go func() {
		_, err := conn.NewSession(context.Background(), NewSessionRequest{Cwd: "/w", McpServers: []McpServer{}})
		done <- err
	}()
	req := peer.await("the session/new request", func(m anyMessage) bool { return m.Method == AgentMethodSessionNew })
	peer.send(`{"jsonrpc":"2.0","id":` + string(*req.ID) + `,"result":{"sessionId":"a","sessionId":"b"}}`)
	err := <-done
	var reqErr *RequestError
	const want = "json: cannot unmarshal duplicate member into Go struct field NewSessionResponse.sessionId of type acp.SessionId"
	if !errors.As(err, &reqErr) || reqErr.Code != -32603 || !strings.Contains(string(reqErr.Data), want) {
		t.Errorf("NewSession: error %v, want -32603 with %q", err, want)
	}
}
