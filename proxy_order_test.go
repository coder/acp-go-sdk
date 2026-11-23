package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
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
	kind, err := messageKind(m)
	if err != nil {
		t.Fatal(err)
	}
	return kind
}

// messageKind is sentKind for a goroutine other than the test's.
func messageKind(m anyMessage) (string, error) {
	switch {
	case m.Method == "":
		return "answer " + string(*m.ID), nil
	case m.Method == ProxyMethodProxySuccessor:
		var envelope struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(m.Params, &envelope); err != nil {
			return "", fmt.Errorf("envelope %s: %v", m.Params, err)
		}
		return "wrapped " + envelope.Method, nil
	default:
		return "plain " + m.Method, nil
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

// A $/cancel_request for a forwarded request whose answer has arrived does
// not let that answer pass a request that the successor sent before it.
func TestUnstableProxySideConnection_KeepsAnAnswersPlaceWhenItsRequestIsCancelled(t *testing.T) {
	release := make(chan struct{})
	p := newProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
		return UnstableProxyHandlers{FromSuccessor: UnstableProxyFromSuccessor{
			RequestPermission: func(ctx context.Context, r RequestPermissionRequest) (RequestPermissionResponse, error) {
				<-release
				return c.Client().RequestPermission(ctx, r)
			},
		}}
	})
	p.send(promptLineOf("1", "s"))
	out := wantKinds(t, p, "wrapped session/prompt")[0]
	p.send(wrappedPermissionLine("c-1", "s"), replyLine(out, `{"stopReason":"end_turn"}`))
	p.send(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":1}}`)
	// Give an answer that does not wait for its turn the time to go out.
	time.Sleep(100 * time.Millisecond)
	close(release)
	var kinds []string
	for len(kinds) < 2 {
		// The cancel can reach the prompt's handler before the answer
		// does, and the proxy then cancels its own request.
		if kind := sentKind(t, p.next()); kind != "plain $/cancel_request" {
			kinds = append(kinds, kind)
		}
	}
	if want := []string{"plain session/request_permission", "answer 1"}; fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Errorf("the proxy sent %q, want %q", kinds, want)
	}
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

// answerRuns is how many times each test of an answer that comes at once
// runs its flow.
const answerRuns = 20

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// newAnsweringProxyPeer is newProxyPeer with a conductor that answers some
// requests at once, as a peer in the same process can: when the proxy writes
// a request whose kind (see sentKind) is in answers, the conductor answers it
// with that result, and the proxy has handled the answer before its write
// returns.
func newAnsweringProxyPeer(t *testing.T, handlers func(*UnstableProxySideConnection) UnstableProxyHandlers, answers map[string]string) *cancelPeer {
	t.Helper()
	var p *cancelPeer
	p = newRawPeer(t, func(peerInput io.Writer, peerOutput io.Reader) {
		answering := writerFunc(func(b []byte) (int, error) {
			n, err := peerInput.Write(b)
			var m anyMessage
			if err != nil || json.Unmarshal(b, &m) != nil || m.ID == nil || m.Method == "" {
				return n, err
			}
			if kind, kindErr := messageKind(m); kindErr == nil {
				if result, ok := answers[kind]; ok {
					_, _ = io.WriteString(p.in, replyLine(m, result)+"\n")
					// The proxy handles a line before it reads the next, and a
					// pipe write returns once it has been read.
					_, _ = io.WriteString(p.in, "\n")
				}
			}
			return n, err
		})
		c := NewUnstableProxySideConnection(handlers, answering, peerOutput)
		c.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return p
}

// wantKinds checks that the next messages that the proxy sends are of the
// kinds want, and returns them. It fails the test when the proxy sends
// nothing for 5 s.
func wantKinds(t *testing.T, p *cancelPeer, want ...string) []anyMessage {
	t.Helper()
	msgs := make([]anyMessage, 0, len(want))
	var kinds []string
	for len(msgs) < len(want) {
		select {
		case m := <-p.lines:
			msgs = append(msgs, m)
			kinds = append(kinds, sentKind(t, m))
		case <-time.After(5 * time.Second):
			t.Fatalf("the proxy sent %q and then hung; want %q", kinds, want)
		}
	}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("the proxy sent %q, want %q", kinds, want)
	}
	return msgs
}

// A handler that sends a request lets the later messages from its side go,
// also when the answer comes before the proxy has finished sending the
// request. The handler then waits for the notifications that arrived before
// the answer (see SendRequest), so a notification left waiting for its turn
// hangs the handler.
func TestUnstableProxySideConnection_ReleasesLaterMessagesWhenAnAnswerComesAtOnce(t *testing.T) {
	t.Run("two requests to the successor", func(t *testing.T) {
		// The agent answers session/new and announces its commands, and it
		// answers session/set_mode at once.
		for i := 0; i < answerRuns; i++ {
			p := newAnsweringProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
				return UnstableProxyHandlers{FromClient: UnstableProxyFromClient{
					NewSession: func(ctx context.Context, r NewSessionRequest) (NewSessionResponse, error) {
						resp, err := c.Successor().NewSession(ctx, r)
						if err != nil {
							return resp, err
						}
						_, err = c.Successor().SetSessionMode(ctx, SetSessionModeRequest{SessionId: resp.SessionId, ModeId: "plan"})
						return resp, err
					},
				}}
			}, map[string]string{"wrapped session/set_mode": `{}`})
			p.send(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/","mcpServers":[]}}`)
			out := wantKinds(t, p, "wrapped session/new")[0]
			p.send(replyLine(out, `{"sessionId":"s"}`), wrappedUpdateLine("s", "commands"))
			wantKinds(t, p, "wrapped session/set_mode", "plain session/update", "answer 1")
		}
	})
	t.Run("a request to the client before the forward", func(t *testing.T) {
		// A policy proxy asks the client before it forwards the prompt, the
		// client answers at once, and a session/cancel follows the prompt.
		for i := 0; i < answerRuns; i++ {
			p := newAnsweringProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
				return UnstableProxyHandlers{FromClient: UnstableProxyFromClient{
					Prompt: func(ctx context.Context, r PromptRequest) (PromptResponse, error) {
						if _, err := c.Client().RequestPermission(ctx, RequestPermissionRequest{SessionId: r.SessionId, ToolCall: ToolCallUpdate{ToolCallId: "t"}, Options: []PermissionOption{}}); err != nil {
							return PromptResponse{}, err
						}
						return c.Successor().Prompt(ctx, r)
					},
				}}
			}, map[string]string{"plain session/request_permission": `{"outcome":{"outcome":"cancelled"}}`})
			p.send(promptLineOf("1", "s"), cancelLineOf("s"))
			msgs := wantKinds(t, p, "plain session/request_permission", "wrapped session/cancel", "wrapped session/prompt")
			p.send(replyLine(msgs[2], `{"stopReason":"cancelled"}`))
			wantAnswer(t, p.next(), "1", `{"stopReason":"cancelled"}`)
		}
	})
}

// A handler that sends with a ctx derived from its own, such as one from
// context.WithoutCancel, sends as the handler: its forward lets the later
// messages from its side go, so the client's answer to a permission request
// that the agent sends before it answers the prompt reaches the agent.
func TestUnstableProxySideConnection_KnowsAHandlerByACtxDerivedFromItsOwn(t *testing.T) {
	p := newProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
		return UnstableProxyHandlers{FromClient: UnstableProxyFromClient{
			Prompt: func(ctx context.Context, r PromptRequest) (PromptResponse, error) {
				return c.Successor().Prompt(context.WithoutCancel(ctx), r)
			},
		}}
	})
	p.send(promptLineOf("1", "s"))
	prompt := wantKinds(t, p, "wrapped session/prompt")[0]
	p.send(wrappedPermissionLine("c-1", "s"))
	permission := wantKinds(t, p, "plain session/request_permission")[0]
	p.send(replyLine(permission, `{"outcome":{"outcome":"cancelled"}}`))
	wantKinds(t, p, `answer "c-1"`)
	p.send(replyLine(prompt, `{"stopReason":"end_turn"}`))
	wantAnswer(t, p.next(), "1", `{"stopReason":"end_turn"}`)
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
	// JSON can escape any character of the method, as \u005f is "_".
	for name, method := range map[string]string{"handled": `$/cancel_request`, "handled, escaped": `$/cancel\u005frequest`} {
		t.Run(name, func(t *testing.T) {
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
			p.send(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"` + method + `","params":{"requestId":"c-9"}}}`)
			if err := <-ended; err == nil {
				t.Fatal("the wrapped $/cancel_request did not cancel the handler's ctx")
			}
			if m := p.next(); sentKind(t, m) != `answer "c-9"` {
				t.Fatalf("the proxy sent %s, want only its answer to c-9", sentKind(t, m))
			}
		})
	}
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
// session/update of a kind that it does not know, goes on unchanged. The
// kind starts with "_", which ACP keeps for extensions, so that a later
// schema does not make it a known kind, as 1.33.0 did with cursor_update.
func TestUnstableProxySideConnection_ForwardsWhatATypedHandlerCannotDecode(t *testing.T) {
	const update = `{"sessionId":"s","update":{"sessionUpdate":"_hologram_update","hologramId":"h1","anchor":{"messageId":"m1","position":"after"}}}`
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
	p.send(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"session/update","params":` + update + `}}`)
	wantMessage(t, p.next(), false, false, "session/update", update)
	const hologram = `{"sessionId":"s","prompt":[{"type":"hologram"}]}`
	p.send(`{"jsonrpc":"2.0","id":5,"method":"session/prompt","params":` + hologram + `}`)
	out := p.next()
	wantMessage(t, out, true, true, "session/prompt", hologram)
	p.send(replyLine(out, `{"stopReason":"end_turn"}`))
	wantAnswer(t, p.next(), "5", `{"stopReason":"end_turn"}`)
}

// RejectUndecodable refuses such a message: a request gets -32602 and a
// notification is dropped.
func TestUnstableProxySideConnection_RejectUndecodableRefusesWhatAHandlerCannotDecode(t *testing.T) {
	p := newProxyPeer(t, func(c *UnstableProxySideConnection) UnstableProxyHandlers {
		return UnstableProxyHandlers{
			FromClient: UnstableProxyFromClient{
				Prompt: func(context.Context, PromptRequest) (PromptResponse, error) {
					return PromptResponse{}, fmt.Errorf("the handler got an undecodable prompt")
				},
			},
			FromSuccessor: UnstableProxyFromSuccessor{
				SessionUpdate: func(ctx context.Context, n SessionNotification) error {
					return c.Client().SessionUpdate(ctx, n)
				},
			},
			RejectUndecodable: true,
		}
	})
	p.send(`{"jsonrpc":"2.0","method":"proxy/successor","params":{"method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"_hologram_update","hologramId":"h1"}}}}`,
		wrappedUpdateLine("s", "after"))
	if m := p.next(); sentKind(t, m) != "plain session/update" || strings.Contains(string(m.Params), "_hologram_update") {
		t.Errorf("the proxy sent %s %s, want the update after the dropped _hologram_update", sentKind(t, m), m.Params)
	}
	p.send(`{"jsonrpc":"2.0","id":5,"method":"session/prompt","params":{"sessionId":"s","prompt":[{"type":"hologram"}]}}`)
	if m := p.next(); m.ID == nil || string(*m.ID) != "5" || m.Error == nil || m.Error.Code != -32602 {
		t.Errorf("the proxy sent %+v, want a -32602 answer to 5", m)
	}
}
