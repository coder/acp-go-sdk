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
			})
			checkInbound(t, peer, handled, tt, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":1}}`)
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
