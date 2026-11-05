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
	// sessionNewest holds, per session, the arrival number of the newest
	// prompt that has registered. A prompt that arrived before it and
	// registers later is cancelled at once, even after the newer prompt
	// returned. pruneCancelMarkers drops an entry once no request from the
	// peer that arrived before it is in flight.
	sessionNewest map[string]uint64
}

// NewAgentSideConnection creates a new agent-side connection bound to the
// provided Agent implementation.
func NewAgentSideConnection(agent Agent, peerInput io.Writer, peerOutput io.Reader) *AgentSideConnection {
	asc := &AgentSideConnection{}
	asc.agent = agent
	asc.sessionCancels = make(map[string]*promptCancel)
	asc.sessionCancelledAt = make(map[string]uint64)
	asc.sessionNewest = make(map[string]uint64)
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

// maxCancelMarkers bounds sessionCancelledAt and sessionNewest while a long
// request is in flight. Only the peer that sends the cancels and prompts can
// push an entry out, and a lost entry only loses that peer's own cancel or
// prompt order.
const maxCancelMarkers = 1024

// registerPrompt makes cancel the cancel func of the prompt for sessionID
// whose handler runs with ctx, and cancels the prompt of the session that it
// replaces. When a prompt of the session that arrived later has already
// registered, even one that has returned, registerPrompt calls cancel at once
// and leaves the session's entry alone. If a session/cancel that arrived
// after the prompt has already run, registerPrompt also calls cancel at once.
// The prompt handler calls the returned func when the prompt returns. It
// removes the entry only while the entry is still this prompt's, so a newer
// prompt of the session stays cancellable.
func (a *AgentSideConnection) registerPrompt(ctx context.Context, sessionID string, cancel context.CancelFunc) (done func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	watermark, hasWatermark := a.conn.requestWatermark(ctx)
	arrival, hasArrival := a.conn.requestArrival(ctx)
	if newest, ok := a.sessionNewest[sessionID]; ok && hasArrival && newest > arrival {
		cancel()
		return func() {}
	}
	if hasArrival {
		a.sessionNewest[sessionID] = arrival
	}
	pc := &promptCancel{cancel: cancel, watermark: watermark, hasWatermark: hasWatermark}
	if prev, ok := a.sessionCancels[sessionID]; ok {
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

// pruneCancelMarkers drops each entry of sessionCancelledAt and sessionNewest
// that no request in flight can match. Only a prompt that arrived before the
// cancel, or before the newest prompt, matches it. Above maxCancelMarkers
// entries in a map, it drops the oldest entries first. The caller holds a.mu.
func (a *AgentSideConnection) pruneCancelMarkers() {
	oldest, ok := a.conn.oldestInflightWatermark()
	for sessionID, cancelledAt := range a.sessionCancelledAt {
		if !ok || oldest >= cancelledAt {
			delete(a.sessionCancelledAt, sessionID)
		}
	}
	oldestArrival, ok := a.conn.oldestInflightArrival()
	for sessionID, newest := range a.sessionNewest {
		if !ok || oldestArrival >= newest {
			delete(a.sessionNewest, sessionID)
		}
	}
	dropOldestAbove(a.sessionCancelledAt, maxCancelMarkers)
	dropOldestAbove(a.sessionNewest, maxCancelMarkers)
}

// dropOldestAbove deletes the entries with the smallest values from m until it
// holds at most limit entries.
func dropOldestAbove(m map[string]uint64, limit int) {
	for len(m) > limit {
		var oldestID string
		var oldestValue uint64
		found := false
		for id, v := range m {
			if !found || v < oldestValue {
				oldestID, oldestValue, found = id, v, true
			}
		}
		delete(m, oldestID)
	}
}
