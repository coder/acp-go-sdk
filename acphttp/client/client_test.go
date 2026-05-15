package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// fakeServer is a minimal in-process ACP-streamable-HTTP server used to
// exercise the client. It speaks the exact header and status-code contract
// the RFD defines, but the "agent" logic is driven by the test itself via
// the `events` channel.
type fakeServer struct {
	t *testing.T

	mu         sync.Mutex
	connID     string
	sessions   map[string]bool
	gotHeaders []http.Header
	posts      []json.RawMessage

	// connEvents is the connection-scoped outbound queue.
	connEvents chan string
	// sessionEvents maps sessionId → session-scoped outbound queue.
	sessionEvents map[string]chan string

	// deleteCalled flips true after a DELETE /acp.
	deleteCalled chan struct{}
}

func newFakeServer(t *testing.T) *fakeServer {
	return &fakeServer{
		t:             t,
		sessions:      make(map[string]bool),
		connEvents:    make(chan string, 64),
		sessionEvents: make(map[string]chan string),
		deleteCalled:  make(chan struct{}, 1),
	}
}

func (s *fakeServer) sessionChan(id string) chan string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.sessionEvents[id]
	if !ok {
		ch = make(chan string, 64)
		s.sessionEvents[id] = ch
	}
	return ch
}

func (s *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/acp", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			s.handlePOST(w, r)
		case http.MethodGet:
			s.handleGET(w, r)
		case http.MethodDelete:
			s.handleDELETE(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func (s *fakeServer) handlePOST(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	require.NoError(s.t, err)

	s.mu.Lock()
	s.posts = append(s.posts, append([]byte(nil), body...))
	s.gotHeaders = append(s.gotHeaders, r.Header.Clone())
	s.mu.Unlock()

	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	if msg.Method == "initialize" {
		s.mu.Lock()
		if s.connID == "" {
			s.connID = "conn-test-1"
		}
		connID := s.connID
		s.mu.Unlock()

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(msg.ID),
			"result": map[string]any{
				"protocolVersion": 2,
				"agentInfo":       map[string]any{"name": "fake", "version": "0"},
				"agentCapabilities": map[string]any{
					"promptCapabilities": map[string]any{"image": false, "audio": false, "embeddedContext": true},
				},
			},
		}
		b, _ := json.Marshal(resp)

		w.Header().Set(headerConnectionID, connID)
		w.Header().Set("Content-Type", mimeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(b)
		return
	}

	// Non-initialize POST: validate headers.
	if r.Header.Get(headerConnectionID) == "" {
		http.Error(w, "missing "+headerConnectionID, http.StatusBadRequest)
		return
	}
	switch msg.Method {
	case "session/prompt", "session/cancel", "session/load", "session/set_mode", "session/set_model":
		if r.Header.Get(headerSessionID) == "" {
			http.Error(w, "missing "+headerSessionID, http.StatusBadRequest)
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *fakeServer) handleGET(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.Header.Get("Accept"), mimeSSE) {
		http.Error(w, "not acceptable", http.StatusNotAcceptable)
		return
	}
	connID := r.Header.Get(headerConnectionID)
	if connID == "" {
		http.Error(w, "missing "+headerConnectionID, http.StatusBadRequest)
		return
	}
	sessionID := r.Header.Get(headerSessionID)

	w.Header().Set("Content-Type", mimeSSE)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set(headerConnectionID, connID)
	if sessionID != "" {
		w.Header().Set(headerSessionID, sessionID)
	}
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	var events chan string
	if sessionID == "" {
		events = s.connEvents
	} else {
		events = s.sessionChan(sessionID)
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", ev)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func (s *fakeServer) handleDELETE(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(headerConnectionID) == "" {
		http.Error(w, "missing "+headerConnectionID, http.StatusBadRequest)
		return
	}
	select {
	case s.deleteCalled <- struct{}{}:
	default:
	}
	w.WriteHeader(http.StatusAccepted)
}

// sendConnEvent pushes one SSE event onto the connection-scoped stream.
func (s *fakeServer) sendConnEvent(payload string) {
	s.connEvents <- payload
}

// sendSessionEvent pushes one SSE event onto the session-scoped stream,
// blocking briefly if no stream has been opened yet (which lets tests
// exercise the pre-subscribe buffering expectations).
func (s *fakeServer) sendSessionEvent(sessionID, payload string) {
	s.sessionChan(sessionID) <- payload
}

// startH2CServer wraps fakeServer in an httptest server that speaks h2c
// (cleartext HTTP/2), matching how a local dev deployment of the goose
// reference server is expected to be configured.
func startH2CServer(t *testing.T, s *fakeServer) (baseURL string, stop func()) {
	h2s := &http2.Server{}
	srv := httptest.NewUnstartedServer(h2c.NewHandler(s.handler(), h2s))
	srv.EnableHTTP2 = true
	srv.Start()
	return srv.URL + "/acp", srv.Close
}

// ---- Tests ----

func TestDial_RequiresURL(t *testing.T) {
	_, err := Dial(context.Background(), Config{})
	require.Error(t, err)
}

func TestDial_NormalizesBareOrigin(t *testing.T) {
	tr, err := Dial(context.Background(), Config{BaseURL: "http://example.com"})
	require.NoError(t, err)
	defer tr.Close()
	assert.Equal(t, "http://example.com/acp", tr.url)
}

func TestInitializeRoundTrip(t *testing.T) {
	fs := newFakeServer(t)
	base, stop := startH2CServer(t, fs)
	defer stop()

	tr, err := Dial(context.Background(), Config{BaseURL: base})
	require.NoError(t, err)
	defer tr.Close()

	// Write an initialize request line-delimited just like the SDK does.
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2}}`
	_, err = tr.Write([]byte(initReq + "\n"))
	require.NoError(t, err)

	// Read one line back — it should be the initialize response.
	line := readOneLine(t, tr, 2*time.Second)
	assert.Contains(t, line, `"id":1`)
	assert.Contains(t, line, `"agentInfo"`)

	// The transport should have stored the connection id.
	assert.Equal(t, "conn-test-1", tr.getConnID())

	// Verify the server received exactly one POST and its body matches.
	fs.mu.Lock()
	require.Len(t, fs.posts, 1)
	assert.JSONEq(t, initReq, string(fs.posts[0]))
	fs.mu.Unlock()
}

func TestSessionNewResponseOnConnStreamOpensSessionStream(t *testing.T) {
	fs := newFakeServer(t)
	base, stop := startH2CServer(t, fs)
	defer stop()

	tr, err := Dial(context.Background(), Config{BaseURL: base})
	require.NoError(t, err)
	defer tr.Close()

	// initialize
	_, err = tr.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"))
	require.NoError(t, err)
	_ = readOneLine(t, tr, 2*time.Second)

	// Client sends session/new via POST.
	_, err = tr.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp"}}` + "\n"))
	require.NoError(t, err)

	// Server delivers the session/new response on the connection-scoped stream.
	fs.sendConnEvent(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"sess-xyz"}}`)

	got := readOneLine(t, tr, 2*time.Second)
	assert.Contains(t, got, `"sessionId":"sess-xyz"`)

	// Shortly after, we expect the transport to have opened the
	// session-scoped GET stream automatically.
	require.Eventually(t, func() bool {
		tr.sessionsMu.Lock()
		defer tr.sessionsMu.Unlock()
		_, ok := tr.sessionGets["sess-xyz"]
		return ok
	}, 2*time.Second, 10*time.Millisecond, "session stream should open after session/new response")

	// Events on the session stream should flow to the reader.
	fs.sendSessionEvent("sess-xyz", `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-xyz","update":{}}}`)
	got = readOneLine(t, tr, 2*time.Second)
	assert.Contains(t, got, `session/update`)
}

func TestSessionPromptSendsRequiredHeaders(t *testing.T) {
	fs := newFakeServer(t)
	base, stop := startH2CServer(t, fs)
	defer stop()

	tr, err := Dial(context.Background(), Config{BaseURL: base})
	require.NoError(t, err)
	defer tr.Close()

	_, err = tr.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"))
	require.NoError(t, err)
	_ = readOneLine(t, tr, 2*time.Second)

	// Send session/prompt referencing a sessionId that the transport has
	// never seen before. The transport should still set Acp-Session-Id
	// from the params and open the session-scoped GET stream before the
	// POST so no notifications are missed.
	prompt := `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"sess-abc","prompt":[]}}`
	_, err = tr.Write([]byte(prompt + "\n"))
	require.NoError(t, err)

	fs.mu.Lock()
	require.GreaterOrEqual(t, len(fs.posts), 2)
	// find the session/prompt request header entry
	var promptHdrs http.Header
	for i, raw := range fs.posts {
		if bytes.Contains(raw, []byte(`"session/prompt"`)) {
			promptHdrs = fs.gotHeaders[i]
			break
		}
	}
	fs.mu.Unlock()

	require.NotNil(t, promptHdrs, "server should have received session/prompt")
	assert.Equal(t, "conn-test-1", promptHdrs.Get(headerConnectionID))
	assert.Equal(t, "sess-abc", promptHdrs.Get(headerSessionID))
}

func TestCloseSendsDelete(t *testing.T) {
	fs := newFakeServer(t)
	base, stop := startH2CServer(t, fs)
	defer stop()

	tr, err := Dial(context.Background(), Config{BaseURL: base})
	require.NoError(t, err)

	_, err = tr.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"))
	require.NoError(t, err)
	_ = readOneLine(t, tr, 2*time.Second)

	require.NoError(t, tr.Close())

	select {
	case <-fs.deleteCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive DELETE /acp within timeout")
	}
}

func TestCustomHeadersAreSent(t *testing.T) {
	fs := newFakeServer(t)
	base, stop := startH2CServer(t, fs)
	defer stop()

	tr, err := Dial(context.Background(), Config{
		BaseURL: base,
		Headers: map[string]string{"X-Auth": "secret"},
	})
	require.NoError(t, err)
	defer tr.Close()

	_, err = tr.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"))
	require.NoError(t, err)
	_ = readOneLine(t, tr, 2*time.Second)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	require.NotEmpty(t, fs.gotHeaders)
	assert.Equal(t, "secret", fs.gotHeaders[0].Get("X-Auth"))
}

func TestParseSSE_MultipleDataLinesAreJoined(t *testing.T) {
	body := "event: message\ndata: hello\ndata: world\n\n"
	var got []string
	err := parseSSE(strings.NewReader(body), func(eventType, data string) {
		got = append(got, data)
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"hello\nworld"}, got)
}

func TestParseSSE_IgnoresCommentsAndRetries(t *testing.T) {
	body := ": heartbeat\nretry: 1000\ndata: {\"ok\":1}\n\n"
	var got []string
	err := parseSSE(strings.NewReader(body), func(eventType, data string) {
		got = append(got, data)
	})
	require.NoError(t, err)
	assert.Equal(t, []string{`{"ok":1}`}, got)
}

// readOneLine reads from r until it sees one complete newline-terminated
// line or the deadline expires. It reads one byte at a time to avoid
// over-reading into a buffer that subsequent test calls might need.
func readOneLine(t *testing.T, r io.Reader, timeout time.Duration) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		one := make([]byte, 1)
		for {
			n, err := r.Read(one)
			if n > 0 {
				if one[0] == '\n' {
					break
				}
				buf.WriteByte(one[0])
			}
			if err != nil {
				if err != io.EOF {
					t.Logf("readOneLine error: %v", err)
				}
				break
			}
		}
		ch <- buf.String()
	}()
	select {
	case line := <-ch:
		return line
	case <-time.After(timeout):
		t.Fatalf("readOneLine timed out after %s", timeout)
		return ""
	}
}
