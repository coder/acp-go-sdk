package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestConnectionInboundCancelRequest_CanonicalizesEquivalentIDs(t *testing.T) {
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

	// Request id is encoded as a unicode escape sequence; cancel uses the canonical form.
	_, err := inW.Write([]byte(`{"jsonrpc":"2.0","id":"\u0061","method":"test","params":{}}` + "\n"))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	_, err = inW.Write([]byte(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":"a"}}` + "\n"))
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
	if msg.Error == nil {
		t.Fatalf("expected error response, got: %s", string(raw))
	}
	if msg.Error.Code != -32800 {
		t.Fatalf("expected error code -32800, got %d (%s)", msg.Error.Code, msg.Error.Message)
	}
}

func TestConnectionInboundCancelRequest_CanonicalizesEquivalentNumericIDs(t *testing.T) {
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

	// Request id uses exponent notation; cancel uses normalized integer notation.
	_, err := inW.Write([]byte(`{"jsonrpc":"2.0","id":1e0,"method":"test","params":{}}` + "\n"))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

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
	if msg.Error == nil {
		t.Fatalf("expected error response, got: %s", string(raw))
	}
	if msg.Error.Code != -32800 {
		t.Fatalf("expected error code -32800, got %d (%s)", msg.Error.Code, msg.Error.Message)
	}
}

func TestCanonicalJSONRPCIDKey_LargeNumericIDsDoNotCollide(t *testing.T) {
	id1 := json.RawMessage(`9007199254740992`)
	id2 := json.RawMessage(`9007199254740993`)

	key1, err := canonicalJSONRPCIDKey(id1)
	if err != nil {
		t.Fatalf("canonicalize id1: %v", err)
	}
	key2, err := canonicalJSONRPCIDKey(id2)
	if err != nil {
		t.Fatalf("canonicalize id2: %v", err)
	}

	if key1 != string(id1) {
		t.Fatalf("unexpected canonical id1: got %q want %q", key1, string(id1))
	}
	if key2 != string(id2) {
		t.Fatalf("unexpected canonical id2: got %q want %q", key2, string(id2))
	}
	if key1 == key2 {
		t.Fatalf("canonical keys collided: id1=%q id2=%q key=%q", id1, id2, key1)
	}
}

func TestCanonicalJSONRPCIDKey_NumericRepresentationsMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    json.RawMessage
		b    json.RawMessage
	}{
		{name: "integer exponent", a: json.RawMessage(`1`), b: json.RawMessage(`1e0`)},
		{name: "integer decimal", a: json.RawMessage(`1`), b: json.RawMessage(`1.0`)},
		{name: "fraction exponent", a: json.RawMessage(`0.1`), b: json.RawMessage(`1e-1`)},
		{name: "negative zero", a: json.RawMessage(`-0`), b: json.RawMessage(`0`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			keyA, err := canonicalJSONRPCIDKey(tc.a)
			if err != nil {
				t.Fatalf("canonicalize a: %v", err)
			}
			keyB, err := canonicalJSONRPCIDKey(tc.b)
			if err != nil {
				t.Fatalf("canonicalize b: %v", err)
			}
			if keyA != keyB {
				t.Fatalf("expected equivalent numeric ids to match: a=%q b=%q keyA=%q keyB=%q", tc.a, tc.b, keyA, keyB)
			}
		})
	}
}

func TestConnectionResponseID_CanonicalizesEquivalentNumericRepresentations(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer func() {
		_ = inW.Close()
		_ = outW.Close()
		_ = inR.Close()
		_ = outR.Close()
	}()

	c := NewConnection(nil, outW, inR)

	responderErr := make(chan error, 1)
	go func() {
		br := bufio.NewReader(outR)
		if _, err := br.ReadBytes('\n'); err != nil {
			responderErr <- fmt.Errorf("read outbound request: %w", err)
			return
		}
		if _, err := inW.Write([]byte(`{"jsonrpc":"2.0","id":1e0,"result":{"ok":true}}` + "\n")); err != nil {
			responderErr <- fmt.Errorf("write response: %w", err)
			return
		}
		responderErr <- nil
	}()

	result, err := SendRequest[map[string]bool](c, context.Background(), "test/method", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("SendRequest returned error: %v", err)
	}
	if !result["ok"] {
		t.Fatalf("unexpected response payload: %#v", result)
	}

	if err := <-responderErr; err != nil {
		t.Fatal(err)
	}
}

func TestConnectionInboundCancelRequest_ImmediateCancelNoRace(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer func() {
		_ = inW.Close()
		_ = outW.Close()
		_ = inR.Close()
		_ = outR.Close()
	}()

	c := NewConnection(func(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
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

	for i := 1; i <= 25; i++ {
		payload := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"test","params":{}}`+"\n"+
				`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":%d}}`+"\n",
			i, i,
		)
		if _, err := inW.Write([]byte(payload)); err != nil {
			t.Fatalf("write request/cancel pair %d: %v", i, err)
		}

		var raw []byte
		select {
		case raw = <-lines:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for response on iteration %d", i)
		}

		var msg anyMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal response on iteration %d: %v", i, err)
		}
		if msg.ID == nil {
			t.Fatalf("response missing id on iteration %d: %s", i, string(raw))
		}
		if got := string(*msg.ID); got != fmt.Sprintf("%d", i) {
			t.Fatalf("unexpected response id on iteration %d: got %q", i, got)
		}
		if msg.Error == nil {
			t.Fatalf("expected error response on iteration %d, got: %s", i, string(raw))
		}
		if msg.Error.Code != -32800 {
			t.Fatalf("expected error code -32800 on iteration %d, got %d (%s)", i, msg.Error.Code, msg.Error.Message)
		}
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

func TestConnectionOutboundCancelRequest_DoesNotBlockWhenPeerStopsReading(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer func() {
		_ = inW.Close()
		_ = outW.Close()
		_ = inR.Close()
		_ = outR.Close()
	}()

	c := NewConnection(nil, outW, inR)

	firstReq := make(chan []byte, 1)
	go func() {
		br := bufio.NewReader(outR)
		line, err := br.ReadBytes('\n')
		if err == nil {
			firstReq <- append([]byte(nil), line...)
		}
		close(firstReq)
		// Intentionally stop reading after the first request line.
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := SendRequest[json.RawMessage](c, ctx, "test/method", map[string]any{"x": 1})
		errCh <- err
	}()

	var reqRaw []byte
	select {
	case reqRaw = <-firstReq:
		if len(reqRaw) == 0 {
			t.Fatal("failed to read first outbound request")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first request")
	}

	var req anyMessage
	if err := json.Unmarshal(reqRaw, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.ID == nil {
		t.Fatalf("request missing id: %s", string(reqRaw))
	}

	// Peer is no longer reading. The best-effort cancel write may block in the background,
	// but SendRequest should still return promptly on context cancellation.
	cancel()

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
	case <-time.After(1 * time.Second):
		t.Fatal("SendRequest blocked on cancel notification write")
	}
}

func TestConnectionWaitForResponse_PeerDisconnectWinsOverDerivedContextCancel(t *testing.T) {
	const iterations = 64

	for i := 0; i < iterations; i++ {
		baseCtx, baseCancel := context.WithCancelCause(context.Background())
		c := &Connection{
			pending: make(map[string]*pendingResponse),
			ctx:     baseCtx,
			cancel:  baseCancel,
		}

		idKey := fmt.Sprintf("id-%d", i)
		pr := &pendingResponse{ch: make(chan anyMessage)}
		c.pending[idKey] = pr

		requestCtx, requestCancel := context.WithCancel(baseCtx)
		baseCancel(errors.New("peer closed"))

		_, err := c.waitForResponse(requestCtx, pr, idKey)
		requestCancel()

		if err == nil {
			t.Fatalf("iteration %d: expected error", i)
		}
		re, ok := err.(*RequestError)
		if !ok {
			t.Fatalf("iteration %d: expected *RequestError, got %T (%v)", i, err, err)
		}
		if re.Code != -32603 {
			t.Fatalf("iteration %d: expected disconnect error code -32603, got %d (%s)", i, re.Code, re.Message)
		}

		if _, ok := c.pending[idKey]; ok {
			t.Fatalf("iteration %d: pending request %q was not cleaned up", i, idKey)
		}
	}
}
