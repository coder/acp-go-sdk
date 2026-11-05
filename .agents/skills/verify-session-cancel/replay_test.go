package acp

// Replays of the counterexamples that check.sh reports, against the real
// code. replay.sh copies this file into the package, runs it and removes it.
// Each test asserts the promise, so it fails while the finding is in the code.
// A failure that starts with "SETUP:" means that the path could not be
// replayed, not that the promise broke.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"
)

type replayPeer struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan anyMessage
}

// newReplayPeer connects agent to a fake client that writes raw lines and
// reads every message the agent sends.
func newReplayPeer(t *testing.T, agent agentFuncs) (*AgentSideConnection, *replayPeer) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	conn := NewAgentSideConnection(agent, outW, inR)
	conn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	p := &replayPeer{t: t, in: inW, lines: make(chan anyMessage, 16)}
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

func (p *replayPeer) send(lines ...string) {
	p.t.Helper()
	if _, err := io.WriteString(p.in, strings.Join(lines, "\n")+"\n"); err != nil {
		p.t.Fatalf("SETUP: %v", err)
	}
}

// await returns the first message from the agent that match accepts.
func (p *replayPeer) await(what string, match func(anyMessage) bool) anyMessage {
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

func replayWait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("SETUP: timed out waiting for %s", what)
	}
}

func replayPromptLine(id int, text string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"session/prompt","params":{"sessionId":"s","prompt":[{"type":"text","text":"` + text + `"}]}}`
}

const replayCancelLine = `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}`

func replayPromptText(p PromptRequest) string {
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
func TestReplay_OverlappingPromptsKeepCancel(t *testing.T) {
	started := map[string]chan struct{}{"p1": make(chan struct{}), "p2": make(chan struct{})}
	p2Canceled := make(chan bool, 1)
	_, peer := newReplayPeer(t, agentFuncs{
		PromptFunc: func(ctx context.Context, p PromptRequest) (PromptResponse, error) {
			text := replayPromptText(p)
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

	peer.send(replayPromptLine(1, "p1"))
	replayWait(t, started["p1"], "prompt 1 to start")
	peer.send(replayPromptLine(2, "p2"))
	replayWait(t, started["p2"], "prompt 2 to start")
	// The response follows the cleanup in the session/prompt case.
	peer.await("prompt 1's response", func(m anyMessage) bool {
		return m.ID != nil && string(*m.ID) == "1" && m.Method == ""
	})
	peer.send(replayCancelLine)
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
func TestReplay_PeerWaitReleaseSparesLaterPrompt(t *testing.T) {
	var conn *AgentSideConnection
	started := make(chan struct{})
	canceled := make(chan bool, 1)
	conn, peer := newReplayPeer(t, agentFuncs{
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
			case <-ctx.Done():
				canceled <- true
			case <-time.After(time.Second):
				canceled <- false
			}
			return PromptResponse{StopReason: StopReasonEndTurn}, nil
		},
	})

	peer.send(`{"jsonrpc":"2.0","method":"_note","params":{}}`)
	ask := peer.await("the handler's request", func(m anyMessage) bool { return m.Method == "_ask" })
	peer.send(replayCancelLine, replayPromptLine(1, "after the cancel"))
	replayWait(t, started, "the prompt to start")
	peer.send(`{"jsonrpc":"2.0","id":` + string(*ask.ID) + `,"result":{}}`)
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
func TestReplay_CancelHandlerWaitSparesLaterPrompt(t *testing.T) {
	var conn *AgentSideConnection
	cancels := 0
	started := make(chan struct{})
	canceled := make(chan bool, 1)
	conn, peer := newReplayPeer(t, agentFuncs{
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
			case <-ctx.Done():
				canceled <- true
			case <-time.After(time.Second):
				canceled <- false
			}
			return PromptResponse{StopReason: StopReasonEndTurn}, nil
		},
	})

	peer.send(replayCancelLine)
	ask := peer.await("the Cancel handler's request", func(m anyMessage) bool { return m.Method == "_ask" })
	peer.send(replayCancelLine, replayPromptLine(1, "after the cancels"))
	replayWait(t, started, "the prompt to start")
	peer.send(`{"jsonrpc":"2.0","id":` + string(*ask.ID) + `,"result":{}}`)
	if <-canceled {
		t.Fatal("session/cancel canceled a prompt that the client sent after it")
	}
}
