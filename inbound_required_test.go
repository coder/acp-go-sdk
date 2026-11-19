package acp

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
)

// inboundCase is one message that a peer sends. A valid message reaches its
// handler. Any other one does not, and a request gets -32602.
type inboundCase struct {
	name         string
	method       string
	params       string
	notification bool
	valid        bool
}

// checkInbound sends tt through peer, followed by syncLine, a valid request
// with the id 2 that no recording handler takes. The connection starts a
// request only after the notifications that came before it are done, so once
// syncLine has its reply, handled holds every handler that tt reached.
func checkInbound(t *testing.T, peer *cancelPeer, handled chan string, tt inboundCase, syncLine string) {
	t.Helper()
	if tt.notification {
		peer.send(`{"jsonrpc":"2.0","method":"` + tt.method + `","params":` + tt.params + `}`)
	} else {
		peer.send(`{"jsonrpc":"2.0","id":1,"method":"` + tt.method + `","params":` + tt.params + `}`)
		reply := peer.await("the reply to "+tt.method, replyTo(1))
		switch {
		case tt.valid && reply.Error != nil:
			t.Errorf("%s %s got error %+v, want a result", tt.method, tt.params, reply.Error)
		case !tt.valid && (reply.Error == nil || reply.Error.Code != -32602):
			t.Errorf("%s %s got result %s and error %+v, want error -32602", tt.method, tt.params, reply.Result, reply.Error)
		}
	}
	peer.send(syncLine)
	peer.await("the reply to the sync request", replyTo(2))
	close(handled)
	var got []string
	for method := range handled {
		got = append(got, method)
	}
	if tt.valid && (len(got) != 1 || got[0] != tt.method) {
		t.Errorf("%s %s reached handlers %v, want only %s", tt.method, tt.params, got, tt.method)
	}
	if !tt.valid && len(got) != 0 {
		t.Errorf("%s %s reached handlers %v, want none", tt.method, tt.params, got)
	}
}

func replyTo(id int) func(anyMessage) bool {
	return func(m anyMessage) bool { return m.ID != nil && string(*m.ID) == strconv.Itoa(id) }
}

const promptText = `"prompt":[{"type":"text","text":"hi"}]`

func TestAgentSideConnection_RejectsMissingRequiredMembers(t *testing.T) {
	t.Parallel()
	tests := []inboundCase{
		{name: "valid prompt", method: AgentMethodSessionPrompt, params: `{"sessionId":"s",` + promptText + `}`, valid: true},
		{name: "prompt without sessionId", method: AgentMethodSessionPrompt, params: `{` + promptText + `}`},
		{name: "prompt with empty sessionId", method: AgentMethodSessionPrompt, params: `{"sessionId":"",` + promptText + `}`},
		{
			name: "valid new session", method: AgentMethodSessionNew, valid: true,
			params: `{"cwd":"/w","mcpServers":[{"type":"http","name":"h","url":"https://h","headers":[]},{"type":"acp","name":"a","serverId":"srv"}]}`,
		},
		{name: "acp MCP server without serverId", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[{"type":"acp","name":"a"}]}`},
		{name: "http MCP server without headers", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[{"type":"http","name":"h","url":"https://h"}]}`},
		{name: "valid cancel", method: AgentMethodSessionCancel, params: `{"sessionId":"s"}`, notification: true, valid: true},
		{name: "cancel without sessionId", method: AgentMethodSessionCancel, params: `{}`, notification: true},

		// A member that holds its Go zero value when it is absent or null,
		// and that Validate cannot tell from a present zero value: a plain
		// string below the message, an integer, or a member set to null.
		{name: "http MCP server without url", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[{"type":"http","name":"h","headers":[]}]}`},
		{name: "valid sse MCP server", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[{"type":"sse","name":"s","url":"https://s","headers":[]}]}`, valid: true},
		{name: "sse MCP server without url", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[{"type":"sse","name":"s","headers":[]}]}`},
		{name: "valid stdio env variable with an empty value", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[{"name":"m","command":"c","args":[],"env":[{"name":"N","value":""}]}]}`, valid: true},
		{name: "stdio env variable without name", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[{"name":"m","command":"c","args":[],"env":[{"value":"v"}]}]}`},
		{name: "stdio env variable with a null name", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[{"name":"m","command":"c","args":[],"env":[{"name":null,"value":"v"}]}]}`},
		{name: "new session with a null cwd", method: AgentMethodSessionNew, params: `{"cwd":null,"mcpServers":[]}`},
		{name: "valid prompt with an empty text", method: AgentMethodSessionPrompt, params: `{"sessionId":"s","prompt":[{"type":"text","text":""}]}`, valid: true},
		{name: "prompt text block without text", method: AgentMethodSessionPrompt, params: `{"sessionId":"s","prompt":[{"type":"text"}]}`},
		{name: "valid initialize with protocolVersion 0", method: AgentMethodInitialize, params: `{"protocolVersion":0}`, valid: true},
		{name: "initialize without protocolVersion", method: AgentMethodInitialize, params: `{}`},
		{name: "initialize with a null protocolVersion", method: AgentMethodInitialize, params: `{"protocolVersion":null}`},
		{name: "valid boolean config option set to false", method: AgentMethodSessionSetConfigOption, params: `{"sessionId":"s","configId":"c","type":"boolean","value":false}`, valid: true},
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
				NewSessionFunc: func(context.Context, NewSessionRequest) (NewSessionResponse, error) {
					handled <- AgentMethodSessionNew
					return NewSessionResponse{SessionId: "s"}, nil
				},
				CancelFunc: func(context.Context, CancelNotification) error {
					handled <- AgentMethodSessionCancel
					return nil
				},
				InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
					handled <- AgentMethodInitialize
					return InitializeResponse{}, nil
				},
				SetSessionConfigOptionFunc: func(context.Context, SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error) {
					handled <- AgentMethodSessionSetConfigOption
					return SetSessionConfigOptionResponse{}, nil
				},
			})
			checkInbound(t, peer, handled, tt, `{"jsonrpc":"2.0","id":2,"method":"authenticate","params":{"methodId":"m"}}`)
		})
	}
}

func TestClientSideConnection_RejectsMissingRequiredMembers(t *testing.T) {
	t.Parallel()
	const update = `"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}`
	tests := []inboundCase{
		{name: "valid permission request", method: ClientMethodSessionRequestPermission, params: `{"sessionId":"s","toolCall":{"toolCallId":"t"},"options":[]}`, valid: true},
		{name: "permission request without toolCall", method: ClientMethodSessionRequestPermission, params: `{"sessionId":"s","options":[]}`},
		{name: "valid update", method: ClientMethodSessionUpdate, params: `{"sessionId":"s",` + update + `}`, notification: true, valid: true},
		{name: "update without update", method: ClientMethodSessionUpdate, params: `{"sessionId":"s"}`, notification: true},

		// Members that hold their Go zero value when absent, as in the
		// agent test above.
		{name: "valid form elicitation", method: ClientMethodElicitationCreate, params: `{"mode":"form","message":"m","sessionId":"s","requestedSchema":{"type":"object","properties":{}}}`, valid: true},
		{name: "form elicitation without requestedSchema", method: ClientMethodElicitationCreate, params: `{"mode":"form","message":"m","sessionId":"s"}`},
		{name: "valid url elicitation", method: ClientMethodElicitationCreate, params: `{"mode":"url","message":"m","sessionId":"s","elicitationId":"e","url":"https://x"}`, valid: true},
		{name: "url elicitation without url", method: ClientMethodElicitationCreate, params: `{"mode":"url","message":"m","sessionId":"s","elicitationId":"e"}`},
		{name: "valid tool call with an empty title", method: ClientMethodSessionUpdate, params: `{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"t","title":""}}`, notification: true, valid: true},
		{name: "tool call without title", method: ClientMethodSessionUpdate, params: `{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"t"}}`, notification: true},
		{name: "valid usage update with used 0", method: ClientMethodSessionUpdate, params: `{"sessionId":"s","update":{"sessionUpdate":"usage_update","used":0,"size":0}}`, notification: true, valid: true},
		{name: "valid permission option with an empty name", method: ClientMethodSessionRequestPermission, params: `{"sessionId":"s","toolCall":{"toolCallId":"t"},"options":[{"optionId":"o","name":"","kind":"allow_once"}]}`, valid: true},
		{name: "permission option without name", method: ClientMethodSessionRequestPermission, params: `{"sessionId":"s","toolCall":{"toolCallId":"t"},"options":[{"optionId":"o","kind":"allow_once"}]}`},
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
				CreateElicitationFunc: func(context.Context, CreateElicitationRequest) (CreateElicitationResponse, error) {
					handled <- ClientMethodElicitationCreate
					return CreateElicitationResponse{Cancel: &CreateElicitationCancel{}}, nil
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
