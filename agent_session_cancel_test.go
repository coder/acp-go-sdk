package acp

import (
	"context"
	"io"
	"log/slog"
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
