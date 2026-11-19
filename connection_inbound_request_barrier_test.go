package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type inboundBarrierTestWire struct {
	messages chan []byte
}

func newInboundBarrierTestWire() *inboundBarrierTestWire {
	return &inboundBarrierTestWire{messages: make(chan []byte, 32)}
}

func (w *inboundBarrierTestWire) Write(p []byte) (int, error) {
	w.messages <- append([]byte(nil), p...)
	return len(p), nil
}

type inboundBarrierTestPeer struct {
	input      *io.PipeWriter
	wire       *inboundBarrierTestWire
	connection *Connection
	pending    map[string]map[string]any
}

func newInboundBarrierTestPeer(t *testing.T, handler MethodHandler) *inboundBarrierTestPeer {
	t.Helper()
	reader, writer := io.Pipe()
	peer := &inboundBarrierTestPeer{
		input:   writer,
		wire:    newInboundBarrierTestWire(),
		pending: make(map[string]map[string]any),
	}
	peer.connection = NewConnection(handler, peer.wire, reader)
	peer.connection.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = writer.Close() })
	return peer
}

func (p *inboundBarrierTestPeer) send(t *testing.T, message any) {
	t.Helper()
	b, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.input.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func (p *inboundBarrierTestPeer) request(t *testing.T, id, method string) {
	t.Helper()
	p.send(t, map[string]any{"jsonrpc": "2.0", "id": id, "method": method})
}

func (p *inboundBarrierTestPeer) notification(t *testing.T, method string, params any) {
	t.Helper()
	p.send(t, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (p *inboundBarrierTestPeer) waitInflight(t *testing.T, id string) {
	t.Helper()
	idKeyBytes, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	idKey := string(idKeyBytes)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		p.connection.mu.Lock()
		_, ok := p.connection.inflight[idKey]
		p.connection.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("request %q did not become inflight", id)
		}
	}
}

func (p *inboundBarrierTestPeer) response(t *testing.T, id string) map[string]any {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	if message := p.pending[id]; message != nil {
		delete(p.pending, id)
		return message
	}
	for {
		select {
		case b := <-p.wire.messages:
			for _, line := range bytes.Split(bytes.TrimSpace(b), []byte{'\n'}) {
				var message map[string]any
				if err := json.Unmarshal(line, &message); err != nil {
					t.Fatal(err)
				}
				messageID, _ := message["id"].(string)
				if messageID == id {
					return message
				}
				if messageID != "" {
					p.pending[messageID] = message
				}
			}
		case <-timeout.C:
			t.Fatalf("response %q did not arrive", id)
		}
	}
}

func TestConnection_InboundRequestWaitsForPriorNotification(t *testing.T) {
	notificationStarted := make(chan struct{})
	releaseNotification := make(chan struct{})
	var releaseNotificationOnce sync.Once
	handlerStarted := make(chan struct{})
	peer := newInboundBarrierTestPeer(t, func(_ context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "notification":
			close(notificationStarted)
			<-releaseNotification
			return nil, nil
		case "request":
			close(handlerStarted)
			return map[string]bool{"ok": true}, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	})
	t.Cleanup(func() { releaseNotificationOnce.Do(func() { close(releaseNotification) }) })

	peer.notification(t, "notification", nil)
	<-notificationStarted
	peer.request(t, "request", "request")
	peer.waitInflight(t, "request")
	select {
	case <-handlerStarted:
		t.Fatal("request handler started before the prior notification completed")
	default:
	}
	releaseNotificationOnce.Do(func() { close(releaseNotification) })
	<-handlerStarted
	if response := peer.response(t, "request"); response["error"] != nil {
		t.Fatalf("response error = %#v", response["error"])
	}
}

func TestConnection_InboundRequestBarrierHonorsCancellation(t *testing.T) {
	notificationStarted := make(chan struct{})
	notificationDone := make(chan struct{})
	releaseNotification := make(chan struct{})
	var releaseNotificationOnce sync.Once
	handlerStarted := make(chan struct{})
	peer := newInboundBarrierTestPeer(t, func(_ context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "notification":
			close(notificationStarted)
			<-releaseNotification
			close(notificationDone)
			return nil, nil
		case "request":
			close(handlerStarted)
			return nil, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	})
	t.Cleanup(func() { releaseNotificationOnce.Do(func() { close(releaseNotification) }) })

	peer.notification(t, "notification", nil)
	<-notificationStarted
	peer.request(t, "request", "request")
	peer.waitInflight(t, "request")
	peer.notification(t, "$/cancel_request", map[string]any{"requestId": "request"})
	response := peer.response(t, "request")
	errorResponse, ok := response["error"].(map[string]any)
	if !ok || errorResponse["code"] != float64(-32800) {
		t.Fatalf("cancel response = %#v", response)
	}
	releaseNotificationOnce.Do(func() { close(releaseNotification) })
	<-notificationDone
	select {
	case <-handlerStarted:
		t.Fatal("cancelled request handler started after its notification barrier released")
	default:
	}
	select {
	case message := <-peer.wire.messages:
		t.Fatalf("unexpected second response: %s", message)
	default:
	}
}

// When the peer disconnects right after a $/cancel_request for a request that
// waits for an earlier notification handler, the waiting request can see the
// cancel or the disconnect first. On the cancel, the peer gets -32800 and the
// handler never runs. On the disconnect, the handler runs with a cancelled ctx.
// Either way, the handler never runs with a live ctx.
func TestConnection_InboundRequestBarrierCancelThenDisconnect(t *testing.T) {
	outcomes := map[string]int{}
	for i := 0; i < 50; i++ {
		releaseNotification := make(chan struct{})
		peer := newInboundBarrierTestPeer(t, func(ctx context.Context, method string, _ json.RawMessage) (any, *RequestError) {
			switch method {
			case "notification":
				<-releaseNotification
				return nil, nil
			case "request":
				return map[string]bool{"cancelled": ctx.Err() != nil}, nil
			default:
				return nil, NewMethodNotFound(method)
			}
		})
		peer.notification(t, "notification", nil)
		peer.request(t, "request", "request")
		peer.waitInflight(t, "request")
		peer.notification(t, "$/cancel_request", map[string]any{"requestId": "request"})
		_ = peer.input.Close()

		response := peer.response(t, "request")
		close(releaseNotification)
		if errorResponse, ok := response["error"].(map[string]any); ok {
			if errorResponse["code"] != float64(-32800) {
				t.Fatalf("run %d: error response = %#v, want code -32800", i, response)
			}
			outcomes["-32800"]++
			continue
		}
		if result, _ := response["result"].(map[string]any); result["cancelled"] != true {
			t.Fatalf("run %d: the handler ran with a live ctx: %#v", i, response)
		}
		outcomes["handler with cancelled ctx"]++
	}
	t.Logf("outcomes over 50 runs: %v", outcomes)
}

func TestConnection_InboundRequestDoesNotWaitForLaterNotification(t *testing.T) {
	handlerStarted := make(chan struct{})
	returnRequest := make(chan struct{})
	var returnRequestOnce sync.Once
	notificationStarted := make(chan struct{})
	releaseNotification := make(chan struct{})
	var releaseNotificationOnce sync.Once
	peer := newInboundBarrierTestPeer(t, func(_ context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "request":
			close(handlerStarted)
			<-returnRequest
			return map[string]bool{"ok": true}, nil
		case "notification":
			close(notificationStarted)
			<-releaseNotification
			return nil, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	})
	t.Cleanup(func() {
		returnRequestOnce.Do(func() { close(returnRequest) })
		releaseNotificationOnce.Do(func() { close(releaseNotification) })
	})

	peer.request(t, "request", "request")
	peer.waitInflight(t, "request")
	peer.notification(t, "notification", nil)
	<-notificationStarted
	<-handlerStarted
	returnRequestOnce.Do(func() { close(returnRequest) })
	if response := peer.response(t, "request"); response["error"] != nil {
		t.Fatalf("response error = %#v", response["error"])
	}
}

func TestConnection_InboundRequestsCrossBarrierConcurrently(t *testing.T) {
	notificationStarted := make(chan struct{})
	releaseNotification := make(chan struct{})
	handlerStarted := make(chan string, 2)
	releaseHandlers := make(chan struct{})
	var releaseNotificationOnce sync.Once
	var releaseHandlersOnce sync.Once
	peer := newInboundBarrierTestPeer(t, func(_ context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "notification":
			close(notificationStarted)
			<-releaseNotification
			return nil, nil
		case "request-1", "request-2":
			handlerStarted <- method
			<-releaseHandlers
			return map[string]string{"method": method}, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	})
	t.Cleanup(func() {
		releaseNotificationOnce.Do(func() { close(releaseNotification) })
		releaseHandlersOnce.Do(func() { close(releaseHandlers) })
	})

	peer.notification(t, "notification", nil)
	<-notificationStarted
	peer.request(t, "request-1", "request-1")
	peer.request(t, "request-2", "request-2")
	peer.waitInflight(t, "request-1")
	peer.waitInflight(t, "request-2")
	select {
	case method := <-handlerStarted:
		t.Fatalf("%s started before the prior notification completed", method)
	default:
	}
	releaseNotificationOnce.Do(func() { close(releaseNotification) })
	<-handlerStarted
	<-handlerStarted
	releaseHandlersOnce.Do(func() { close(releaseHandlers) })
	for _, id := range []string{"request-1", "request-2"} {
		if response := peer.response(t, id); response["error"] != nil {
			t.Fatalf("%s response error = %#v", id, response["error"])
		}
	}
}

// A slow notification handler delays the notifications and the requests that
// arrive after it, and they run in arrival order once it returns.
func TestConnection_SlowNotificationDelaysLaterMessages(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once
	started := make(chan string, 2)
	peer := newInboundBarrierTestPeer(t, func(_ context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "first":
			close(firstStarted)
			<-releaseFirst
			return nil, nil
		case "second", "request":
			started <- method
			return nil, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	})
	t.Cleanup(func() { releaseFirstOnce.Do(func() { close(releaseFirst) }) })

	peer.notification(t, "first", nil)
	<-firstStarted
	peer.notification(t, "second", nil)
	peer.request(t, "request", "request")
	// The connection reads messages in order, so once the request is in
	// flight, the second notification is queued too.
	peer.waitInflight(t, "request")
	select {
	case method := <-started:
		t.Fatalf("%s started while the first notification handler ran", method)
	case <-time.After(20 * time.Millisecond):
	}
	releaseFirstOnce.Do(func() { close(releaseFirst) })
	for _, want := range []string{"second", "request"} {
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("started %s, want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not start", want)
		}
	}
	if response := peer.response(t, "request"); response["error"] != nil {
		t.Fatalf("response error = %#v", response["error"])
	}
}

// A notification handler makes a request, and the peer answers it only after
// we answer the peer's own request. That request must not wait for the
// handler, or both sides wait forever.
func TestConnection_InboundRequestStartsWhileHandlerWaitsOnPeer(t *testing.T) {
	handlerDone := make(chan error, 1)
	var peer *inboundBarrierTestPeer
	peer = newInboundBarrierTestPeer(t, func(ctx context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "notification":
			_, err := SendRequest[map[string]bool](peer.connection, ctx, "_peer/ask", nil)
			handlerDone <- err
			return nil, nil
		case "callback":
			return map[string]bool{"ok": true}, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	})

	peer.notification(t, "notification", nil)
	askID := peer.outboundRequestID(t, "_peer/ask")
	peer.request(t, "callback", "callback")
	if response := peer.response(t, "callback"); response["error"] != nil {
		t.Fatalf("callback response error = %#v", response["error"])
	}
	peer.send(t, map[string]any{"jsonrpc": "2.0", "id": askID, "result": map[string]bool{"ok": true}})
	select {
	case err := <-handlerDone:
		if err != nil {
			t.Fatalf("nested request error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("notification handler did not get its response")
	}
}

// A notification handler makes a request, and the peer answers it only after
// we answer the peer's own request, whose handler makes a request with its
// ctx. The response to that request must not wait for the notification
// handler, or the two handlers and the peer wait for each other. The peer's
// request can arrive after the notification, and then it starts while the
// notification handler waits on the peer, or before it.
func TestConnection_RequestHandlerGetsResponseWhileHandlerWaitsOnPeer(t *testing.T) {
	for _, tt := range []struct {
		name              string
		notificationFirst bool
	}{
		{name: "request after notification", notificationFirst: true},
		{name: "request before notification", notificationFirst: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			aReader, bWriter := io.Pipe()
			bReader, aWriter := io.Pipe()
			t.Cleanup(func() {
				_ = aWriter.Close()
				_ = bWriter.Close()
			})
			discard := slog.New(slog.NewTextHandler(io.Discard, nil))

			callbackStarted := make(chan struct{})
			askArrived := make(chan struct{})
			callbackAnswered := make(chan struct{})
			notificationDone := make(chan error, 1)
			var a *Connection
			a = NewConnection(func(ctx context.Context, method string, _ json.RawMessage) (any, *RequestError) {
				switch method {
				case "notification":
					_, err := SendRequest[json.RawMessage](a, ctx, "ask", nil)
					notificationDone <- err
					return nil, nil
				case "callback":
					close(callbackStarted)
					select {
					case <-askArrived:
					case <-ctx.Done():
						return nil, toReqErr(ctx.Err())
					}
					if _, err := SendRequest[json.RawMessage](a, ctx, "lookup", nil); err != nil {
						return nil, toReqErr(err)
					}
					return map[string]bool{"ok": true}, nil
				default:
					return nil, NewMethodNotFound(method)
				}
			}, aWriter, aReader)
			a.SetLogger(discard)
			b := NewConnection(func(ctx context.Context, method string, _ json.RawMessage) (any, *RequestError) {
				switch method {
				case "ask":
					close(askArrived)
					select {
					case <-callbackAnswered:
						return map[string]bool{"ok": true}, nil
					case <-ctx.Done():
						return nil, toReqErr(ctx.Err())
					}
				case "lookup":
					return map[string]bool{"ok": true}, nil
				default:
					return nil, NewMethodNotFound(method)
				}
			}, bWriter, bReader)
			b.SetLogger(discard)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			notify := func() {
				if err := b.SendNotification(ctx, "notification", nil); err != nil {
					t.Fatal(err)
				}
			}
			if tt.notificationFirst {
				notify()
			}
			callbackDone := make(chan error, 1)
			go func() {
				_, err := SendRequest[json.RawMessage](b, ctx, "callback", nil)
				callbackDone <- err
			}()
			if !tt.notificationFirst {
				select {
				case <-callbackStarted:
				case <-ctx.Done():
					t.Fatal("callback handler did not start")
				}
				notify()
			}
			if err := <-callbackDone; err != nil {
				t.Fatalf("callback error = %v", err)
			}
			close(callbackAnswered)
			select {
			case err := <-notificationDone:
				if err != nil {
					t.Fatalf("notification handler's request error = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("notification handler did not get its response")
			}
		})
	}
}

// A ctx that outlives the handler of a request from the peer, such as one from
// context.WithoutCancel, gets the full wait: once the handler has returned, the
// peer no longer waits for its answer.
func TestConnection_RequestHandlerCtxGetsFullWaitAfterHandlerReturns(t *testing.T) {
	aReader, bWriter := io.Pipe()
	bReader, aWriter := io.Pipe()
	t.Cleanup(func() {
		_ = aWriter.Close()
		_ = bWriter.Close()
	})
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	handlerCtx := make(chan context.Context, 1)
	notificationDone := make(chan error, 1)
	var a *Connection
	a = NewConnection(func(ctx context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "notification":
			_, err := SendRequest[json.RawMessage](a, ctx, "ask", nil)
			notificationDone <- err
			return nil, nil
		case "callback":
			handlerCtx <- context.WithoutCancel(ctx)
			return map[string]bool{"ok": true}, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	}, aWriter, aReader)
	a.SetLogger(discard)
	askArrived := make(chan struct{})
	answerAsk := make(chan struct{})
	b := NewConnection(func(ctx context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "ask":
			close(askArrived)
			select {
			case <-answerAsk:
				return map[string]bool{"ok": true}, nil
			case <-ctx.Done():
				return nil, toReqErr(ctx.Err())
			}
		case "lookup":
			return map[string]bool{"ok": true}, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	}, bWriter, bReader)
	b.SetLogger(discard)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := SendRequest[json.RawMessage](b, ctx, "callback", nil); err != nil {
		t.Fatalf("callback error = %v", err)
	}
	outlivingCtx := <-handlerCtx
	if err := b.SendNotification(ctx, "notification", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-askArrived:
	case <-ctx.Done():
		t.Fatal("notification handler's request did not arrive")
	}
	lookupDone := make(chan error, 1)
	go func() {
		_, err := SendRequest[json.RawMessage](a, outlivingCtx, "lookup", nil)
		lookupDone <- err
	}()
	select {
	case err := <-lookupDone:
		t.Fatalf("request returned (err = %v) while the notification handler still ran", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(answerAsk)
	select {
	case err := <-lookupDone:
		if err != nil {
			t.Fatalf("lookup error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("lookup did not return")
	}
	select {
	case err := <-notificationDone:
		if err != nil {
			t.Fatalf("notification handler's request error = %v", err)
		}
	default:
		t.Fatal("request returned before the notification handler")
	}
}

// A request made with a ctx that outlives the handler of a request from the
// peer can start its wait while the handler runs. When the handler returns
// before the running notification handler starts to wait on the peer, the
// request gets the full wait.
func TestConnection_RequestHandlerCtxGetsFullWaitWhenHandlerReturnsDuringWait(t *testing.T) {
	aReader, bWriter := io.Pipe()
	bReader, aWriter := io.Pipe()
	t.Cleanup(func() {
		_ = aWriter.Close()
		_ = bWriter.Close()
	})
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	callbackStarted := make(chan struct{})
	startLookup := make(chan struct{})
	returnCallback := make(chan struct{})
	lookupDone := make(chan error, 1)
	notificationStarted := make(chan struct{})
	notificationAsks := make(chan struct{})
	notificationDone := make(chan error, 1)
	var a *Connection
	a = NewConnection(func(ctx context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "notification":
			close(notificationStarted)
			<-notificationAsks
			_, err := SendRequest[json.RawMessage](a, ctx, "ask", nil)
			notificationDone <- err
			return nil, nil
		case "callback":
			close(callbackStarted)
			<-startLookup
			outlivingCtx := context.WithoutCancel(ctx)
			go func() {
				_, err := SendRequest[json.RawMessage](a, outlivingCtx, "lookup", nil)
				lookupDone <- err
			}()
			<-returnCallback
			return map[string]bool{"ok": true}, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	}, aWriter, aReader)
	a.SetLogger(discard)
	askArrived := make(chan struct{})
	answerAsk := make(chan struct{})
	b := NewConnection(func(ctx context.Context, method string, _ json.RawMessage) (any, *RequestError) {
		switch method {
		case "ask":
			close(askArrived)
			select {
			case <-answerAsk:
				return map[string]bool{"ok": true}, nil
			case <-ctx.Done():
				return nil, toReqErr(ctx.Err())
			}
		case "lookup":
			return map[string]bool{"ok": true}, nil
		default:
			return nil, NewMethodNotFound(method)
		}
	}, bWriter, bReader)
	b.SetLogger(discard)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wait := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal(what)
		}
	}
	callbackDone := make(chan error, 1)
	go func() {
		_, err := SendRequest[json.RawMessage](b, ctx, "callback", nil)
		callbackDone <- err
	}()
	wait(callbackStarted, "callback handler did not start")
	if err := b.SendNotification(ctx, "notification", nil); err != nil {
		t.Fatal(err)
	}
	wait(notificationStarted, "notification handler did not start")
	// The lookup response arrives after the notification, so the lookup
	// waits for its handler, which does not wait on the peer yet.
	close(startLookup)
	waitForPendingRequests(t, a, 0, 5*time.Second)
	time.Sleep(20 * time.Millisecond)
	close(returnCallback)
	if err := <-callbackDone; err != nil {
		t.Fatalf("callback error = %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		a.mu.Lock()
		running := len(a.inflightArrivals)
		a.mu.Unlock()
		if running == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("callback handler did not leave c.inflightArrivals")
		}
	}
	close(notificationAsks)
	wait(askArrived, "notification handler's request did not arrive")
	select {
	case err := <-lookupDone:
		t.Fatalf("request returned (err = %v) while the notification handler still ran", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(answerAsk)
	select {
	case err := <-lookupDone:
		if err != nil {
			t.Fatalf("lookup error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("lookup did not return")
	}
	select {
	case err := <-notificationDone:
		if err != nil {
			t.Fatalf("notification handler's request error = %v", err)
		}
	default:
		t.Fatal("request returned before the notification handler")
	}
}

// outboundRequestID waits for our request with method and returns its id.
func (p *inboundBarrierTestPeer) outboundRequestID(t *testing.T, method string) any {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case b := <-p.wire.messages:
			for _, line := range bytes.Split(bytes.TrimSpace(b), []byte{'\n'}) {
				var message map[string]any
				if err := json.Unmarshal(line, &message); err != nil {
					t.Fatal(err)
				}
				if message["method"] == method && message["id"] != nil {
					return message["id"]
				}
				if messageID, ok := message["id"].(string); ok {
					p.pending[messageID] = message
				}
			}
		case <-timeout.C:
			t.Fatalf("request %q did not arrive", method)
		}
	}
}

// A request whose ctx ends as its barrier clears gets the cancel error, and
// its handler does not start.
func TestConnection_InboundBarrierReportsCancelAsBarrierClears(t *testing.T) {
	peer := newInboundBarrierTestPeer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := peer.connection.waitInboundBarrier(ctx, 0)
	var reqErr *RequestError
	if !errors.As(err, &reqErr) || reqErr.Code != -32800 {
		t.Fatalf("waitInboundBarrier error = %v, want code -32800", err)
	}
}
