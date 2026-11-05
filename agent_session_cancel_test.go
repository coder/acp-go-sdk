package acp

import (
	"bufio"
	"context"
	"encoding/json"
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

type cancelPeer struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan anyMessage
}

// newCancelPeer connects agent to a fake client that writes raw lines and
// reads every message the agent sends.
func newCancelPeer(t *testing.T, agent agentFuncs) (*AgentSideConnection, *cancelPeer) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	conn := NewAgentSideConnection(agent, outW, inR)
	conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := &cancelPeer{t: t, in: inW, lines: make(chan anyMessage, 16)}
	go func() {
		s := bufio.NewScanner(outR)
		for s.Scan() {
			var m anyMessage
			if json.Unmarshal(s.Bytes(), &m) == nil {
				p.lines <- m
			}
		}
	}()
	t.Cleanup(func() { _ = inW.Close(); _ = outW.Close() })
	return conn, p
}

func (p *cancelPeer) send(lines ...string) {
	p.t.Helper()
	if _, err := io.WriteString(p.in, strings.Join(lines, "\n")+"\n"); err != nil {
		p.t.Fatalf("SETUP: %v", err)
	}
}

// await returns the first message from the agent that match accepts.
func (p *cancelPeer) await(what string, match func(anyMessage) bool) anyMessage {
	p.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-p.lines:
			if match(m) {
				return m
			}
		case <-deadline:
			p.t.Fatalf("SETUP: timed out waiting for %s", what)
		}
	}
}

func waitCancelTest(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("SETUP: timed out waiting for %s", what)
	}
}

func cancelTestPromptLine(id int, text string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"session/prompt","params":{"sessionId":"s","prompt":[{"type":"text","text":"` + text + `"}]}}`
}

func cancelTestPromptText(p PromptRequest) string {
	if len(p.Prompt) == 1 && p.Prompt[0].Text != nil {
		return p.Prompt[0].Text.Text
	}
	return ""
}

// overlapping-prompts.cfg, CancelReachesEarlierPrompts (#83):
//  1. Prompt 1 registers and runs.
//  2. Prompt 2 of the same session registers. registerPrompt cancels prompt 1.
//  3. Prompt 1 returns. Its cleanup deletes a.sessionCancels["s"], which now
//     holds prompt 2's cancel func.
//  4. The client cancels the session. cancelPrompt finds no entry.
//
// Promise: prompt 2's ctx is canceled.
func TestAgentSideConnection_OverlappingPromptsKeepCancel(t *testing.T) {
	started := map[string]chan struct{}{"p1": make(chan struct{}), "p2": make(chan struct{})}
	p2Canceled := make(chan bool, 1)
	_, peer := newCancelPeer(t, agentFuncs{
		PromptFunc: func(ctx context.Context, p PromptRequest) (PromptResponse, error) {
			text := cancelTestPromptText(p)
			close(started[text])
			if text == "p1" {
				<-ctx.Done()
				return PromptResponse{StopReason: StopReasonCancelled}, nil
			}
			select {
			case <-ctx.Done():
				p2Canceled <- true
			case <-time.After(2 * time.Second):
				p2Canceled <- false
			}
			return PromptResponse{StopReason: StopReasonEndTurn}, nil
		},
	})

	peer.send(cancelTestPromptLine(1, "p1"))
	waitCancelTest(t, started["p1"], "prompt 1 to start")
	peer.send(cancelTestPromptLine(2, "p2"))
	waitCancelTest(t, started["p2"], "prompt 2 to start")
	// The response follows the cleanup in the session/prompt case.
	peer.await("prompt 1's response", func(m anyMessage) bool {
		return m.ID != nil && string(*m.ID) == "1" && m.Method == ""
	})
	peer.send(sessionCancelLine)
	if !<-p2Canceled {
		t.Fatal("session/cancel did not cancel prompt 2, which the client sent before it")
	}
}

// peer-wait-later-prompt.cfg, CancelSparesLaterPrompts (#72 item 1):
//  1. An extension notification's handler calls the client with its ctx and
//     waits for the answer (startPeerWait).
//  2. The client sends session/cancel, then a new session/prompt.
//  3. The peer-wait release starts the prompt before the cancel's handler runs.
//     The prompt registers.
//  4. The client answers. The notification handler returns, and cancelPrompt
//     cancels the registered prompt.
//
// Promise: the prompt, sent after the cancel, keeps a live ctx.
func TestAgentSideConnection_SessionCancelSparesPromptReleasedByPeerWait(t *testing.T) {
	var conn *AgentSideConnection
	started := make(chan struct{})
	// checkNow closes once the cancel handlers have returned, so the prompt
	// reads its ctx after any cancel that could reach it.
	checkNow := make(chan struct{})
	canceled := make(chan bool, 1)
	conn, peer := newCancelPeer(t, agentFuncs{
		HandleExtensionMethodFunc: func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
			if method == "_note" {
				_, err := conn.CallExtension(ctx, "_ask", map[string]any{})
				return nil, err
			}
			return nil, NewMethodNotFound(method)
		},
		PromptFunc: func(ctx context.Context, _ PromptRequest) (PromptResponse, error) {
			close(started)
			select {
			case <-checkNow:
			case <-time.After(5 * time.Second):
			}
			canceled <- ctx.Err() != nil
			return PromptResponse{StopReason: StopReasonEndTurn}, nil
		},
	})

	peer.send(`{"jsonrpc":"2.0","method":"_note","params":{}}`)
	ask := peer.await("the handler's request", func(m anyMessage) bool { return m.Method == "_ask" })
	peer.send(sessionCancelLine, cancelTestPromptLine(1, "after the cancel"))
	waitCancelTest(t, started, "the prompt to start")
	peer.send(`{"jsonrpc":"2.0","id":` + string(*ask.ID) + `,"result":{}}`)
	waitCompletedNotifications(t, conn.conn, 2)
	close(checkNow)
	if <-canceled {
		t.Fatal("session/cancel canceled a prompt that the client sent after it")
	}
}

// cancel-waits-later-prompt.cfg, CancelSparesLaterPrompts (#72 item 1, second
// trigger):
//  1. The client cancels the session. a.agent.Cancel calls the client with its
//     ctx and waits for the answer.
//  2. The client sends a second session/cancel, then a new session/prompt.
//  3. The peer-wait release starts the prompt before the second cancel's
//     handler runs. The prompt registers.
//  4. The client answers. The first cancel's handler returns, and the second
//     cancel's cancelPrompt cancels the registered prompt.
//
// Promise: the prompt, sent after both cancels, keeps a live ctx.
func TestAgentSideConnection_SessionCancelSparesPromptReleasedByCancelWait(t *testing.T) {
	var conn *AgentSideConnection
	cancels := 0
	started := make(chan struct{})
	// checkNow closes once the cancel handlers have returned, so the prompt
	// reads its ctx after any cancel that could reach it.
	checkNow := make(chan struct{})
	canceled := make(chan bool, 1)
	conn, peer := newCancelPeer(t, agentFuncs{
		CancelFunc: func(ctx context.Context, _ CancelNotification) error {
			cancels++
			if cancels == 1 {
				_, err := conn.CallExtension(ctx, "_ask", map[string]any{})
				return err
			}
			return nil
		},
		PromptFunc: func(ctx context.Context, _ PromptRequest) (PromptResponse, error) {
			close(started)
			select {
			case <-checkNow:
			case <-time.After(5 * time.Second):
			}
			canceled <- ctx.Err() != nil
			return PromptResponse{StopReason: StopReasonEndTurn}, nil
		},
	})

	peer.send(sessionCancelLine)
	ask := peer.await("the Cancel handler's request", func(m anyMessage) bool { return m.Method == "_ask" })
	peer.send(sessionCancelLine, cancelTestPromptLine(1, "after the cancels"))
	waitCancelTest(t, started, "the prompt to start")
	peer.send(`{"jsonrpc":"2.0","id":` + string(*ask.ID) + `,"result":{}}`)
	waitCompletedNotifications(t, conn.conn, 2)
	close(checkNow)
	if <-canceled {
		t.Fatal("session/cancel canceled a prompt that the client sent after it")
	}
}

// overlapping-prompts.cfg, NewerPromptSurvives: the client sends two prompts
// of one session back to back. Their handlers can register in either order.
// The older prompt's ctx ends, and the newer prompt keeps a live ctx.
func TestAgentSideConnection_NewerPromptWinsWhicheverRegistersFirst(t *testing.T) {
	for i := 0; i < 20; i++ {
		olderDone := make(chan struct{})
		olderCanceled := make(chan bool, 1)
		newerCanceled := make(chan bool, 1)
		_, peer := newCancelPeer(t, agentFuncs{
			PromptFunc: func(ctx context.Context, p PromptRequest) (PromptResponse, error) {
				if cancelTestPromptText(p) == "older" {
					select {
					case <-ctx.Done():
						olderCanceled <- true
					case <-time.After(2 * time.Second):
						olderCanceled <- false
					}
					close(olderDone)
					return PromptResponse{StopReason: StopReasonCancelled}, nil
				}
				waitCancelTest(t, olderDone, "the older prompt to return")
				newerCanceled <- ctx.Err() != nil
				return PromptResponse{StopReason: StopReasonEndTurn}, nil
			},
		})
		peer.send(cancelTestPromptLine(1, "older"), cancelTestPromptLine(2, "newer"))
		if !<-olderCanceled {
			t.Fatalf("run %d: the older prompt kept running after a newer prompt of its session arrived", i)
		}
		if <-newerCanceled {
			t.Fatalf("run %d: the newer prompt was canceled by the older one", i)
		}
	}
}
