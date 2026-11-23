package acp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
)

// AgentSideConnection represents the agent's view of a connection to a client.
type AgentSideConnection struct {
	conn  *Connection
	agent Agent
	// ready closes once conn is set. NewConnection starts reading from the
	// peer before it returns, and the handlers read conn, so each waits for
	// ready first, as the proxy's handleWhenReady does.
	ready chan struct{}

	// initialized becomes true once the Agent's Initialize returns without an
	// error, and never goes back. A failed initialize leaves it false.
	initialized atomic.Bool

	mu             sync.Mutex
	sessionCancels map[sessionKey]*promptCancel
	// sessionCancelledAt holds, per session, the arrival number of the last
	// session/cancel. A prompt that arrived before that cancel and registers
	// after it is cancelled at once. pruneCancelMarkers drops an entry once no
	// request from the peer that arrived before its cancel is in flight.
	sessionCancelledAt map[sessionKey]uint64
	// sessionNewest holds, per session, the arrival number of the newest
	// prompt that has registered. A prompt that arrived before it and
	// registers later is cancelled at once, even after the newer prompt
	// returned. pruneCancelMarkers drops an entry once no request from the
	// peer that arrived before it is in flight.
	sessionNewest map[sessionKey]uint64
}

// NewAgentSideConnection creates a new agent-side connection bound to the
// provided Agent implementation. opts set the limits on what it holds from the
// peer (see ConnectionOption). The Agent gets no request other than initialize,
// and no notification, until its Initialize returns without an error: the
// connection answers such a request with -32600 and drops such a notification.
func NewAgentSideConnection(agent Agent, peerInput io.Writer, peerOutput io.Reader, opts ...ConnectionOption) *AgentSideConnection {
	asc := &AgentSideConnection{}
	asc.agent = agent
	asc.sessionCancels = make(map[sessionKey]*promptCancel)
	asc.sessionCancelledAt = make(map[sessionKey]uint64)
	asc.sessionNewest = make(map[sessionKey]uint64)
	asc.ready = make(chan struct{})
	asc.conn = NewConnection(asc.handleWithExtensions, peerInput, peerOutput, opts...)
	close(asc.ready)
	return asc
}

// handleBeforeInitialize handles a message from the client while the
// connection is not initialized. It drops a notification, answers a request
// other than initialize with -32600, and marks the connection initialized when
// the Agent's Initialize returns a response that encodes. A response that does
// not encode reaches the client as -32603, so the connection stays
// uninitialized then too.
func (a *AgentSideConnection) handleBeforeInitialize(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
	if _, ok := a.conn.notificationSeq(ctx); ok {
		a.conn.log(slog.LevelDebug, "dropped notification that arrived before initialize", "method", method)
		return nil, nil
	}
	if method != AgentMethodInitialize {
		return nil, NewInvalidRequest(map[string]any{"error": "initialize must complete before " + method})
	}
	resp, err := a.handle(ctx, method, params)
	if err != nil {
		return nil, err
	}
	b, mErr := json.Marshal(resp)
	if mErr != nil {
		return nil, NewInternalError(map[string]any{"error": mErr.Error()})
	}
	a.initialized.Store(true)
	return json.RawMessage(b), nil
}

// Done exposes a channel that closes when the peer disconnects. It does not
// end a write blocked on a peer that stopped reading. An agent on os.Stdout
// ends that write by exiting, because closing a blocking stdout pipe leaves
// the write blocked (see NewConnection).
func (c *AgentSideConnection) Done() <-chan struct{} { return c.conn.Done() }

// SetLogger directs connection diagnostics to the provided logger.
func (c *AgentSideConnection) SetLogger(l *slog.Logger) { c.conn.SetLogger(l) }

// sessionKey keys a session by the SHA-256 of its sessionId, because the peer
// picks sessionIds of any length and the cancel markers outlive its messages.
type sessionKey [sha256.Size]byte

func sessionKeyOf(id string) sessionKey { return sha256.Sum256([]byte(id)) }

// promptCancel is the entry of a running prompt in sessionCancels.
type promptCancel struct {
	cancel context.CancelFunc
	// arrival is the messageArrival of the prompt, when known. A
	// session/cancel with a smaller arrival arrived before the prompt.
	arrival    uint64
	hasArrival bool
}

// cancelPrompt handles a session/cancel for session id that runs with ctx. It
// cancels the prompt that has registered for the session, unless that prompt
// arrived after the cancel, and records where the cancel arrived, for a prompt
// that arrived before it but has not registered yet.
func (a *AgentSideConnection) cancelPrompt(ctx context.Context, id string) {
	sessionID := sessionKeyOf(id)
	a.mu.Lock()
	defer a.mu.Unlock()
	arrival, hasArrival := a.conn.messageArrival(ctx)
	if pc, ok := a.sessionCancels[sessionID]; ok && !(hasArrival && pc.hasArrival && pc.arrival > arrival) {
		pc.cancel()
		delete(a.sessionCancels, sessionID)
	}
	if hasArrival && arrival > a.sessionCancelledAt[sessionID] {
		a.sessionCancelledAt[sessionID] = arrival
	}
	a.pruneCancelMarkers()
}

// maxCancelMarkers bounds sessionCancelledAt and sessionNewest while a long
// request is in flight. Only the peer that sends the cancels and prompts can
// push an entry out, and a lost entry only loses that peer's own cancel or
// prompt order.
const maxCancelMarkers = 1024

// registerPrompt makes cancel the cancel func of the prompt for session id
// whose handler runs with ctx, and cancels the prompt of the session that it
// replaces. When a prompt of the session that arrived later has already
// registered, even one that has returned, registerPrompt calls cancel at once
// and leaves the session's entry alone. If a session/cancel that arrived
// after the prompt has already run, registerPrompt also calls cancel at once.
// The prompt handler calls the returned func when the prompt returns. It
// removes the entry only while the entry is still this prompt's, so a newer
// prompt of the session stays cancellable.
func (a *AgentSideConnection) registerPrompt(ctx context.Context, id string, cancel context.CancelFunc) (done func()) {
	sessionID := sessionKeyOf(id)
	a.mu.Lock()
	defer a.mu.Unlock()
	arrival, hasArrival := a.conn.messageArrival(ctx)
	if newest, ok := a.sessionNewest[sessionID]; ok && hasArrival && newest > arrival {
		cancel()
		return func() {}
	}
	if hasArrival {
		a.sessionNewest[sessionID] = arrival
	}
	pc := &promptCancel{cancel: cancel, arrival: arrival, hasArrival: hasArrival}
	if prev, ok := a.sessionCancels[sessionID]; ok {
		prev.cancel()
	}
	a.sessionCancels[sessionID] = pc
	if cancelledAt, ok := a.sessionCancelledAt[sessionID]; ok {
		if hasArrival && cancelledAt > arrival {
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
	oldest, ok := a.conn.oldestInflightArrival()
	for sessionID, cancelledAt := range a.sessionCancelledAt {
		if !ok || oldest >= cancelledAt {
			delete(a.sessionCancelledAt, sessionID)
		}
	}
	for sessionID, newest := range a.sessionNewest {
		if !ok || oldest >= newest {
			delete(a.sessionNewest, sessionID)
		}
	}
	dropOldestAbove(a.sessionCancelledAt, maxCancelMarkers)
	dropOldestAbove(a.sessionNewest, maxCancelMarkers)
}

// dropOldestAbove deletes the entries with the smallest values from m until it
// holds at most limit entries.
func dropOldestAbove(m map[sessionKey]uint64, limit int) {
	for len(m) > limit {
		var oldestID sessionKey
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
