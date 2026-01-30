package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestConnectionInboundCancelRequest_CancelsHandler(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer func() {
		_ = inW.Close()
		_ = outW.Close()
		_ = inR.Close()
		_ = outR.Close()
	}()

	started := make(chan struct{})
	c := NewConnection(func(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
		close(started)
		<-ctx.Done()
		return nil, toReqErr(ctx.Err())
	}, outW, inR)
	_ = c

	lines := make(chan []byte, 10)
	go func() {
		scanner := bufio.NewScanner(outR)
		for scanner.Scan() {
			b := append([]byte(nil), scanner.Bytes()...)
			lines <- b
		}
		close(lines)
	}()

	// Send a request that will block until cancelled.
	_, err := inW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"test","params":{}}` + "\n"))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	// Cancel the in-flight request.
	_, err = inW.Write([]byte(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":1}}` + "\n"))
	if err != nil {
		t.Fatalf("write cancel notification: %v", err)
	}

	var raw []byte
	select {
	case raw = <-lines:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}

	var msg anyMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if msg.ID == nil {
		t.Fatalf("response missing id: %s", string(raw))
	}
	if got := string(*msg.ID); got != "1" {
		t.Fatalf("unexpected response id: %q", got)
	}
	if msg.Error == nil {
		t.Fatalf("expected error response, got: %s", string(raw))
	}
	if msg.Error.Code != -32800 {
		t.Fatalf("expected error code -32800, got %d (%s)", msg.Error.Code, msg.Error.Message)
	}
}

func TestConnectionOutboundCancelRequest_SendsNotification(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer func() {
		_ = inW.Close()
		_ = outW.Close()
		_ = inR.Close()
		_ = outR.Close()
	}()

	c := NewConnection(nil, outW, inR)

	lines := make(chan []byte, 10)
	go func() {
		scanner := bufio.NewScanner(outR)
		for scanner.Scan() {
			b := append([]byte(nil), scanner.Bytes()...)
			lines <- b
		}
		close(lines)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := SendRequest[json.RawMessage](c, ctx, "test/method", map[string]any{"x": 1})
		errCh <- err
	}()

	// First message should be the outbound request.
	var reqRaw []byte
	select {
	case reqRaw = <-lines:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for request")
	}

	var req anyMessage
	if err := json.Unmarshal(reqRaw, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.ID == nil {
		t.Fatalf("request missing id: %s", string(reqRaw))
	}
	if req.Method != "test/method" {
		t.Fatalf("unexpected request method: %q", req.Method)
	}
	idKey := string(*req.ID)

	// Cancel the outbound request context; this should trigger a best-effort $/cancel_request.
	cancel()

	var cancelRaw []byte
	select {
	case cancelRaw = <-lines:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancel notification")
	}

	var cancelMsg anyMessage
	if err := json.Unmarshal(cancelRaw, &cancelMsg); err != nil {
		t.Fatalf("unmarshal cancel notification: %v", err)
	}
	if cancelMsg.ID != nil {
		t.Fatalf("cancel notification unexpectedly had id: %s", string(cancelRaw))
	}
	if cancelMsg.Method != "$/cancel_request" {
		t.Fatalf("unexpected cancel method: %q", cancelMsg.Method)
	}

	var p cancelRequestParams
	if err := json.Unmarshal(cancelMsg.Params, &p); err != nil {
		t.Fatalf("unmarshal cancel params: %v", err)
	}
	if got := string(p.RequestID); got != idKey {
		t.Fatalf("unexpected cancel requestId: got %q want %q", got, idKey)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected request error")
		}
		re, ok := err.(*RequestError)
		if !ok {
			t.Fatalf("expected *RequestError, got %T: %v", err, err)
		}
		if re.Code != -32800 {
			t.Fatalf("expected error code -32800, got %d", re.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SendRequest to return")
	}
}
