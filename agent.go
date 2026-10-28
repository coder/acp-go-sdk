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
	sessionCancels map[string]context.CancelFunc
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
	asc.sessionCancels = make(map[string]context.CancelFunc)
	asc.sessionCancelledAt = make(map[string]uint64)
	asc.conn = NewConnection(asc.handleWithExtensions, peerInput, peerOutput)
	return asc
}

// Done exposes a channel that closes when the peer disconnects.
func (c *AgentSideConnection) Done() <-chan struct{} { return c.conn.Done() }

// SetLogger directs connection diagnostics to the provided logger.
func (c *AgentSideConnection) SetLogger(l *slog.Logger) { c.conn.SetLogger(l) }

// cancelPrompt handles a session/cancel for sessionID that runs with ctx. It
// cancels the prompt that has registered for the session, and records where
// the cancel arrived, for a prompt that arrived before it but has not
// registered yet.
func (a *AgentSideConnection) cancelPrompt(ctx context.Context, sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cn, ok := a.sessionCancels[sessionID]; ok {
		cn()
		delete(a.sessionCancels, sessionID)
	}
	if seq, ok := a.conn.notificationSeq(ctx); ok {
		a.sessionCancelledAt[sessionID] = seq
	}
	a.pruneCancelMarkers()
}

// maxCancelMarkers bounds sessionCancelledAt while a long request is in
// flight. Only the peer that sends the cancels can push a marker out, and a
// lost marker only loses that peer's own cancel.
const maxCancelMarkers = 1024

// registerPrompt makes cancel the cancel func of the prompt for sessionID
// whose handler runs with ctx. If a session/cancel that arrived after the
// prompt has already run, registerPrompt calls cancel at once.
func (a *AgentSideConnection) registerPrompt(ctx context.Context, sessionID string, cancel context.CancelFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if prev, ok := a.sessionCancels[sessionID]; ok {
		prev()
	}
	a.sessionCancels[sessionID] = cancel
	if cancelledAt, ok := a.sessionCancelledAt[sessionID]; ok {
		if watermark, ok := a.conn.requestWatermark(ctx); ok && cancelledAt > watermark {
			cancel()
		}
	}
	a.pruneCancelMarkers()
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
