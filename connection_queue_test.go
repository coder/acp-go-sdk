package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type asyncMessageReader struct {
	ch  chan []byte
	buf []byte
}

func newAsyncMessageReader(capacity int) *asyncMessageReader {
	return &asyncMessageReader{ch: make(chan []byte, capacity)}
}

func (r *asyncMessageReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		next, ok := <-r.ch
		if !ok {
			return 0, io.EOF
		}
		r.buf = next
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *asyncMessageReader) send(msg []byte) {
	r.ch <- msg
}

func (r *asyncMessageReader) close() {
	close(r.ch)
}

func sessionUpdateNotificationLine(t *testing.T, seq int) []byte {
	t.Helper()

	params, err := json.Marshal(SessionNotification{
		SessionId: SessionId("test-session"),
		Update:    UpdateAgentMessageText(fmt.Sprintf("update-%d", seq)),
	})
	if err != nil {
		t.Fatalf("marshal session/update params: %v", err)
	}

	line, err := json.Marshal(anyMessage{
		JSONRPC: "2.0",
		Method:  ClientMethodSessionUpdate,
		Params:  params,
	})
	if err != nil {
		t.Fatalf("marshal session/update notification: %v", err)
	}
	return append(line, '\n')
}

func TestConnectionMaxQueuedNotificationsOption(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		c := NewConnection(func(context.Context, string, json.RawMessage) (any, *RequestError) {
			return nil, nil
		}, io.Discard, strings.NewReader(""))
		if got := cap(c.notificationQueue); got != defaultMaxQueuedNotifications {
			t.Fatalf("default notification queue cap = %d, want %d", got, defaultMaxQueuedNotifications)
		}
		if got := c.maxQueuedNotifications; got != defaultMaxQueuedNotifications {
			t.Fatalf("default max queued notifications = %d, want %d", got, defaultMaxQueuedNotifications)
		}
	})

	t.Run("non_positive_falls_back_to_default", func(t *testing.T) {
		c := NewConnection(func(context.Context, string, json.RawMessage) (any, *RequestError) {
			return nil, nil
		}, io.Discard, strings.NewReader(""), WithMaxQueuedNotifications(0))
		if got := cap(c.notificationQueue); got != defaultMaxQueuedNotifications {
			t.Fatalf("fallback notification queue cap = %d, want %d", got, defaultMaxQueuedNotifications)
		}
	})

	t.Run("agent_and_client_constructors_apply_option", func(t *testing.T) {
		clientConn := NewClientSideConnection(&clientFuncs{}, io.Discard, strings.NewReader(""), WithMaxQueuedNotifications(7))
		if got := cap(clientConn.conn.notificationQueue); got != 7 {
			t.Fatalf("client notification queue cap = %d, want 7", got)
		}

		agentConn := NewAgentSideConnection(&agentFuncs{}, io.Discard, strings.NewReader(""), WithMaxQueuedNotifications(9))
		if got := cap(agentConn.conn.notificationQueue); got != 9 {
			t.Fatalf("agent notification queue cap = %d, want 9", got)
		}
	})
}

func TestClientSideConnectionNotificationBurstQueueCapacity(t *testing.T) {
	const totalNotifications = defaultMaxQueuedNotifications + 128

	t.Run("default_capacity_overflows", func(t *testing.T) {
		reader := newAsyncMessageReader(totalNotifications)
		defer reader.close()

		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		var delivered atomic.Int64

		c := NewClientSideConnection(&clientFuncs{
			SessionUpdateFunc: func(context.Context, SessionNotification) error {
				if delivered.Add(1) == 1 {
					close(firstStarted)
					<-releaseFirst
				}
				return nil
			},
		}, io.Discard, reader)

		reader.send(sessionUpdateNotificationLine(t, 0))
		select {
		case <-firstStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for first session/update handler")
		}

		producerDone := make(chan struct{})
		go func() {
			defer close(producerDone)
			for i := 1; i < totalNotifications; i++ {
				reader.send(sessionUpdateNotificationLine(t, i))
			}
		}()

		select {
		case <-producerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for notification producer")
		}

		select {
		case <-c.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for connection cancellation on queue overflow")
		}

		if cause := context.Cause(c.conn.ctx); !errors.Is(cause, ErrNotificationQueueOverflow) {
			t.Fatalf("connection cancellation cause = %v, want %v", cause, ErrNotificationQueueOverflow)
		}

		close(releaseFirst)
	})

	t.Run("configured_capacity_delivers_all_notifications", func(t *testing.T) {
		reader := newAsyncMessageReader(totalNotifications)
		defer reader.close()

		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		var delivered atomic.Int64

		c := NewClientSideConnection(&clientFuncs{
			SessionUpdateFunc: func(context.Context, SessionNotification) error {
				if delivered.Add(1) == 1 {
					close(firstStarted)
					<-releaseFirst
				}
				return nil
			},
		}, io.Discard, reader, WithMaxQueuedNotifications(totalNotifications*2))

		reader.send(sessionUpdateNotificationLine(t, 0))
		select {
		case <-firstStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for first session/update handler")
		}

		producerDone := make(chan struct{})
		go func() {
			defer close(producerDone)
			for i := 1; i < totalNotifications; i++ {
				reader.send(sessionUpdateNotificationLine(t, i))
			}
		}()

		select {
		case <-producerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for notification producer")
		}

		select {
		case <-c.Done():
			t.Fatalf("connection closed while burst was queued: %v", context.Cause(c.conn.ctx))
		case <-time.After(50 * time.Millisecond):
		}

		close(releaseFirst)
		waitForDeliveredNotifications(t, &delivered, totalNotifications)

		select {
		case <-c.Done():
			t.Fatalf("connection closed after delivering burst: %v", context.Cause(c.conn.ctx))
		default:
		}
	})
}

func waitForDeliveredNotifications(t *testing.T, delivered *atomic.Int64, want int64) {
	t.Helper()

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if got := delivered.Load(); got == want {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("delivered %d notifications, want %d", delivered.Load(), want)
		case <-ticker.C:
		}
	}
}
