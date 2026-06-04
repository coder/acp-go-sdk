package httpserver

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubAgent is a minimal acp.Agent used by server tests. It tracks a
// per-connection sessionId counter so the agent looks realistic end-to-end.
type stubAgent struct {
	sessionCounter atomic.Uint64
	conn           *acp.AgentSideConnection
	promptCalls    atomic.Uint64
}

func (a *stubAgent) Initialize(ctx context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "stub-agent", Version: "0.0.1"},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: acp.PromptCapabilities{
				Image:           false,
				Audio:           false,
				EmbeddedContext: true,
			},
		},
	}, nil
}

func (a *stubAgent) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	n := a.sessionCounter.Add(1)
	return acp.NewSessionResponse{SessionId: acp.SessionId(fmt.Sprintf("sess-%d", n))}, nil
}

func (a *stubAgent) LoadSession(ctx context.Context, req acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	return acp.LoadSessionResponse{}, nil
}

func (a *stubAgent) ResumeSession(ctx context.Context, req acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}

func (a *stubAgent) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}

func (a *stubAgent) Authenticate(ctx context.Context, req acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *stubAgent) Logout(ctx context.Context, req acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (a *stubAgent) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	a.promptCalls.Add(1)
	// Emit one session update notification so session-scoped streaming
	// has something to exercise.
	if a.conn != nil {
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: req.SessionId,
			Update: acp.SessionUpdate{
				AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "hello"}},
				},
			},
		})
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *stubAgent) Cancel(ctx context.Context, params acp.CancelNotification) error {
	return nil
}

func (a *stubAgent) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func (a *stubAgent) ListSessions(ctx context.Context, params acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (a *stubAgent) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}

// startServer spins up a test HTTP server around a new server.Server with
// the given factory, and returns the base URL and a cleanup.
func startServer(t *testing.T, factory AgentFactory) (baseURL string, stop func()) {
	t.Helper()
	srv, err := New(Config{Factory: factory})
	require.NoError(t, err)

	httpSrv := httptest.NewServer(srv.Handler())
	return httpSrv.URL + "/acp", func() {
		_ = srv.Close()
		httpSrv.Close()
	}
}

// TestInitializeRoundTrip confirms that initialize is synchronous and
// returns both an Acp-Connection-Id header and a JSON-RPC response body.
func TestInitializeRoundTrip(t *testing.T) {
	base, stop := startServer(t, func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		a := &stubAgent{}
		return a, func(c *acp.AgentSideConnection) { a.conn = c }, nil, nil
	})
	defer stop()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2,"clientInfo":{"name":"t","version":"0"},"clientCapabilities":{}}}`
	req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader(body))
	req.Header.Set("Content-Type", mimeJSON)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	connID := resp.Header.Get(HeaderConnectionID)
	require.NotEmpty(t, connID)

	got, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(got), `"id":1`)
	assert.Contains(t, string(got), `"agentInfo"`)
}

// TestPostWithoutConnectionIDIs400 ensures the transport rejects
// post-initialize messages that forget the connection id.
func TestPostWithoutConnectionIDIs400(t *testing.T) {
	base, stop := startServer(t, func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		return &stubAgent{}, nil, nil, nil
	})
	defer stop()

	req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`))
	req.Header.Set("Content-Type", mimeJSON)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestBatchRequestsRejected verifies JSON-RPC batch arrays return 501.
func TestBatchRequestsRejected(t *testing.T) {
	base, stop := startServer(t, func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		return &stubAgent{}, nil, nil, nil
	})
	defer stop()

	req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader(`[{"jsonrpc":"2.0","id":1,"method":"initialize"}]`))
	req.Header.Set("Content-Type", mimeJSON)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
}

// TestFullLifecycle drives a server through an end-to-end flow: initialize
// → open connection-scoped GET → POST session/new → receive response on
// the connection-scoped stream → open session-scoped GET → POST
// session/prompt → receive session/update notification + response on the
// session-scoped stream → DELETE.
func TestFullLifecycle(t *testing.T) {
	var agent *stubAgent
	base, stop := startServer(t, func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		agent = &stubAgent{}
		return agent, func(c *acp.AgentSideConnection) { agent.conn = c }, nil, nil
	})
	defer stop()

	client := &http.Client{Timeout: 10 * time.Second}

	// --- initialize ---
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2}}`
	req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader(initReq))
	req.Header.Set("Content-Type", mimeJSON)
	resp, err := client.Do(req)
	require.NoError(t, err)
	connID := resp.Header.Get(HeaderConnectionID)
	resp.Body.Close()
	require.NotEmpty(t, connID)

	// --- connection-scoped GET ---
	connStreamEvents := openStream(t, base, connID, "")
	defer connStreamEvents.close()

	// --- session/new ---
	newReq := `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`
	req, _ = http.NewRequest(http.MethodPost, base, strings.NewReader(newReq))
	req.Header.Set("Content-Type", mimeJSON)
	req.Header.Set(HeaderConnectionID, connID)
	resp, err = client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	// The session/new response should arrive on the connection-scoped stream.
	ev := connStreamEvents.waitFor(t, 2*time.Second)
	assert.Contains(t, ev, `"id":2`)
	assert.Contains(t, ev, `"sessionId":"sess-1"`)

	// --- open session-scoped GET ---
	sessionStreamEvents := openStream(t, base, connID, "sess-1")
	defer sessionStreamEvents.close()

	// --- session/prompt ---
	promptReq := `{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[]}}`
	req, _ = http.NewRequest(http.MethodPost, base, strings.NewReader(promptReq))
	req.Header.Set("Content-Type", mimeJSON)
	req.Header.Set(HeaderConnectionID, connID)
	req.Header.Set(HeaderSessionID, "sess-1")
	resp, err = client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	// Expect: one session/update notification, then the response to id:3.
	seen := 0
	seenUpdate, seenResponse := false, false
	for seen < 2 {
		ev := sessionStreamEvents.waitFor(t, 2*time.Second)
		if strings.Contains(ev, "session/update") {
			seenUpdate = true
		}
		if strings.Contains(ev, `"id":3`) {
			seenResponse = true
		}
		seen++
	}
	assert.True(t, seenUpdate, "expected session/update on session-scoped stream")
	assert.True(t, seenResponse, "expected response to id:3 on session-scoped stream")

	// --- DELETE ---
	req, _ = http.NewRequest(http.MethodDelete, base, nil)
	req.Header.Set(HeaderConnectionID, connID)
	resp, err = client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()
}

// TestSessionStreamReplaysPreSubscribeBuffer exercises the pre-subscribe
// buffer in outboundStream: messages emitted on a session-scoped stream
// BEFORE the client opens its session-scoped GET must be buffered and
// replayed when the GET finally arrives. TestFullLifecycle opens the GET
// first, so it never touches this path. Here we POST session/prompt without
// opening the session GET, confirm (via internal state) that the resulting
// update + response land in the pre-subscribe buffer, then open the GET and
// assert both are replayed.
func TestSessionStreamReplaysPreSubscribeBuffer(t *testing.T) {
	var agent *stubAgent
	srv, err := New(Config{Factory: func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		agent = &stubAgent{}
		return agent, func(c *acp.AgentSideConnection) { agent.conn = c }, nil, nil
	}})
	require.NoError(t, err)
	httpSrv := httptest.NewServer(srv.Handler())
	defer func() {
		_ = srv.Close()
		httpSrv.Close()
	}()
	base := httpSrv.URL + "/acp"

	client := &http.Client{Timeout: 10 * time.Second}

	// initialize
	req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2}}`))
	req.Header.Set("Content-Type", mimeJSON)
	resp, err := client.Do(req)
	require.NoError(t, err)
	connID := resp.Header.Get(HeaderConnectionID)
	resp.Body.Close()
	require.NotEmpty(t, connID)

	// connection-scoped GET to receive the session/new response.
	connStream := openStream(t, base, connID, "")
	defer connStream.close()

	// session/new
	req, _ = http.NewRequest(http.MethodPost, base, strings.NewReader(
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`))
	req.Header.Set("Content-Type", mimeJSON)
	req.Header.Set(HeaderConnectionID, connID)
	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	ev := connStream.waitFor(t, 2*time.Second)
	require.Contains(t, ev, `"sessionId":"sess-1"`)

	// session/prompt WITHOUT opening the session-scoped GET first.
	req, _ = http.NewRequest(http.MethodPost, base, strings.NewReader(
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[]}}`))
	req.Header.Set("Content-Type", mimeJSON)
	req.Header.Set(HeaderConnectionID, connID)
	req.Header.Set(HeaderSessionID, "sess-1")
	resp, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	// Wait until the agent's update + the prompt response have both been
	// routed into the session stream's pre-subscribe buffer (no subscriber is
	// attached yet). Inspecting internal state makes the test deterministic
	// without sleeping on the agent goroutine.
	require.Eventually(t, func() bool {
		conn := srv.getConn(connID)
		if conn == nil {
			return false
		}
		v, ok := conn.sessionStreams.Load("sess-1")
		if !ok {
			return false
		}
		st := v.(*outboundStream)
		st.mu.Lock()
		n := len(st.preBuffer)
		st.mu.Unlock()
		return n >= 2
	}, 2*time.Second, 10*time.Millisecond, "update + response should buffer before any subscriber attaches")

	// Now open the session-scoped GET; it must replay the buffered messages.
	sessionStream := openStream(t, base, connID, "sess-1")
	defer sessionStream.close()

	seenUpdate, seenResponse := false, false
	for i := 0; i < 2; i++ {
		ev := sessionStream.waitFor(t, 2*time.Second)
		if strings.Contains(ev, "session/update") {
			seenUpdate = true
		}
		if strings.Contains(ev, `"id":3`) {
			seenResponse = true
		}
	}
	assert.True(t, seenUpdate, "buffered session/update should replay on subscribe")
	assert.True(t, seenResponse, "buffered prompt response should replay on subscribe")
}

// TestMaxConnectionsRejectsWith503 verifies the MaxConnections cap: once the
// limit is reached, further initialize POSTs are rejected with 503 before the
// factory runs, and a slot frees up after the connection is DELETEd.
func TestMaxConnectionsRejectsWith503(t *testing.T) {
	var factoryCalls atomic.Uint64
	srv, err := New(Config{
		MaxConnections: 1,
		Factory: func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
			factoryCalls.Add(1)
			a := &stubAgent{}
			return a, func(c *acp.AgentSideConnection) { a.conn = c }, nil, nil
		},
	})
	require.NoError(t, err)
	httpSrv := httptest.NewServer(srv.Handler())
	defer func() {
		_ = srv.Close()
		httpSrv.Close()
	}()
	base := httpSrv.URL + "/acp"
	client := &http.Client{Timeout: 10 * time.Second}

	initialize := func() *http.Response {
		req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2}}`))
		req.Header.Set("Content-Type", mimeJSON)
		resp, err := client.Do(req)
		require.NoError(t, err)
		return resp
	}

	// First initialize succeeds and consumes the only slot.
	resp := initialize()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	connID := resp.Header.Get(HeaderConnectionID)
	resp.Body.Close()
	require.NotEmpty(t, connID)

	// Second initialize is rejected with 503 and never reaches the factory.
	resp = initialize()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	resp.Body.Close()
	assert.Equal(t, uint64(1), factoryCalls.Load(), "factory must not run when the cap is hit")

	// DELETE frees the slot; a subsequent initialize succeeds again.
	req, _ := http.NewRequest(http.MethodDelete, base, nil)
	req.Header.Set(HeaderConnectionID, connID)
	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	require.Eventually(t, func() bool {
		resp := initialize()
		ok := resp.StatusCode == http.StatusOK
		resp.Body.Close()
		return ok
	}, 2*time.Second, 20*time.Millisecond, "slot should free up after DELETE")
}

// TestSessionLoadResponseGoesToConnectionStream verifies that session/load
// responses land on the connection-scoped stream, per the RFD: "Connection-
// Scoped Stream ... Carries responses to session/new, session/load." This
// holds even though the POST itself is session-scoped (it carries
// Acp-Session-Id), because the client may not yet have opened the
// session-scoped GET when it issues session/load.
func TestSessionLoadResponseGoesToConnectionStream(t *testing.T) {
	base, stop := startServer(t, func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		a := &stubAgent{}
		return a, func(c *acp.AgentSideConnection) { a.conn = c }, nil, nil
	})
	defer stop()

	client := &http.Client{Timeout: 10 * time.Second}

	// initialize
	req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2}}`))
	req.Header.Set("Content-Type", mimeJSON)
	resp, err := client.Do(req)
	require.NoError(t, err)
	connID := resp.Header.Get(HeaderConnectionID)
	resp.Body.Close()
	require.NotEmpty(t, connID)

	connStream := openStream(t, base, connID, "")
	defer connStream.close()
	sessionStream := openStream(t, base, connID, "sess-loaded")
	defer sessionStream.close()

	// POST session/load with both headers; the spec considers session/load
	// session-scoped, but its response must land on the connection stream.
	req, _ = http.NewRequest(http.MethodPost, base, strings.NewReader(
		`{"jsonrpc":"2.0","id":7,"method":"session/load","params":{"sessionId":"sess-loaded","cwd":"/tmp","mcpServers":[]}}`))
	req.Header.Set("Content-Type", mimeJSON)
	req.Header.Set(HeaderConnectionID, connID)
	req.Header.Set(HeaderSessionID, "sess-loaded")
	resp, err = client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	ev := connStream.waitFor(t, 2*time.Second)
	assert.Contains(t, ev, `"id":7`, "session/load response must arrive on connection-scoped stream")

	// Negative check: no copy of the response leaks onto the session stream.
	select {
	case got := <-sessionStream.events:
		if strings.Contains(got, `"id":7`) {
			t.Fatalf("session/load response should not appear on session stream; got %s", got)
		}
	case <-time.After(150 * time.Millisecond):
	}
}

// TestSpuriousSessionHeaderDoesNotDivertConnectionResponse verifies that a
// non-session-scoped method (session/new) carrying an Acp-Session-Id header
// still has its response delivered on the connection-scoped stream. Routing
// is gated on IsSessionScoped rather than the raw header, so a malformed or
// adversarial POST cannot push a response onto a session stream the client
// is not waiting on.
func TestSpuriousSessionHeaderDoesNotDivertConnectionResponse(t *testing.T) {
	base, stop := startServer(t, func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		a := &stubAgent{}
		return a, func(c *acp.AgentSideConnection) { a.conn = c }, nil, nil
	})
	defer stop()

	client := &http.Client{Timeout: 10 * time.Second}

	req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2}}`))
	req.Header.Set("Content-Type", mimeJSON)
	resp, err := client.Do(req)
	require.NoError(t, err)
	connID := resp.Header.Get(HeaderConnectionID)
	resp.Body.Close()
	require.NotEmpty(t, connID)

	connStream := openStream(t, base, connID, "")
	defer connStream.close()
	bogusStream := openStream(t, base, connID, "bogus-sess")
	defer bogusStream.close()

	// session/new is NOT session-scoped, but we attach a spurious
	// Acp-Session-Id header anyway.
	req, _ = http.NewRequest(http.MethodPost, base, strings.NewReader(
		`{"jsonrpc":"2.0","id":9,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`))
	req.Header.Set("Content-Type", mimeJSON)
	req.Header.Set(HeaderConnectionID, connID)
	req.Header.Set(HeaderSessionID, "bogus-sess")
	resp, err = client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	ev := connStream.waitFor(t, 2*time.Second)
	assert.Contains(t, ev, `"id":9`, "session/new response must arrive on the connection-scoped stream")

	// Negative check: the response must not leak onto the spuriously named
	// session stream.
	select {
	case got := <-bogusStream.events:
		if strings.Contains(got, `"id":9`) {
			t.Fatalf("session/new response should not appear on the session stream; got %s", got)
		}
	case <-time.After(150 * time.Millisecond):
	}
}

// TestDeleteUnknownConnectionIs404 verifies the 404 path on DELETE.
func TestDeleteUnknownConnectionIs404(t *testing.T) {
	base, stop := startServer(t, func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		return &stubAgent{}, nil, nil, nil
	})
	defer stop()

	req, _ := http.NewRequest(http.MethodDelete, base, nil)
	req.Header.Set(HeaderConnectionID, "not-a-real-connection")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestGetWithoutAcceptIs406 verifies content-negotiation on GET.
func TestGetWithoutAcceptIs406(t *testing.T) {
	base, stop := startServer(t, func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		return &stubAgent{}, nil, nil, nil
	})
	defer stop()

	req, _ := http.NewRequest(http.MethodGet, base, nil)
	req.Header.Set(HeaderConnectionID, "whatever")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotAcceptable, resp.StatusCode)
}

// ---- SSE helpers ----

type sseTap struct {
	cancel chan struct{}
	events chan string
}

func openStream(t *testing.T, base, connID, sessionID string) *sseTap {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", mimeSSE)
	req.Header.Set(HeaderConnectionID, connID)
	if sessionID != "" {
		req.Header.Set(HeaderSessionID, sessionID)
	}

	// Use a client that never times out so the SSE stream can live forever.
	// Test cleanup closes the underlying body.
	tr := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
	client := &http.Client{Transport: tr}

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	tap := &sseTap{
		cancel: make(chan struct{}),
		events: make(chan string, 32),
	}

	go func() {
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		var buf bytes.Buffer
		for scanner.Scan() {
			line := strings.TrimRight(scanner.Text(), "\r")
			if line == "" {
				if buf.Len() > 0 {
					select {
					case tap.events <- buf.String():
					case <-tap.cancel:
						return
					}
					buf.Reset()
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				val := strings.TrimPrefix(line, "data:")
				val = strings.TrimPrefix(val, " ")
				if buf.Len() > 0 {
					buf.WriteByte('\n')
				}
				buf.WriteString(val)
			}
		}
	}()

	return tap
}

func (s *sseTap) waitFor(t *testing.T, timeout time.Duration) string {
	t.Helper()
	select {
	case ev := <-s.events:
		return ev
	case <-time.After(timeout):
		t.Fatalf("SSE: waited %s for an event but none arrived", timeout)
		return ""
	}
}

func (s *sseTap) close() {
	select {
	case <-s.cancel:
		// already closed
	default:
		close(s.cancel)
	}
}
