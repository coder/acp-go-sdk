package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// orderRuns is how many times the order tests send their messages in one
// write. Through the conductor, the proxy at c7e3805 sent a session/cancel
// ahead of its session/prompt in 48 of 60 such writes.
const orderRuns = 200

// sentKind names a message that the proxy sends: "answer <id>", "wrapped
// <method>" for a proxy/successor envelope, or "plain <method>".
func sentKind(t *testing.T, m anyMessage) string {
	t.Helper()
	switch {
	case m.Method == "":
		return "answer " + string(*m.ID)
	case m.Method == ProxyMethodProxySuccessor:
		var envelope struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(m.Params, &envelope); err != nil {
			t.Fatalf("envelope %s: %v", m.Params, err)
		}
		return "wrapped " + envelope.Method
	default:
		return "plain " + m.Method
	}
}

// nextKinds returns the next n messages that the proxy sends and their
// kinds.
func (p *cancelPeer) nextKinds(n int) ([]anyMessage, []string) {
	p.t.Helper()
	msgs := make([]anyMessage, n)
	kinds := make([]string, n)
	for i := range msgs {
		msgs[i] = p.next()
		kinds[i] = sentKind(p.t, msgs[i])
	}
	return msgs, kinds
}

// countOrder runs send orderRuns times. Each run returns the kinds of the
// messages that the proxy sent and the kinds in arrival order. countOrder
// fails the test with the number of runs whose order differs.
func countOrder(t *testing.T, what string, run func(i int) (got, want []string)) {
	t.Helper()
	wrong := 0
	var example string
	for i := 0; i < orderRuns; i++ {
		got, want := run(i)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			wrong++
			example = fmt.Sprintf("run %d sent %q, want %q", i, got, want)
		}
	}
	if wrong > 0 {
		t.Errorf("%s: %d of %d runs left the proxy out of arrival order; %s", what, wrong, orderRuns, example)
	}
}

func promptLineOf(id, session string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"session/prompt","params":{"sessionId":%q,"prompt":[]}}`, id, session)
}

func cancelLineOf(session string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":%q}}`, session)
}

func wrappedUpdateLine(session, text string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}}`, session, text)
}

func wrappedPermissionLine(id, session string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":"proxy/successor","params":{"method":"session/request_permission","params":{"sessionId":%q,"toolCall":{"toolCallId":"t"},"options":[]}}}`, id, session)
}

// The race harness of the review of #153 in one test: a client writes
// session/prompt and its session/cancel in one write. The proxy must forward
// the cancel after the prompt, or the agent gets a cancel for no turn and the
// turn runs on.
func TestUnstableProxySideConnection_ForwardsACancelAfterItsPrompt(t *testing.T) {
	p := newProxyPeer(t, nil)
	countOrder(t, "session/prompt then session/cancel", func(i int) ([]string, []string) {
		session := fmt.Sprint("s", i)
		p.send(promptLineOf(fmt.Sprint(i), session), cancelLineOf(session))
		msgs, kinds := p.nextKinds(2)
		for _, m := range msgs {
			if m.ID != nil {
				p.send(replyLine(m, `{"stopReason":"cancelled"}`))
				p.next()
			}
		}
		return kinds, []string{"wrapped session/prompt", "wrapped session/cancel"}
	})
}

// Requests from one side leave in the order they arrived.
func TestUnstableProxySideConnection_ForwardsRequestsInOrder(t *testing.T) {
	p := newProxyPeer(t, nil)
	countOrder(t, "session/set_mode then session/prompt", func(i int) ([]string, []string) {
		session := fmt.Sprint("s", i)
		p.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":"m%d","method":"session/set_mode","params":{"sessionId":%q,"modeId":"code"}}`, i, session),
			promptLineOf(fmt.Sprint(i), session))
		msgs, kinds := p.nextKinds(2)
		for _, m := range msgs {
			p.send(replyLine(m, `{}`))
		}
		p.nextKinds(2)
		return kinds, []string{"wrapped session/set_mode", "wrapped session/prompt"}
	})
}

// The answer to a forwarded request leaves before a message that arrived
// after it from the same side.
func TestUnstableProxySideConnection_ForwardsAnAnswerBeforeLaterMessages(t *testing.T) {
	p := newProxyPeer(t, nil)
	countOrder(t, "the prompt result then a session/update", func(i int) ([]string, []string) {
		session := fmt.Sprint("s", i)
		p.send(promptLineOf(fmt.Sprint(i), session))
		out := p.next()
		p.send(replyLine(out, `{"stopReason":"end_turn"}`), wrappedUpdateLine(session, "after"))
		_, kinds := p.nextKinds(2)
		return kinds, []string{fmt.Sprint("answer ", i), "plain session/update"}
	})
}

// A request that arrived before the answer to a forwarded request leaves
// before that answer.
func TestUnstableProxySideConnection_ForwardsARequestBeforeALaterAnswer(t *testing.T) {
	p := newProxyPeer(t, nil)
	countOrder(t, "session/request_permission then the prompt result", func(i int) ([]string, []string) {
		session := fmt.Sprint("s", i)
		p.send(promptLineOf(fmt.Sprint(i), session))
		out := p.next()
		p.send(wrappedPermissionLine(fmt.Sprint("c", i), session), replyLine(out, `{"stopReason":"cancelled"}`))
		msgs, kinds := p.nextKinds(2)
		for _, m := range msgs {
			if m.Method != "" {
				p.send(replyLine(m, `{"outcome":{"outcome":"cancelled"}}`))
				p.next()
			}
		}
		return kinds, []string{"plain session/request_permission", fmt.Sprint("answer ", i)}
	})
}

// A handler that sends its message on to the other side holds back the
// later messages from its side until it has sent it.
func TestUnstableProxySideConnection_HoldsLaterMessagesUntilAHandlerSends(t *testing.T) {
	release := make(chan struct{})
	p := newProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
		return UnstableProxyHandlers{FromClient: UnstableProxyFromClient{
			Prompt: func(ctx context.Context, r PromptRequest) (PromptResponse, error) {
				<-release
				return c.Successor().Prompt(ctx, r)
			},
		}}
	})
	p.send(promptLineOf("1", "s"), cancelLineOf("s"))
	select {
	case m := <-p.lines:
		t.Fatalf("the proxy sent %s while the prompt handler had not sent the prompt", sentKind(t, m))
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	_, kinds := p.nextKinds(2)
	if fmt.Sprint(kinds) != fmt.Sprint([]string{"wrapped session/prompt", "wrapped session/cancel"}) {
		t.Errorf("the proxy sent %q, want the prompt and then the cancel", kinds)
	}
}

// A handler that waits for an answer lets the later messages from its side
// go: the answer can need them.
func TestUnstableProxySideConnection_ReleasesLaterMessagesWhileAHandlerWaits(t *testing.T) {
	p := newProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
		return UnstableProxyHandlers{FromClient: UnstableProxyFromClient{
			Prompt: func(ctx context.Context, r PromptRequest) (PromptResponse, error) {
				if _, err := c.Client().RequestPermission(ctx, RequestPermissionRequest{SessionId: r.SessionId, ToolCall: ToolCallUpdate{ToolCallId: "t"}, Options: []PermissionOption{}}); err != nil {
					return PromptResponse{}, err
				}
				return c.Successor().Prompt(ctx, r)
			},
		}}
	})
	p.send(promptLineOf("1", "s"), cancelLineOf("s"))
	msgs, kinds := p.nextKinds(2)
	var permission anyMessage
	for i, k := range kinds {
		if k == "plain session/request_permission" {
			permission = msgs[i]
		}
	}
	if permission.ID == nil || (kinds[0] != "wrapped session/cancel" && kinds[1] != "wrapped session/cancel") {
		t.Fatalf("the proxy sent %q, want the permission request and the cancel before the client answers", kinds)
	}
	p.send(replyLine(permission, `{"outcome":{"outcome":"cancelled"}}`))
	out := p.next()
	if sentKind(t, out) != "wrapped session/prompt" {
		t.Fatalf("the proxy sent %s, want the prompt", sentKind(t, out))
	}
	p.send(replyLine(out, `{"stopReason":"cancelled"}`))
	wantAnswer(t, p.next(), "1", `{"stopReason":"cancelled"}`)
}

// A request from the peer starts while the running notification handler
// waits for the peer (see MethodHandler), and its forward does not wait for
// the notifications queued behind that handler: the peer can need the
// request's answer before it answers the handler.
func TestUnstableProxySideConnection_ForwardsARequestWhileANotificationHandlerWaits(t *testing.T) {
	p := newProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
		return UnstableProxyHandlers{FromClient: UnstableProxyFromClient{
			Cancel: func(ctx context.Context, n CancelNotification) error {
				if _, err := c.Client().RequestPermission(ctx, RequestPermissionRequest{SessionId: n.SessionId, ToolCall: ToolCallUpdate{ToolCallId: "t"}, Options: []PermissionOption{}}); err != nil {
					return err
				}
				return c.Successor().Cancel(ctx, n)
			},
		}}
	})
	p.send(cancelLineOf("a"), cancelLineOf("b"), promptLineOf("1", "c"))
	msgs, kinds := p.nextKinds(2)
	var permission, prompt anyMessage
	for i, k := range kinds {
		switch k {
		case "plain session/request_permission":
			permission = msgs[i]
		case "wrapped session/prompt":
			prompt = msgs[i]
		}
	}
	if permission.ID == nil || prompt.ID == nil {
		t.Fatalf("the proxy sent %q, want the permission request and the prompt before the client answers", kinds)
	}
	p.send(replyLine(prompt, `{"stopReason":"end_turn"}`))
	wantAnswer(t, p.next(), "1", `{"stopReason":"end_turn"}`)
}

// The RFD's cancellation rule (proxy-chains.mdx, Cancellation): a
// $/cancel_request wrapped in proxy/successor names the proxy/successor
// request on this connection, as a plain one does, and is never forwarded.
func TestUnstableProxySideConnection_WrappedCancelRequestIsPerHop(t *testing.T) {
	t.Run("handled", func(t *testing.T) {
		started := make(chan struct{})
		ended := make(chan error, 1)
		p := newProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
			return UnstableProxyHandlers{FromSuccessor: UnstableProxyFromSuccessor{
				RequestPermission: func(ctx context.Context, r RequestPermissionRequest) (RequestPermissionResponse, error) {
					close(started)
					select {
					case <-ctx.Done():
						ended <- ctx.Err()
					case <-time.After(5 * time.Second):
						ended <- nil
					}
					return RequestPermissionResponse{}, ctx.Err()
				},
			}}
		})
		p.send(wrappedPermissionLine("c-9", "s"))
		waitCancelTest(t, started, "the permission handler")
		p.send(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"$/cancel_request","params":{"requestId":"c-9"}}}`)
		if err := <-ended; err == nil {
			t.Fatal("the wrapped $/cancel_request did not cancel the handler's ctx")
		}
		if m := p.next(); sentKind(t, m) != `answer "c-9"` {
			t.Fatalf("the proxy sent %s, want only its answer to c-9", sentKind(t, m))
		}
	})
	t.Run("forwarded", func(t *testing.T) {
		p := newProxyPeer(t, nil)
		p.send(wrappedPermissionLine("c-9", "s"))
		out := p.next()
		if sentKind(t, out) != "plain session/request_permission" {
			t.Fatalf("the proxy sent %s, want the permission request", sentKind(t, out))
		}
		p.send(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"$/cancel_request","params":{"requestId":"c-9"}}}`)
		// The proxy's own $/cancel_request and its answer to c-9 can come in
		// either order.
		msgs, kinds := p.nextKinds(2)
		for i, m := range msgs {
			switch kinds[i] {
			case "plain $/cancel_request":
				if got := compactJSON(t, m.Params); got != `{"requestId":`+string(*out.ID)+`}` {
					t.Errorf("$/cancel_request params = %s, want the id of the proxy's request %s", got, *out.ID)
				}
			case `answer "c-9"`:
				if m.Error == nil || m.Error.Code != -32800 {
					t.Errorf("answer to c-9 = %+v, want error -32800", m)
				}
			default:
				t.Errorf("the proxy sent %s, want its $/cancel_request and its answer to c-9", kinds[i])
			}
		}
	})
}

// A typed handler's message that this SDK cannot decode, such as a
// session/update of a kind from a newer schema, goes on unchanged.
func TestUnstableProxySideConnection_ForwardsWhatATypedHandlerCannotDecode(t *testing.T) {
	const cursor = `{"sessionId":"s","update":{"sessionUpdate":"cursor_update","cursorId":"c1","anchor":{"messageId":"m1","position":"after"}}}`
	p := newProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
		return UnstableProxyHandlers{
			FromClient: UnstableProxyFromClient{
				Prompt: func(context.Context, PromptRequest) (PromptResponse, error) {
					return PromptResponse{}, fmt.Errorf("the handler got an undecodable prompt")
				},
			},
			FromSuccessor: UnstableProxyFromSuccessor{
				SessionUpdate: func(context.Context, SessionNotification) error {
					return fmt.Errorf("the handler got an undecodable update")
				},
			},
		}
	})
	p.send(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"session/update","params":` + cursor + `}}`)
	wantMessage(t, p.next(), false, false, "session/update", cursor)
	const hologram = `{"sessionId":"s","prompt":[{"type":"hologram"}]}`
	p.send(`{"jsonrpc":"2.0","id":5,"method":"session/prompt","params":` + hologram + `}`)
	out := p.next()
	wantMessage(t, out, true, true, "session/prompt", hologram)
	p.send(replyLine(out, `{"stopReason":"end_turn"}`))
	wantAnswer(t, p.next(), "5", `{"stopReason":"end_turn"}`)
}
