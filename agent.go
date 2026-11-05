package acp

import (
	"context"
	"io"
	"log/slog"
	"sync"
)

// AgentSideConnection represents the agent's view of a connection to a client.
type AgentSideConnection struct {
	conn  *Connection
	agent Agent

	mu             sync.Mutex
	sessionCancels map[string]*promptCancel
	// sessionCancelledAt holds, per session, the sequence number of the last
	// session/cancel. A prompt that arrived before that cancel and registers
	// after it is cancelled at once. pruneCancelMarkers drops an entry once no
	// request from the peer that arrived before its cancel is in flight.
	sessionCancelledAt map[string]uint64
}

// NewAgentSideConnection creates a new agent-side connection bound to the
// provided Agent implementation.
func NewAgentSideConnection(agent Agent, peerInput io.Writer, peerOutput io.Reader) *AgentSideConnection {
	asc := &AgentSideConnection{}
	asc.agent = agent
	asc.sessionCancels = make(map[string]*promptCancel)
	asc.sessionCancelledAt = make(map[string]uint64)
	asc.conn = NewConnection(asc.handleWithExtensions, peerInput, peerOutput)
	return asc
}

// Done exposes a channel that closes when the peer disconnects.
func (c *AgentSideConnection) Done() <-chan struct{} { return c.conn.Done() }

// SetLogger directs connection diagnostics to the provided logger.
func (c *AgentSideConnection) SetLogger(l *slog.Logger) { c.conn.SetLogger(l) }

// promptCancel is the entry of a running prompt in sessionCancels.
type promptCancel struct {
	cancel context.CancelFunc
	// watermark is the requestWatermark of the prompt, when known. A
	// session/cancel with a seq at or below it arrived after the prompt.
	watermark    uint64
	hasWatermark bool
	// arrival is the requestArrival of the prompt, valid with hasWatermark.
	arrival uint64
}

// cancelPrompt handles a session/cancel for sessionID that runs with ctx. It
// cancels the prompt that has registered for the session, unless that prompt
// arrived after the cancel, and records where the cancel arrived, for a prompt
// that arrived before it but has not registered yet.
func (a *AgentSideConnection) cancelPrompt(ctx context.Context, sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	seq, hasSeq := a.conn.notificationSeq(ctx)
	if pc, ok := a.sessionCancels[sessionID]; ok && !(hasSeq && pc.hasWatermark && pc.watermark >= seq) {
		pc.cancel()
		delete(a.sessionCancels, sessionID)
	}
	if hasSeq {
		a.sessionCancelledAt[sessionID] = seq
	}
	a.pruneCancelMarkers()
}

// maxCancelMarkers bounds sessionCancelledAt while a long request is in
// flight. Only the peer that sends the cancels can push a marker out, and a
// lost marker only loses that peer's own cancel.
const maxCancelMarkers = 1024

// registerPrompt makes cancel the cancel func of the prompt for sessionID
// whose handler runs with ctx, and cancels the prompt of the session that it
// replaces. When a prompt of the session that arrived later has registered
// first, that prompt stays and registerPrompt calls cancel at once. If a
// session/cancel that arrived after the prompt has already run, registerPrompt
// also calls cancel at once. The prompt
// handler calls the returned func when the prompt returns. It removes the
// entry only while the entry is still this prompt's, so a newer prompt of the
// session stays cancellable.
func (a *AgentSideConnection) registerPrompt(ctx context.Context, sessionID string, cancel context.CancelFunc) (done func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	watermark, hasWatermark := a.conn.requestWatermark(ctx)
	arrival, _ := a.conn.requestArrival(ctx)
	pc := &promptCancel{cancel: cancel, watermark: watermark, hasWatermark: hasWatermark, arrival: arrival}
	if prev, ok := a.sessionCancels[sessionID]; ok {
		if hasWatermark && prev.hasWatermark && prev.arrival > arrival {
			cancel()
			return func() {}
		}
		prev.cancel()
	}
	a.sessionCancels[sessionID] = pc
	if cancelledAt, ok := a.sessionCancelledAt[sessionID]; ok {
		if hasWatermark && cancelledAt > watermark {
			cancel()
		}
	}
	a.pruneCancelMarkers()
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.sessionCancels[sessionID] == pc {
			delete(a.sessionCancels, sessionID)
		}
	}
}

// pruneCancelMarkers drops each entry of sessionCancelledAt that no request in
// flight can match. Only a prompt that arrived before the cancel matches it.
// Above maxCancelMarkers entries, it drops the oldest cancels first. The caller
// holds a.mu.
func (a *AgentSideConnection) pruneCancelMarkers() {
	oldest, ok := a.conn.oldestInflightWatermark()
	for sessionID, cancelledAt := range a.sessionCancelledAt {
		if !ok || oldest >= cancelledAt {
			delete(a.sessionCancelledAt, sessionID)
		}
	}
	for len(a.sessionCancelledAt) > maxCancelMarkers {
		var oldestID string
		var oldestSeq uint64
		found := false
		for sessionID, cancelledAt := range a.sessionCancelledAt {
			if !found || cancelledAt < oldestSeq {
				oldestID, oldestSeq, found = sessionID, cancelledAt, true
			}
		}
		delete(a.sessionCancelledAt, oldestID)
	}
}
