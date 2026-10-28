package acp

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	sessionCancelLine = `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}`
	sessionPromptLine = `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s","prompt":[{"type":"text","text":"hi"}]}}`
)

// A session/cancel cancels the prompts of its session that arrived before it,
// whichever handler runs first. Before this rule, the cancel handler could run
// before the prompt handler registered its cancel func, and the cancel was lost.
func TestAgentSideConnection_SessionCancelCancelsEarlierPrompt(t *testing.T) {
	tests := []struct {
		name string
		// lines go to the agent in one write.
		lines []string
		// holdFirstCancel blocks the first Cancel call until every line is
		// queued, so the prompt waits behind it.
		holdFirstCancel bool
		wantCancelled   bool
	}{
		{name: "cancel right after prompt", lines: []string{sessionPromptLine, sessionCancelLine}, wantCancelled: true},
		{name: "prompt held behind an earlier cancel", lines: []string{sessionCancelLine, sessionPromptLine, sessionCancelLine}, holdFirstCancel: true, wantCancelled: true},
		{name: "cancel before prompt", lines: []string{sessionCancelLine, sessionPromptLine}, wantCancelled: false},
	}
	for _, tt := range tests {
		tt := tt // go.mod is go 1.21: handlers of one subtest can outlive it.
		t.Run(tt.name, func(t *testing.T) {
			promptCancelled := make(chan bool, 1)
			var conn *AgentSideConnection
			var cancelCalls sync.Once
			agent := agentFuncs{
				CancelFunc: func(context.Context, CancelNotification) error {
					cancelCalls.Do(func() {
						if tt.holdFirstCancel {
							waitEnqueuedNotifications(t, conn.conn, 2)
						}
					})
					return nil
				},
				PromptFunc: func(ctx context.Context, _ PromptRequest) (PromptResponse, error) {
					if tt.wantCancelled {
						select {
						case <-ctx.Done():
						case <-time.After(5 * time.Second):
						}
					}
					promptCancelled <- ctx.Err() != nil
					return PromptResponse{StopReason: StopReasonEndTurn}, nil
				},
			}
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = writer.Close() })
			conn = NewAgentSideConnection(agent, io.Discard, reader)
			conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))

			if _, err := io.WriteString(writer, strings.Join(tt.lines, "\n")+"\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-promptCancelled:
				if got != tt.wantCancelled {
					t.Fatalf("prompt ctx cancelled = %v, want %v", got, tt.wantCancelled)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("prompt handler did not run")
			}
		})
	}
}

// waitEnqueuedNotifications waits until c has queued n notifications.
func waitEnqueuedNotifications(t *testing.T, c *Connection, n uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.notifyMu.Lock()
		got := c.lastEnqueuedNotificationSeq
		c.notifyMu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("queued %d notifications, want %d", got, n)
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// A cancel marker lives only while a request that arrived before its cancel
// is in flight.
func TestAgentSideConnection_SessionCancelMarkersArePruned(t *testing.T) {
	promptStarted := make(chan struct{})
	releasePrompt := make(chan struct{})
	var releasePromptOnce sync.Once
	agent := agentFuncs{
		PromptFunc: func(context.Context, PromptRequest) (PromptResponse, error) {
			close(promptStarted)
			<-releasePrompt
			return PromptResponse{StopReason: StopReasonEndTurn}, nil
		},
	}
	reader, writer := io.Pipe()
	output := newInboundBarrierTestWire()
	t.Cleanup(func() {
		releasePromptOnce.Do(func() { close(releasePrompt) })
		_ = writer.Close()
	})
	conn := NewAgentSideConnection(agent, output, reader)
	conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	send := func(lines ...string) {
		t.Helper()
		if _, err := io.WriteString(writer, strings.Join(lines, "\n")+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	markers := func() int {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return len(conn.sessionCancelledAt)
	}
	cancelFor := func(sessionID string) string {
		return `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"` + sessionID + `"}}`
	}

	send(cancelFor("idle-1"), cancelFor("idle-2"))
	waitCompletedNotifications(t, conn.conn, 2)
	if got := markers(); got != 0 {
		t.Fatalf("markers with no request in flight = %d, want 0", got)
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s","prompt":[{"type":"text","text":"hi"}]}}`)
	<-promptStarted
	send(cancelFor("other"))
	waitCompletedNotifications(t, conn.conn, 3)
	if got := markers(); got != 1 {
		t.Fatalf("markers while an earlier prompt is in flight = %d, want 1", got)
	}

	sent := uint64(3)
	for sent < 3+maxCancelMarkers+10 {
		var batch []string
		for i := 0; i < 100; i++ {
			batch = append(batch, cancelFor("flood-"+strconv.FormatUint(sent, 10)))
			sent++
		}
		send(batch...)
		waitCompletedNotifications(t, conn.conn, sent)
	}
	conn.mu.Lock()
	got := len(conn.sessionCancelledAt)
	_, newestKept := conn.sessionCancelledAt["flood-"+strconv.FormatUint(sent-1, 10)]
	_, oldestKept := conn.sessionCancelledAt["other"]
	conn.mu.Unlock()
	if got != maxCancelMarkers || !newestKept || oldestKept {
		t.Fatalf("after a flood: markers = %d (want %d), newest kept = %v, oldest kept = %v", got, maxCancelMarkers, newestKept, oldestKept)
	}

	releasePromptOnce.Do(func() { close(releasePrompt) })
	waitNoInflightRequests(t, conn.conn)
	send(cancelFor("idle-3"))
	waitCompletedNotifications(t, conn.conn, sent+1)
	if got := markers(); got != 0 {
		t.Fatalf("markers after the prompt returned = %d, want 0", got)
	}
}

// waitCompletedNotifications waits until the handlers of n notifications of c
// have returned.
func waitCompletedNotifications(t *testing.T, c *Connection, n uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.notifyMu.Lock()
		got := c.completedNotificationSeq
		c.notifyMu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("completed %d notifications, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitNoInflightRequests waits until no request from the peer is in flight on c.
func waitNoInflightRequests(t *testing.T, c *Connection) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := c.oldestInflightWatermark(); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("requests still in flight")
		}
		time.Sleep(time.Millisecond)
	}
}
