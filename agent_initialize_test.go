package acp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// initializeLine is the initialize request that a test peer that writes raw
// lines sends before any other request.
const initializeLine = `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":1}}`

func replyToInitialize(m anyMessage) bool { return m.ID != nil && string(*m.ID) == `"init"` }

// initialize sends initializeLine and waits for its reply. The connection
// writes that reply after it is initialized.
func (p *cancelPeer) initialize() {
	p.t.Helper()
	p.send(initializeLine)
	if reply := p.await("the reply to initialize", replyToInitialize); reply.Error != nil {
		p.t.Fatalf("SETUP: initialize failed: %+v", reply.Error)
	}
}

// initializeRaw writes initializeLine to w, which conn reads, as the first
// message, and waits until conn has answered it. It serves a test that does
// not read what conn writes.
func initializeRaw(t *testing.T, conn *AgentSideConnection, w io.Writer) {
	t.Helper()
	if _, err := io.WriteString(w, initializeLine+"\n"); err != nil {
		t.Fatalf("SETUP: write initialize: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !conn.initialized.Load() {
		if time.Now().After(deadline) {
			t.Fatal("SETUP: the connection did not initialize")
		}
		time.Sleep(time.Millisecond)
	}
	// An initialize still in flight would keep cancel markers alive.
	waitNoInflightRequests(t, conn.conn)
}

// initializeClient initializes the agent that c talks to.
func initializeClient(t testing.TB, c *ClientSideConnection) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Initialize(ctx, InitializeRequest{ProtocolVersion: ProtocolVersionNumber}); err != nil {
		t.Fatalf("SETUP: initialize: %v", err)
	}
}

// gatePeer connects agent to a raw peer that has not initialized it.
func gatePeer(t *testing.T, agent agentFuncs) *cancelPeer {
	t.Helper()
	return newRawPeer(t, func(peerInput io.Writer, peerOutput io.Reader) {
		conn := NewAgentSideConnection(agent, peerInput, peerOutput)
		conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
}

// recordingAgent sends to reached the method of every session/new,
// session/cancel and extension message that reaches it. Its Initialize
// returns initErr.
func recordingAgent(reached chan<- string, initErr error) agentFuncs {
	return agentFuncs{
		InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
			return InitializeResponse{ProtocolVersion: ProtocolVersionNumber}, initErr
		},
		NewSessionFunc: func(context.Context, NewSessionRequest) (NewSessionResponse, error) {
			reached <- AgentMethodSessionNew
			return NewSessionResponse{SessionId: "s"}, nil
		},
		CancelFunc: func(context.Context, CancelNotification) error {
			reached <- AgentMethodSessionCancel
			return nil
		},
		HandleExtensionMethodFunc: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			reached <- method
			return map[string]any{}, nil
		},
	}
}

// drain returns what ch holds now.
func drain(ch <-chan string) []string {
	var got []string
	for {
		select {
		case m := <-ch:
			got = append(got, m)
		default:
			return got
		}
	}
}

const sessionNewParams = `{"cwd":"/","mcpServers":[]}`

func TestAgentSideConnection_RejectsRequestsBeforeInitialize(t *testing.T) {
	tests := []struct {
		name   string
		method string
		params string
		// failedInitialize makes the peer send an initialize that the Agent
		// fails before it sends the request.
		failedInitialize bool
	}{
		{name: "session/new", method: AgentMethodSessionNew, params: sessionNewParams},
		{name: "extension request", method: "_vendor.test/echo", params: `{}`},
		{name: "session/new after a failed initialize", method: AgentMethodSessionNew, params: sessionNewParams, failedInitialize: true},
		{name: "extension request after a failed initialize", method: "_vendor.test/echo", params: `{}`, failedInitialize: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			reached := make(chan string, 4)
			var initErr error
			if tt.failedInitialize {
				initErr = NewInternalError(nil)
			}
			peer := gatePeer(t, recordingAgent(reached, initErr))
			if tt.failedInitialize {
				peer.send(initializeLine)
				if reply := peer.await("the reply to initialize", replyToInitialize); reply.Error == nil {
					t.Fatal("SETUP: initialize succeeded, want it to fail")
				}
			}
			peer.send(`{"jsonrpc":"2.0","id":1,"method":"` + tt.method + `","params":` + tt.params + `}`)
			reply := peer.await("the reply to "+tt.method, replyTo(1))
			if reply.Error == nil || reply.Error.Code != -32600 {
				t.Errorf("%s before initialize got result %s and error %+v, want error -32600", tt.method, reply.Result, reply.Error)
			}
			// The connection would call the Agent before it writes the reply.
			if got := drain(reached); len(got) != 0 {
				t.Errorf("%s before initialize reached the Agent as %v", tt.method, got)
			}
		})
	}
}

func TestAgentSideConnection_DropsNotificationsBeforeInitialize(t *testing.T) {
	tests := []struct {
		name   string
		method string
		params string
	}{
		{name: "session/cancel", method: AgentMethodSessionCancel, params: `{"sessionId":"s"}`},
		{name: "extension notification", method: "_vendor.test/note", params: `{}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			reached := make(chan string, 4)
			peer := gatePeer(t, recordingAgent(reached, nil))
			notification := `{"jsonrpc":"2.0","method":"` + tt.method + `","params":` + tt.params + `}`
			peer.send(notification)
			// initialize starts after the handler of the notification before
			// it has returned, so its reply comes after any call it made.
			peer.initialize()
			if got := drain(reached); len(got) != 0 {
				t.Errorf("%s before initialize reached the Agent as %v", tt.method, got)
			}

			peer.send(notification, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":`+sessionNewParams+`}`)
			peer.await("the reply to session/new", replyTo(2))
			if got := drain(reached); len(got) != 2 || got[0] != tt.method {
				t.Errorf("%s and session/new after initialize reached the Agent as %v, want [%s %s]", tt.method, got, tt.method, AgentMethodSessionNew)
			}
		})
	}
}

func TestAgentSideConnection_InitializeOpensTheConnection(t *testing.T) {
	tests := []struct {
		name string
		// failures is how many initialize calls fail before one succeeds.
		failures int
	}{
		{name: "first initialize succeeds", failures: 0},
		{name: "initialize succeeds after a failed one", failures: 1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var initCalls atomic.Int32
			reached := make(chan string, 4)
			agent := recordingAgent(reached, nil)
			agent.InitializeFunc = func(context.Context, InitializeRequest) (InitializeResponse, error) {
				if int(initCalls.Add(1)) <= tt.failures {
					return InitializeResponse{}, NewInternalError(nil)
				}
				return InitializeResponse{ProtocolVersion: ProtocolVersionNumber}, nil
			}
			peer := gatePeer(t, agent)
			for i := 0; i < tt.failures; i++ {
				peer.send(initializeLine)
				if reply := peer.await("the reply to initialize", replyToInitialize); reply.Error == nil {
					t.Fatal("SETUP: initialize succeeded, want it to fail")
				}
			}
			peer.initialize()

			peer.send(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":` + sessionNewParams + `}`)
			if reply := peer.await("the reply to session/new", replyTo(1)); reply.Error != nil || string(reply.Result) != `{"sessionId":"s"}` {
				t.Errorf("session/new after initialize got result %s and error %+v, want sessionId s", reply.Result, reply.Error)
			}
			// A repeated initialize still reaches the Agent.
			peer.initialize()
			if got, want := int(initCalls.Load()), tt.failures+2; got != want {
				t.Errorf("Initialize ran %d times, want %d", got, want)
			}
		})
	}
}

// TestAgentSideConnection_InitializeThatDoesNotEncodeKeepsTheGate checks an
// Initialize that returns a response the connection cannot encode: the client
// gets -32603, so the connection must stay uninitialized.
func TestAgentSideConnection_InitializeThatDoesNotEncodeKeepsTheGate(t *testing.T) {
	reached := make(chan string, 4)
	agent := recordingAgent(reached, nil)
	agent.InitializeFunc = func(context.Context, InitializeRequest) (InitializeResponse, error) {
		return InitializeResponse{ProtocolVersion: ProtocolVersionNumber, Meta: Meta{"bad": json.RawMessage(`{`)}}, nil
	}
	peer := gatePeer(t, agent)
	peer.send(initializeLine)
	if reply := peer.await("the reply to initialize", replyToInitialize); reply.Error == nil || reply.Error.Code != -32603 {
		t.Fatalf("initialize with an unencodable response got error %+v, want -32603", reply.Error)
	}
	peer.send(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":` + sessionNewParams + `}`)
	if reply := peer.await("the reply to session/new", replyTo(1)); reply.Error == nil || reply.Error.Code != -32600 {
		t.Errorf("session/new after a failed initialize got error %+v, want -32600", reply.Error)
	}
	if got := drain(reached); len(got) != 0 {
		t.Errorf("session/new after a failed initialize reached the Agent as %v", got)
	}
}

func TestAgentSideConnection_RejectsRequestsDuringInitialize(t *testing.T) {
	initStarted := make(chan struct{})
	releaseInit := make(chan struct{})
	reached := make(chan string, 4)
	agent := recordingAgent(reached, nil)
	agent.InitializeFunc = func(context.Context, InitializeRequest) (InitializeResponse, error) {
		close(initStarted)
		<-releaseInit
		return InitializeResponse{ProtocolVersion: ProtocolVersionNumber}, nil
	}
	peer := gatePeer(t, agent)
	peer.send(initializeLine)
	waitCancelTest(t, initStarted, "Initialize to start")

	peer.send(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":` + sessionNewParams + `}`)
	if reply := peer.await("the reply to session/new", replyTo(1)); reply.Error == nil || reply.Error.Code != -32600 {
		t.Errorf("session/new during initialize got result %s and error %+v, want error -32600", reply.Result, reply.Error)
	}
	close(releaseInit)
	if reply := peer.await("the reply to initialize", replyToInitialize); reply.Error != nil {
		t.Fatalf("SETUP: initialize failed: %+v", reply.Error)
	}
	if got := drain(reached); len(got) != 0 {
		t.Errorf("session/new during initialize reached the Agent as %v", got)
	}
}

// TestAgentSideConnection_HandlesMessagesWaitingAtConstruction feeds the
// connection messages that are already waiting when it is built, so their
// handlers run as soon as it starts reading. Run it with -race: the gate and
// the prompt handler read a.conn, which must be set before the first handler.
func TestAgentSideConnection_HandlesMessagesWaitingAtConstruction(t *testing.T) {
	input := `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}` + "\n" + initializeLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s","prompt":[]}}` + "\n"
	for i := 0; i < 50; i++ {
		reached := make(chan string, 4)
		conn := NewAgentSideConnection(recordingAgent(reached, nil), io.Discard, strings.NewReader(input))
		select {
		case <-conn.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the connection did not end after its input")
		}
		if got := drain(reached); len(got) != 0 {
			t.Fatalf("session/cancel before initialize reached the Agent as %v", got)
		}
	}
}
