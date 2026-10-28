package acp

import (
	"bytes"
	"context"
	"encoding/json"
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
