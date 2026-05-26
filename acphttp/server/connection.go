package httpserver

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"

	acp "github.com/coder/acp-go-sdk"
	"github.com/coder/acp-go-sdk/acphttp"
	"github.com/google/uuid"
)

// outboundSubscriber is a single consumer of one outbound stream (one SSE
// GET). Messages are delivered on ch; done is closed (via closeDone) when
// the stream should stop, either because the server side is tearing down,
// the HTTP client disconnected, or the subscriber fell too far behind.
type outboundSubscriber struct {
	ch       chan string
	done     chan struct{}
	doneOnce sync.Once
}

func newOutboundSubscriber() *outboundSubscriber {
	return &outboundSubscriber{
		ch:   make(chan string, 128),
		done: make(chan struct{}),
	}
}

// closeDone idempotently signals the subscriber to stop. Safe to call
// from both the HTTP handler (on client disconnect) and from closeAll (on
// server shutdown) — only the first call closes the channel.
func (s *outboundSubscriber) closeDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

// outboundStream fans one source of messages out to zero or more
// subscribers, buffering anything emitted before the first subscriber
// attaches so no messages are lost in the window between a session being
// created and the client opening its session-scoped GET.
type outboundStream struct {
	logger      *slog.Logger
	mu          sync.Mutex
	preBuffer   []string
	subscribers []*outboundSubscriber
	// cap on the pre-subscribe buffer to keep memory bounded if a client
	// never attaches. Excess messages are dropped from the front.
	preBufferCap int
	// warnedOverflow is set the first time the pre-buffer overflows so the
	// warning fires once per overflow window (it resets when a subscriber
	// attaches and drains the buffer) rather than once per dropped message.
	warnedOverflow bool
	closed         bool
}

func newOutboundStream(logger *slog.Logger) *outboundStream {
	return &outboundStream{logger: logger, preBufferCap: 1024}
}

// push delivers msg to every current subscriber. If no subscriber has
// attached yet the message is appended to the pre-subscribe buffer so the
// first subscriber replays it on arrival.
func (s *outboundStream) push(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if len(s.subscribers) == 0 {
		if len(s.preBuffer) >= s.preBufferCap {
			// Drop oldest. Warn once per overflow window so a client that
			// never opens its GET stream leaves a breadcrumb instead of
			// silently losing messages.
			if !s.warnedOverflow {
				s.logger.Warn("outbound pre-subscribe buffer overflow, dropping oldest messages", "cap", s.preBufferCap)
				s.warnedOverflow = true
			}
			s.preBuffer = s.preBuffer[1:]
		}
		s.preBuffer = append(s.preBuffer, msg)
		return
	}
	for _, sub := range s.subscribers {
		select {
		case sub.ch <- msg:
		default:
			// The subscriber's buffer is full: the SSE handler is blocked
			// (TCP backpressure, slow client, buffering proxy). Dropping
			// silently would discard JSON-RPC responses and hang the client
			// SDK forever with no diagnostic. Instead tear the subscriber
			// down: the SSE handler unblocks via sub.done, the client sees
			// the stream close, and its reconnect logic re-establishes a
			// fresh stream. The dropped message is still lost, but a silent
			// hang becomes a visible disconnect.
			s.logger.Warn("outbound subscriber overflow, closing stream to force client reconnect", "buffer", cap(sub.ch))
			sub.closeDone()
		}
	}
}

// subscribe attaches a new subscriber and returns the replay buffer so the
// subscriber can emit it in order before reading from its channel.
func (s *outboundStream) subscribe() (replay []string, sub *outboundSubscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub = newOutboundSubscriber()
	s.subscribers = append(s.subscribers, sub)
	replay = s.preBuffer
	s.preBuffer = nil
	s.warnedOverflow = false
	return replay, sub
}

// unsubscribe removes a subscriber. Idempotent.
func (s *outboundStream) unsubscribe(target *outboundSubscriber) {
	s.mu.Lock()
	for i, sub := range s.subscribers {
		if sub == target {
			s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	target.closeDone()
}

// closeAll marks the stream closed and wakes every subscriber.
func (s *outboundStream) closeAll() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	subs := s.subscribers
	s.subscribers = nil
	s.preBuffer = nil
	s.mu.Unlock()
	for _, sub := range subs {
		sub.closeDone()
	}
}

// responseRoute records where a response should be routed. Set when the
// client POSTs a request; consumed when the agent replies with a matching
// JSON-RPC id.
type responseRoute int

const (
	routeConnection responseRoute = iota
	routeSession
)

type pendingResponse struct {
	route     responseRoute
	sessionID string
}

// connection owns an in-process agent instance plus the pipes and streams
// that bridge it to the HTTP transport.
type connection struct {
	id     string
	logger *slog.Logger

	// Pipes: toAgent carries messages written by the transport and read
	// by the SDK; fromAgent carries messages written by the SDK and read
	// by the router.
	toAgentW   *io.PipeWriter
	toAgentR   *io.PipeReader
	fromAgentW *io.PipeWriter
	fromAgentR *io.PipeReader

	// SDK-side connection. Held so we can observe Done() for liveness.
	agentConn *acp.AgentSideConnection

	// agentCleanup (if non-nil) is called once when the connection is
	// torn down, so the factory can release any per-agent resources.
	agentCleanup func()

	// connStream: outbound messages that are not associated with a
	// specific session (session/new and session/load responses, etc.).
	connStream *outboundStream
	// sessionStreams: outbound messages associated with a sessionId.
	sessionStreams sync.Map // map[string]*outboundStream

	// pending records where server → client responses to client → server
	// POSTs must go, keyed by the canonical JSON-RPC id.
	pendingMu sync.Mutex
	pending   map[string]pendingResponse

	ctx    context.Context
	cancel context.CancelFunc

	// initResponseCh delivers exactly one message: the agent's response
	// to the initialize request. The POST handler waits on it before
	// starting the router goroutine.
	initResponseCh chan string

	routerStarted sync.Once
	routerWG      sync.WaitGroup

	shutdownOnce sync.Once
}

// createConnection starts a new agent and wires up the pipes. It returns
// the new connection, ready to receive the initialize message on
// c.toAgentW.
//
// The connection context is intentionally rooted in context.Background()
// rather than the initiating request's context: a connection outlives the
// single HTTP request that creates it and must survive until the client
// DELETEs it (or the server shuts down). Do not thread the request context
// in here.
func (s *Server) createConnection() (*connection, error) {
	ctx, cancel := context.WithCancel(context.Background())
	agent, bindConn, cleanup, err := s.cfg.Factory(ctx)
	if err != nil {
		cancel()
		return nil, err
	}

	toAgentR, toAgentW := io.Pipe()
	fromAgentR, fromAgentW := io.Pipe()

	id := uuid.NewString()
	c := &connection{
		id:             id,
		logger:         s.logger.With("conn", id),
		toAgentR:       toAgentR,
		toAgentW:       toAgentW,
		fromAgentR:     fromAgentR,
		fromAgentW:     fromAgentW,
		agentCleanup:   cleanup,
		pending:        make(map[string]pendingResponse),
		ctx:            ctx,
		cancel:         cancel,
		initResponseCh: make(chan string, 1),
	}
	c.connStream = newOutboundStream(c.logger.With("stream", "connection"))

	// Spin up the SDK's agent-side connection. Its goroutines will read
	// from toAgentR (what the client POSTed) and write JSON-RPC messages
	// to fromAgentW (which our router drains).
	c.agentConn = acp.NewAgentSideConnection(agent, fromAgentW, toAgentR)

	// Let the agent implementation see the connection so it can issue
	// server → client calls (fs/read, request_permission, etc.).
	if bindConn != nil {
		bindConn(c.agentConn)
	}

	// Observe the agent-side connection's lifecycle so if the agent
	// goroutines die (peer closed, unrecoverable error), we tear the
	// connection down. Remove it from the server's map first so a client
	// that never sends DELETE (crash, timeout, dropped network) does not
	// leave a zombie entry that grows the map without bound.
	go func() {
		select {
		case <-c.agentConn.Done():
		case <-c.ctx.Done():
		}
		s.removeConn(c.id)
		c.shutdown()
	}()

	if err := s.addConn(id, c); err != nil {
		c.shutdown()
		return nil, err
	}
	c.logger.Info("connection created")
	return c, nil
}

// writeToAgent sends one complete JSON-RPC message into the pipe feeding
// the SDK. The message must be a single JSON object (no trailing newline
// is required; we add one because the SDK reads line-delimited).
func (c *connection) writeToAgent(msg []byte) error {
	trimmed := bytes.TrimRight(msg, "\r\n")
	// Single write with newline appended so partial reads don't split
	// the message across scanner iterations.
	buf := make([]byte, 0, len(trimmed)+1)
	buf = append(buf, trimmed...)
	buf = append(buf, '\n')
	_, err := c.toAgentW.Write(buf)
	return err
}

// getOrCreateSessionStream returns the outbound stream for sessionID,
// lazily creating it the first time it is referenced.
func (c *connection) getOrCreateSessionStream(sessionID string) *outboundStream {
	if v, ok := c.sessionStreams.Load(sessionID); ok {
		return v.(*outboundStream)
	}
	fresh := newOutboundStream(c.logger.With("stream", "session", "session_id", sessionID))
	actual, _ := c.sessionStreams.LoadOrStore(sessionID, fresh)
	return actual.(*outboundStream)
}

// recordPendingRoute remembers the route for a response to a client-issued
// request, so the router can fan the response out to the right stream.
func (c *connection) recordPendingRoute(idKey string, route pendingResponse) {
	if idKey == "" {
		return
	}
	c.pendingMu.Lock()
	c.pending[idKey] = route
	c.pendingMu.Unlock()
}

// takePendingRoute returns and removes the stored route for idKey.
func (c *connection) takePendingRoute(idKey string) (pendingResponse, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	r, ok := c.pending[idKey]
	if ok {
		delete(c.pending, idKey)
	}
	return r, ok
}

// startRouter launches the goroutine that reads outbound agent messages
// line-by-line and fans them out. It is safe to call once per connection.
// The first JSON-RPC message (the initialize response) is intercepted and
// delivered to initResponseCh instead of being routed, so the POST handler
// can surface it synchronously on the initialize response body.
func (c *connection) startRouter() {
	c.routerStarted.Do(func() {
		c.routerWG.Add(1)
		go c.runRouter()
	})
}

func (c *connection) runRouter() {
	defer c.routerWG.Done()

	scanner := bufio.NewScanner(c.fromAgentR)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	initialized := false
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		// Copy once: the scanner reuses its buffer between iterations.
		msg := make([]byte, len(raw))
		copy(msg, raw)

		if !initialized {
			// First message is always the response to initialize.
			// Deliver it to the POST handler which is blocking on
			// initResponseCh and DO NOT route it.
			initialized = true
			select {
			case c.initResponseCh <- string(msg):
			case <-c.ctx.Done():
				return
			}
			continue
		}

		c.route(string(msg))
	}
	if err := scanner.Err(); err != nil {
		c.logger.Debug("router: read error", "err", err)
	}
}

// route classifies one outbound JSON-RPC message and pushes it into the
// appropriate outbound stream. It delegates the classification policy to
// the shared acphttp.ClassifyOutbound helper.
func (c *connection) route(msg string) {
	raw := []byte(msg)
	target := acphttp.ClassifyOutbound(raw, func(idKey string) (acphttp.OutboundTarget, bool) {
		r, ok := c.takePendingRoute(idKey)
		if !ok || r.route != routeSession {
			return acphttp.OutboundTarget{}, false
		}
		return acphttp.SessionTarget(r.sessionID), true
	})
	if target.IsSession() {
		c.getOrCreateSessionStream(target.SessionID).push(msg)
		return
	}
	c.connStream.push(msg)
}

// shutdown tears the connection down: closes pipes, streams, and invokes
// the factory cleanup callback. Safe to call multiple times.
func (c *connection) shutdown() {
	c.shutdownOnce.Do(func() {
		c.logger.Info("connection shutdown")
		c.cancel()

		// Close the pipes so any SDK goroutines reading/writing on
		// them return with EOF / ErrClosedPipe.
		_ = c.toAgentW.Close()
		_ = c.toAgentR.Close()
		_ = c.fromAgentW.Close()
		_ = c.fromAgentR.Close()

		// Wait for the router goroutine to observe the closed read pipe and
		// exit before we report the connection as fully torn down, so callers
		// (e.g. Server.Close) don't race with the router touching connection
		// state. cancel() above already unblocks any send on initResponseCh,
		// so this returns promptly.
		c.routerWG.Wait()

		c.connStream.closeAll()
		c.sessionStreams.Range(func(_, v any) bool {
			v.(*outboundStream).closeAll()
			return true
		})

		if c.agentCleanup != nil {
			c.agentCleanup()
		}
	})
}
