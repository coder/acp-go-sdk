package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// newProxyPeer connects a proxy with handlers to a fake conductor that
// writes raw lines and reads every message the proxy sends.
func newProxyPeer(t *testing.T, handlers func(*UnstableProxySideConnection) UnstableProxyHandlers) *cancelPeer {
	t.Helper()
	return newRawPeer(t, func(peerInput io.Writer, peerOutput io.Reader) {
		c := NewUnstableProxySideConnection(handlers, peerInput, peerOutput)
		c.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
}

// next returns the next message that the proxy sends.
func (p *cancelPeer) next() anyMessage {
	p.t.Helper()
	return p.await("the next message", func(anyMessage) bool { return true })
}

// replyLine answers the request m with result, which is a JSON value or, when
// it starts with "error:", the JSON of an error object.
func replyLine(m anyMessage, result string) string {
	if errJSON, ok := strings.CutPrefix(result, "error:"); ok {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":%s}`, *m.ID, errJSON)
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, *m.ID, result)
}

func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return b.String()
}

// wantMessage checks that m is a request (request) or a notification of
// method whose params are params, or that it has none when params is "".
// When wrapped, m must be a proxy/successor envelope of such a message.
func wantMessage(t *testing.T, m anyMessage, wrapped, request bool, method, params string) {
	t.Helper()
	if request != (m.ID != nil) {
		t.Fatalf("message %+v: request = %v, want %v", m, m.ID != nil, request)
	}
	gotMethod, gotParams := m.Method, m.Params
	if wrapped {
		if m.Method != ProxyMethodProxySuccessor {
			t.Fatalf("method = %q, want %q", m.Method, ProxyMethodProxySuccessor)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(m.Params, &envelope); err != nil {
			t.Fatalf("envelope %s: %v", m.Params, err)
		}
		if err := json.Unmarshal(envelope["method"], &gotMethod); err != nil {
			t.Fatalf("envelope method %s: %v", envelope["method"], err)
		}
		gotParams = envelope["params"]
		if _, ok := envelope["_meta"]; ok {
			t.Errorf("envelope %s has a _meta", m.Params)
		}
	}
	if gotMethod != method {
		t.Errorf("forwarded method = %q, want %q", gotMethod, method)
	}
	switch {
	case params == "" && gotParams != nil:
		t.Errorf("forwarded params = %s, want none", gotParams)
	case params != "" && (gotParams == nil || compactJSON(t, gotParams) != params):
		t.Errorf("forwarded params = %s, want %s", gotParams, params)
	}
}

// wantAnswer checks that m answers the request id with result, or with the
// error object when result starts with "error:", byte for byte.
func wantAnswer(t *testing.T, m anyMessage, id, result string) {
	t.Helper()
	if m.ID == nil || string(*m.ID) != id {
		t.Fatalf("message %+v does not answer id %s", m, id)
	}
	got := string(m.Result)
	if m.Error != nil {
		b, err := json.Marshal(m.Error)
		if err != nil {
			t.Fatal(err)
		}
		got = "error:" + string(b)
	}
	if got != result {
		t.Errorf("answer to %s = %s, want %s", id, got, result)
	}
}

// The conductor's own wire test (Rust SDK, agent-client-protocol-conductor
// tests/proxy_wire.rs): proxy/initialize goes on as initialize, a client
// request leaves wrapped, and a successor request and notification leave
// plain, the request with the proxy's own id.
func TestUnstableProxySideConnection_RelaysAChain(t *testing.T) {
	p := newProxyPeer(t, nil)

	const initParams = `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true}},"futureField":{"a":1}}`
	p.send(`{"jsonrpc":"2.0","id":0,"method":"proxy/initialize","params":` + initParams + `}`)
	out := p.next()
	wantMessage(t, out, true, true, "initialize", initParams)
	const initResult = `{"protocolVersion":1,"agentCapabilities":{"loadSession":true},"authMethods":[],"extra":"kept"}`
	p.send(replyLine(out, initResult))
	wantAnswer(t, p.next(), "0", initResult)

	p.send(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/w","mcpServers":[]}}`)
	out = p.next()
	wantMessage(t, out, true, true, "session/new", `{"cwd":"/w","mcpServers":[]}`)
	p.send(replyLine(out, `{"sessionId":"s"}`))
	wantAnswer(t, p.next(), "1", `{"sessionId":"s"}`)

	p.send(`{"jsonrpc":"2.0","id":"c1","method":"proxy/successor","params":{"method":"fs/read_text_file","params":{"sessionId":"s","path":"/a"}}}`)
	out = p.next()
	wantMessage(t, out, false, true, "fs/read_text_file", `{"sessionId":"s","path":"/a"}`)
	if string(*out.ID) == `"c1"` {
		t.Errorf("the proxy reused the conductor's id %s", *out.ID)
	}
	p.send(replyLine(out, `{"content":"text"}`))
	wantAnswer(t, p.next(), `"c1"`, `{"content":"text"}`)

	const update = `{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}`
	p.send(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"session/update","params":` + update + `}}`)
	wantMessage(t, p.next(), false, false, "session/update", update)
}

// A message without a handler of its kind goes on unchanged, in the kind it
// arrived: from the client side wrapped to the successor, from the
// successor plain to the client. The result of a request comes back
// unchanged.
func TestUnstableProxySideConnection_ForwardsUnhandledMessages(t *testing.T) {
	tests := []struct {
		name     string
		handlers UnstableProxyHandlers
		in       string // a message from the conductor; a request has id 1
		// The message that the proxy sends on: to the successor when
		// wrapped, else to the client. params is "" when it has none.
		wrapped, request bool
		method, params   string
	}{
		{
			name:    "client request, unknown members kept",
			in:      `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/w","mcpServers":[],"future":{"n":2.50}}}`,
			wrapped: true, request: true, method: "session/new", params: `{"cwd":"/w","mcpServers":[],"future":{"n":2.50}}`,
		},
		{
			name:    "client notification",
			in:      `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}`,
			wrapped: true, method: "session/cancel", params: `{"sessionId":"s"}`,
		},
		{
			name:    "client extension request",
			in:      `{"jsonrpc":"2.0","id":1,"method":"_x/echo","params":{"a":1}}`,
			wrapped: true, request: true, method: "_x/echo", params: `{"a":1}`,
		},
		{
			name:    "client request of an unknown method",
			in:      `{"jsonrpc":"2.0","id":1,"method":"x/unknown","params":[1,2]}`,
			wrapped: true, request: true, method: "x/unknown", params: `[1,2]`,
		},
		{
			name:    "client request without params",
			in:      `{"jsonrpc":"2.0","id":1,"method":"_x/ask"}`,
			wrapped: true, request: true, method: "_x/ask",
		},
		{
			name:    "client notification with null params",
			in:      `{"jsonrpc":"2.0","method":"_x/ping","params":null}`,
			wrapped: true, method: "_x/ping",
		},
		{
			name: "a request of a notification method skips its handler",
			handlers: UnstableProxyHandlers{FromClient: UnstableProxyFromClient{
				Cancel: func(context.Context, CancelNotification) error {
					t.Error("Cancel ran for a request")
					return nil
				},
			}},
			in:      `{"jsonrpc":"2.0","id":1,"method":"session/cancel","params":{"sessionId":"s"}}`,
			wrapped: true, request: true, method: "session/cancel", params: `{"sessionId":"s"}`,
		},
		{
			name: "proxy/initialize as a notification goes on as initialize",
			handlers: UnstableProxyHandlers{FromClient: UnstableProxyFromClient{
				UnstableInitializeProxy: func(context.Context, UnstableInitializeProxyRequest) (UnstableInitializeProxyResponse, error) {
					t.Error("UnstableInitializeProxy ran for a notification")
					return UnstableInitializeProxyResponse{}, nil
				},
			}},
			in:      `{"jsonrpc":"2.0","method":"proxy/initialize","params":{"protocolVersion":1}}`,
			wrapped: true, method: "initialize", params: `{"protocolVersion":1}`,
		},
		{
			name:    "successor request, unknown members kept",
			in:      `{"jsonrpc":"2.0","id":1,"method":"proxy/successor","params":{"method":"fs/read_text_file","params":{"sessionId":"s","path":"/a","future":true}}}`,
			request: true, method: "fs/read_text_file", params: `{"sessionId":"s","path":"/a","future":true}`,
		},
		{
			name:   "successor notification",
			in:     `{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"future_kind"}}}}`,
			method: "session/update", params: `{"sessionId":"s","update":{"sessionUpdate":"future_kind"}}`,
		},
		{
			name:    "successor request without params",
			in:      `{"jsonrpc":"2.0","id":1,"method":"proxy/successor","params":{"method":"_x/ask"}}`,
			request: true, method: "_x/ask",
		},
		{
			name:   "successor notification with null params",
			in:     `{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"_x/ping","params":null}}`,
			method: "_x/ping",
		},
		{
			name: "a notification of a request method skips its handler",
			handlers: UnstableProxyHandlers{FromSuccessor: UnstableProxyFromSuccessor{
				ReadTextFile: func(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error) {
					t.Error("ReadTextFile ran for a notification")
					return ReadTextFileResponse{}, nil
				},
			}},
			in:     `{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"fs/read_text_file","params":{"sessionId":"s","path":"/a"}}}`,
			method: "fs/read_text_file", params: `{"sessionId":"s","path":"/a"}`,
		},
		{
			name:   "the envelope's _meta stays at its hop",
			in:     `{"jsonrpc":"2.0","method":"proxy/successor","params":{"_meta":{"hop":1},"method":"_x/ping","params":{"_meta":{"inner":1}}}}`,
			method: "_x/ping", params: `{"_meta":{"inner":1}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProxyPeer(t, func(*UnstableProxySideConnection) UnstableProxyHandlers { return tt.handlers })
			p.send(tt.in)
			out := p.next()
			wantMessage(t, out, tt.wrapped, tt.request, tt.method, tt.params)
			if tt.request {
				const result = `{"ok":true,"n":2.50,"future":[null]}`
				p.send(replyLine(out, result))
				wantAnswer(t, p.next(), "1", result)
			}
		})
	}
}

// The error of a forwarded request comes back unchanged in both directions:
// its code, message and data.
func TestUnstableProxySideConnection_RelaysErrorsUnchanged(t *testing.T) {
	const rpcErr = `error:{"code":-32042,"message":"custom","data":{"n":2.50,"why":["a"]}}`
	for name, in := range map[string]string{
		"from the successor": `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s","prompt":[]}}`,
		"from the client":    `{"jsonrpc":"2.0","id":1,"method":"proxy/successor","params":{"method":"session/request_permission","params":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := newProxyPeer(t, nil)
			p.send(in)
			p.send(replyLine(p.next(), rpcErr))
			wantAnswer(t, p.next(), "1", rpcErr)
		})
	}
}

func TestUnstableProxySideConnection_RejectsBadMessages(t *testing.T) {
	tests := []struct {
		name, in string
		code     int
	}{
		{"plain initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`, -32600},
		{"envelope without a method", `{"jsonrpc":"2.0","id":1,"method":"proxy/successor","params":{"params":{}}}`, -32602},
		{"envelope that is not an object", `{"jsonrpc":"2.0","id":1,"method":"proxy/successor","params":["initialize"]}`, -32602},
		{"envelope with number params", `{"jsonrpc":"2.0","id":1,"method":"proxy/successor","params":{"method":"x/y","params":5}}`, -32602},
		{"envelope with string params", `{"jsonrpc":"2.0","id":1,"method":"proxy/successor","params":{"method":"x/y","params":"a"}}`, -32602},
		{"envelope with boolean params", `{"jsonrpc":"2.0","id":1,"method":"proxy/successor","params":{"method":"x/y","params":true}}`, -32602},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProxyPeer(t, nil)
			p.send(tt.in)
			m := p.next()
			if m.ID == nil || string(*m.ID) != "1" || m.Error == nil || m.Error.Code != tt.code {
				t.Fatalf("answer = %+v, want error %d for id 1", m, tt.code)
			}
		})
	}
}

// A wrapped notification whose params are not an object or an array is
// dropped: the next message the client side gets is the request after it.
func TestUnstableProxySideConnection_DropsAWrappedNotificationWithScalarParams(t *testing.T) {
	p := newProxyPeer(t, nil)
	p.send(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"x/note","params":5}}`)
	p.send(`{"jsonrpc":"2.0","id":7,"method":"proxy/successor","params":{"method":"x/ask","params":{}}}`)
	wantMessage(t, p.next(), false, true, "x/ask", `{}`)
}

// A FromSuccessor handler that edits a session/update and sends it with
// Client reaches the client side plain and edited.
func TestUnstableProxySideConnection_TypedSessionUpdateEditsTheUpdate(t *testing.T) {
	p := newProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
		return UnstableProxyHandlers{FromSuccessor: UnstableProxyFromSuccessor{
			SessionUpdate: func(ctx context.Context, n SessionNotification) error {
				text := n.Update.AgentMessageChunk.Content.Text
				text.Text = "[proxy] " + text.Text
				return c.Client().SessionUpdate(ctx, n)
			},
		}}
	})
	p.send(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}}`)
	out := p.next()
	var n SessionNotification
	if out.Method != ClientMethodSessionUpdate || out.ID != nil || json.Unmarshal(out.Params, &n) != nil {
		t.Fatalf("message %+v, want a plain session/update notification", out)
	}
	if chunk := n.Update.AgentMessageChunk; chunk == nil || chunk.Content.Text == nil || chunk.Content.Text.Text != "[proxy] hi" {
		t.Errorf("session/update params %s, want the text [proxy] hi", out.Params)
	}
}

// An UnstableInitializeProxy handler that initializes the successor with
// Successor and adds a capability answers with the edited result.
func TestUnstableProxySideConnection_TypedInitializeProxyEditsACapability(t *testing.T) {
	p := newProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
		return UnstableProxyHandlers{FromClient: UnstableProxyFromClient{
			UnstableInitializeProxy: func(ctx context.Context, req UnstableInitializeProxyRequest) (UnstableInitializeProxyResponse, error) {
				resp, err := c.Successor().Initialize(ctx, req)
				resp.AgentCapabilities.LoadSession = true
				return resp, err
			},
		}}
	})
	p.send(`{"jsonrpc":"2.0","id":0,"method":"proxy/initialize","params":{"protocolVersion":1}}`)
	out := p.next()
	var envelope UnstableProxySuccessorRequest
	var req InitializeRequest
	if out.Method != ProxyMethodProxySuccessor || json.Unmarshal(out.Params, &envelope) != nil ||
		envelope.Method != AgentMethodInitialize || json.Unmarshal(envelope.Params, &req) != nil || req.ProtocolVersion != 1 {
		t.Fatalf("message %+v, want initialize with protocolVersion 1 wrapped in proxy/successor", out)
	}
	p.send(replyLine(out, `{"protocolVersion":1,"agentCapabilities":{},"authMethods":[]}`))
	answer := p.next()
	var resp InitializeResponse
	if err := json.Unmarshal(answer.Result, &resp); err != nil || !resp.AgentCapabilities.LoadSession || resp.ProtocolVersion != 1 {
		t.Fatalf("answer %s (%v), want protocolVersion 1 with loadSession", answer.Result, err)
	}
}

// $/cancel_request is per hop: one for the client's request makes the proxy
// cancel its own proxy/successor request, with that request's id.
func TestUnstableProxySideConnection_CancelRequestIsPerHop(t *testing.T) {
	p := newProxyPeer(t, nil)
	p.send(`{"jsonrpc":"2.0","id":7,"method":"session/prompt","params":{"sessionId":"s","prompt":[]}}`)
	out := p.next()
	wantMessage(t, out, true, true, "session/prompt", `{"sessionId":"s","prompt":[]}`)
	p.send(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":7}}`)
	// The proxy's $/cancel_request and its answer to 7 can come in either order.
	var cancel, answer anyMessage
	for i := 0; i < 2; i++ {
		if m := p.next(); m.Method != "" {
			cancel = m
		} else {
			answer = m
		}
	}
	if cancel.Method != ProtocolMethodCancelRequest || compactJSON(t, cancel.Params) != `{"requestId":`+string(*out.ID)+`}` {
		t.Errorf("message %+v, want a $/cancel_request for the proxy/successor request %s", cancel, *out.ID)
	}
	if answer.ID == nil || string(*answer.ID) != "7" || answer.Error == nil || answer.Error.Code != -32800 {
		t.Errorf("answer = %+v, want error -32800 for id 7", answer)
	}
}

// The successor's notifications that arrive before its prompt result reach
// the client side before the proxy's answer to the prompt.
func TestUnstableProxySideConnection_ForwardsUpdatesBeforeThePromptAnswer(t *testing.T) {
	p := newProxyPeer(t, nil)
	p.send(`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s","prompt":[]}}`)
	out := p.next()
	update := func(i int) string {
		return fmt.Sprintf(`{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"%d"}}}`, i)
	}
	for i := 0; i < 3; i++ {
		p.send(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"session/update","params":` + update(i) + `}}`)
	}
	p.send(replyLine(out, `{"stopReason":"end_turn"}`))
	for i := 0; i < 3; i++ {
		wantMessage(t, p.next(), false, false, "session/update", update(i))
	}
	wantAnswer(t, p.next(), "3", `{"stopReason":"end_turn"}`)
}

// A message that the connection reads before the constructor returns, while
// handlers runs, still gets the handlers.
func TestUnstableProxySideConnection_HandlesMessagesReadBeforeItReturns(t *testing.T) {
	inR, inW := io.Pipe()
	t.Cleanup(func() { _ = inW.Close() })
	cancelled := make(chan SessionId, 1)
	NewUnstableProxySideConnection(func(*UnstableProxySideConnection) UnstableProxyHandlers {
		// The write returns once the connection has read the line.
		if _, err := io.WriteString(inW, `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"early"}}`+"\n"); err != nil {
			t.Error(err)
		}
		return UnstableProxyHandlers{FromClient: UnstableProxyFromClient{
			Cancel: func(_ context.Context, n CancelNotification) error {
				cancelled <- n.SessionId
				return nil
			},
		}}
	}, io.Discard, inR)
	select {
	case got := <-cancelled:
		if got != "early" {
			t.Errorf("Cancel got session %q, want early", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel never ran for the message read before the constructor returned")
	}
}
