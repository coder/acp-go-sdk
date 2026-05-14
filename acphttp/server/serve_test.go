package httpserver

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

// TestH2CHandler_AcceptsHTTP1 confirms the h2c wrapper does not break
// HTTP/1.1 clients (we serve HTTP/1.1 requests transparently while still
// accepting h2c upgrades for HTTP/2 clients).
func TestH2CHandler_AcceptsHTTP1(t *testing.T) {
	stub := &stubAgent{}
	srv, err := New(Config{Factory: func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		return stub, nil, nil, nil
	}})
	require.NoError(t, err)
	defer srv.Close()

	httpSrv := srv.BuildHTTPServer()
	defer httpSrv.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() { _ = httpSrv.Serve(ln) }()

	// Plain HTTP/1.1 client: initialize should still 200 OK.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+"/acp", strings.NewReader(body))
	req.Header.Set("Content-Type", mimeJSON)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "HTTP/1.1", resp.Proto) // confirm it was actually 1.1
	io.Copy(io.Discard, resp.Body)
}

// TestH2CHandler_AcceptsH2CPriorKnowledge confirms HTTP/2 prior-knowledge
// clients (`curl --http2-prior-knowledge`) are upgraded by the h2c
// wrapper.
func TestH2CHandler_AcceptsH2CPriorKnowledge(t *testing.T) {
	srv, err := New(Config{Factory: func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		return &stubAgent{}, nil, nil, nil
	}})
	require.NoError(t, err)
	defer srv.Close()

	httpSrv := srv.BuildHTTPServer()
	defer httpSrv.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = httpSrv.Serve(ln) }()

	// HTTP/2 prior-knowledge: dial cleartext and speak h2 directly.
	client := &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+"/acp", strings.NewReader(body))
	req.Header.Set("Content-Type", mimeJSON)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "HTTP/2.0", resp.Proto)
}

// TestServe_GracefulShutdownOnContextCancel verifies that Serve exits
// cleanly when its context is cancelled and tears down the underlying
// connections via Close().
func TestServe_GracefulShutdownOnContextCancel(t *testing.T) {
	srv, err := New(Config{Factory: func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		return &stubAgent{}, nil, nil, nil
	}})
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	// Give Serve a moment to start serving before cancelling.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return within 3s of context cancellation")
	}
}
