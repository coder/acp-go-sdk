package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

type accessorCall struct {
	method string
	params json.RawMessage
}

// newRecordingPeer starts a bare Connection on the far end of a pipe pair. It
// answers every request with {"method": <the method it received>} and records
// each call, so a test can see exactly what reached the wire.
func newRecordingPeer(w io.Writer, r io.Reader) <-chan accessorCall {
	calls := make(chan accessorCall, 4)
	NewConnection(func(_ context.Context, method string, params json.RawMessage) (any, *RequestError) {
		calls <- accessorCall{method: method, params: params}
		return map[string]string{"method": method}, nil
	}, w, r)
	return calls
}

func newAccessorPipes(t *testing.T) (localR *io.PipeReader, localW *io.PipeWriter, peerR *io.PipeReader, peerW *io.PipeWriter) {
	t.Helper()
	peerR, localW = io.Pipe()
	localR, peerW = io.Pipe()
	t.Cleanup(func() {
		_ = localW.Close()
		_ = peerW.Close()
		_ = localR.Close()
		_ = peerR.Close()
	})
	return localR, localW, peerR, peerW
}

// assertRequestReachesPeer sends method through conn with SendRequest and
// checks the peer received that method and those params, and that its reply
// came back.
func assertRequestReachesPeer(t *testing.T, conn *Connection, calls <-chan accessorCall, method string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	params := map[string]string{"sessionId": "sess-1", "value": "v"}
	got, err := SendRequest[map[string]string](conn, ctx, method, params)
	if err != nil {
		t.Fatalf("SendRequest(%q) through Connection(): %v", method, err)
	}
	if got["method"] != method {
		t.Fatalf("reply = %v, want the peer's answer to %q", got, method)
	}

	select {
	case call := <-calls:
		if call.method != method {
			t.Fatalf("peer received method %q, want %q", call.method, method)
		}
		var gotParams map[string]string
		if err := json.Unmarshal(call.params, &gotParams); err != nil {
			t.Fatalf("peer params %s: %v", call.params, err)
		}
		if gotParams["sessionId"] != "sess-1" || gotParams["value"] != "v" {
			t.Fatalf("peer received params %v, want %v", gotParams, params)
		}
	case <-ctx.Done():
		t.Fatalf("peer never received %q", method)
	}
}

func TestClientSideConnection_ConnectionReachesThePeer(t *testing.T) {
	localR, localW, peerR, peerW := newAccessorPipes(t)
	c := NewClientSideConnection(&clientFuncs{}, localW, localR)
	calls := newRecordingPeer(peerW, peerR)

	conn := c.Connection()
	if conn == nil {
		t.Fatal("Connection() = nil")
	}
	if conn != c.conn {
		t.Fatal("Connection() is not the connection NewClientSideConnection built")
	}

	// The case the accessor exists for: a method without the "_" prefix,
	// which CallExtension refuses.
	const method = "session/set_model"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.CallExtension(ctx, method, nil); err == nil {
		t.Fatalf("CallExtension(%q) succeeded; expected it to refuse a non-extension method", method)
	}

	assertRequestReachesPeer(t, conn, calls, method)
}

func TestAgentSideConnection_ConnectionReachesThePeer(t *testing.T) {
	localR, localW, peerR, peerW := newAccessorPipes(t)
	a := NewAgentSideConnection(agentFuncs{}, localW, localR)
	calls := newRecordingPeer(peerW, peerR)

	conn := a.Connection()
	if conn == nil {
		t.Fatal("Connection() = nil")
	}
	if conn != a.conn {
		t.Fatal("Connection() is not the connection NewAgentSideConnection built")
	}

	const method = "client/unmodelled_method"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.CallExtension(ctx, method, nil); err == nil {
		t.Fatalf("CallExtension(%q) succeeded; expected it to refuse a non-extension method", method)
	}

	assertRequestReachesPeer(t, conn, calls, method)
}
