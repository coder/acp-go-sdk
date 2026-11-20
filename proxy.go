package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"sync"
)

// UnstableProxySideConnection is a proxy's connection to its conductor in an
// ACP proxy chain.
//
// UNSTABLE: proxy chains are a draft RFD
// (https://agentclientprotocol.com/rfds/proxy-chains), not part of the spec
// yet. This API may change or go away in any release.
//
// A conductor stands between a client and a chain of components, each proxy
// and then the agent. It looks like an agent to the client and routes every
// message, so a proxy talks only to its conductor, over this one connection:
//
//   - Messages from the client side arrive as they are, and the proxy sends
//     to the client side as it is, with Client.
//   - Messages to and from the proxy's successor, the next component, travel
//     wrapped in a proxy/successor request or notification. The proxy sends
//     to its successor with Successor.
//   - The conductor initializes a proxy with proxy/initialize. A plain
//     initialize gets a -32600 (invalid request) error.
//
// Every handler in UnstableProxyHandlers is optional. A nil handler forwards
// its message unchanged, so the zero UnstableProxyHandlers make a proxy that
// passes every message through. A message of a method that this SDK does not
// know, an extension method included, is forwarded too. The result or error
// of a forwarded request comes back unchanged.
//
// A handler gets its message decoded into this SDK's Go types, and the
// method it sends with encodes it again, so members that those types do not
// know are lost. A nil handler forwards the bytes as they are. A message that
// a handler would get but that does not decode into its type or fails its
// validation, such as a session/update of a kind from a newer schema, goes on
// unchanged as if the handler were nil, and the proxy logs it at Debug level.
// UnstableProxyHandlers.RejectUndecodable refuses such messages instead. The
// _meta of a proxy/successor envelope belongs to its hop and is dropped.
//
// Cancellation is per hop, as the proxy chains RFD says. A $/cancel_request
// for a request that the proxy handles cancels the handler's ctx, whether it
// arrives plain or wrapped in proxy/successor, and a request that the handler
// sent with that ctx then sends a plain $/cancel_request with its own id. The
// proxy never forwards a $/cancel_request. session/cancel is a notification
// like any other: the proxy forwards it.
//
// The proxy keeps the order of each side. What it sends to one side for the
// messages from the other goes out in the order those messages arrived:
// requests, notifications, the answers to the requests that it forwarded,
// and whatever a handler sends to the other side with its ctx in place of
// its message. So a session/cancel that the client sends right after its
// session/prompt reaches the successor after the prompt. To keep that order,
// a handler holds back the later messages from its side from the time its
// message arrives until the handler sends a request and waits for the
// answer, or returns. A handler that answers a request itself, or does long
// work before it sends, holds them back for that long, and because
// notification handlers run one at a time, a notification held back so also
// holds back the notifications after it from both sides. Two cases give up
// the order, so that no message waits for an answer that needs it: a
// handler lets the later messages go while it waits for an answer from
// either side, and a request that starts while a notification handler waits
// for an answer (see MethodHandler) goes ahead of the notifications queued
// behind that handler.
//
// The proxy knows which handler sends a message only by the ctx it is sent
// with, so a handler must send with its ctx or one derived from it, such as
// context.WithoutCancel(ctx). A message sent with another ctx, such as
// context.Background(), keeps no order, and the handler keeps holding back
// the later messages from its side until it returns. When the answer to such
// a request needs one of those messages, the proxy deadlocks: a Prompt
// handler that forwards the prompt with context.Background() never gets the
// answer if the agent asks the client for permission first, because the
// client's answer waits for that handler.
type UnstableProxySideConnection struct {
	conn      *Connection
	handlers  UnstableProxyHandlers
	client    *UnstableProxyClient
	successor *UnstableProxySuccessor
	order     *forwardOrder
	// ready closes once the fields above are set. The connection reads
	// messages before NewUnstableProxySideConnection returns, so its handler
	// waits for ready.
	ready chan struct{}
}

// UnstableProxyHandlers holds a proxy's handlers, one set per direction. A
// nil handler forwards its message unchanged.
//
// UNSTABLE: proxy chains are not part of the spec yet.
type UnstableProxyHandlers struct {
	// FromClient handles the messages from the client side.
	FromClient UnstableProxyFromClient
	// FromSuccessor handles the messages from the successor.
	FromSuccessor UnstableProxyFromSuccessor
	// RejectUndecodable refuses a message that a handler would get but that
	// does not decode into its type or fails its validation: a request gets a
	// -32602 (invalid params) error and a notification is dropped, with an
	// Error log. By default the proxy forwards such a message unchanged.
	RejectUndecodable bool
}

// UnstableProxyClient sends messages to the client side of a proxy, as they
// are. It has a method per client method.
//
// UNSTABLE: proxy chains are not part of the spec yet.
type UnstableProxyClient struct{ p *UnstableProxySideConnection }

// UnstableProxySuccessor sends messages to a proxy's successor, each wrapped
// in proxy/successor. It has a method per agent method, initialize included.
//
// UNSTABLE: proxy chains are not part of the spec yet.
type UnstableProxySuccessor struct{ p *UnstableProxySideConnection }

// NewUnstableProxySideConnection returns a proxy's connection to its
// conductor, which writes messages to peerInput and reads them from
// peerOutput. It calls handlers once, with the connection, before it handles
// a message, so that the handlers can keep the connection and send with its
// Client and Successor. handlers must return without sending. A nil handlers
// forwards every message. opts set the limits on what it holds from the peer
// (see ConnectionOption).
//
// UNSTABLE: proxy chains are not part of the spec yet.
func NewUnstableProxySideConnection(handlers func(c *UnstableProxySideConnection) UnstableProxyHandlers, peerInput io.Writer, peerOutput io.Reader, opts ...ConnectionOption) *UnstableProxySideConnection {
	c := &UnstableProxySideConnection{order: newForwardOrder(), ready: make(chan struct{})}
	c.client = &UnstableProxyClient{p: c}
	c.successor = &UnstableProxySuccessor{p: c}
	hooks := withHooks(connectionHooks{requestArrived: c.order.requestArrived, wrappedCancel: wrappedCancel})
	c.conn = NewConnection(c.handleWhenReady, peerInput, peerOutput, append(opts[:len(opts):len(opts)], hooks)...)
	if handlers != nil {
		c.handlers = handlers(c)
	}
	close(c.ready)
	return c
}

// Done exposes a channel that closes when the conductor disconnects.
func (c *UnstableProxySideConnection) Done() <-chan struct{} { return c.conn.Done() }

// SetLogger directs connection diagnostics to the provided logger.
func (c *UnstableProxySideConnection) SetLogger(l *slog.Logger) { c.conn.SetLogger(l) }

// Client returns the handle that sends to the client side, as it is.
func (c *UnstableProxySideConnection) Client() *UnstableProxyClient { return c.client }

// Successor returns the handle that sends to the successor, wrapped in
// proxy/successor.
func (c *UnstableProxySideConnection) Successor() *UnstableProxySuccessor { return c.successor }

// proxySide is a side of a proxy. A message from one side goes on to the
// other.
type proxySide uint8

const (
	clientSide proxySide = iota
	successorSide
)

func (s proxySide) other() proxySide { return 1 - s }

// sideOf returns the side that a message of method from the conductor comes
// from.
func sideOf(method string) proxySide {
	if method == ProxyMethodProxySuccessor {
		return successorSide
	}
	return clientSide
}

// proxyHandler is the ctx value of a proxy handler that runs: the arrival
// number of its message (see Connection.messageArrival), and its side.
type proxyHandler struct {
	p       *UnstableProxySideConnection
	from    proxySide
	arrival uint64
}

type proxyHandlerKey struct{}

// handlerOf returns the handler of c whose ctx ctx is, or derives from.
func (c *UnstableProxySideConnection) handlerOf(ctx context.Context) (proxyHandler, bool) {
	h, ok := ctx.Value(proxyHandlerKey{}).(proxyHandler)
	return h, ok && h.p == c
}

func (c *UnstableProxySideConnection) handleWhenReady(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
	<-c.ready
	arrival, _ := c.conn.messageArrival(ctx)
	h := proxyHandler{p: c, from: sideOf(method), arrival: arrival}
	ctx = context.WithValue(ctx, proxyHandlerKey{}, h)
	if c.isNotification(ctx) {
		c.order.start(arrival)
		defer c.order.finish(arrival)
		return c.handle(ctx, method, params)
	}
	result, err := c.handle(ctx, method, params)
	// The answer goes back to h.from. When it forwards an answer from the
	// other side, it waits for its turn there, until the connection closes:
	// the connection writes it even when a $/cancel_request has ended ctx.
	// The hooks end h's holds once the connection has written it.
	_, _ = c.order.awaitTurn(c.conn.ctx, h, h.from)
	c.order.stop(arrival)
	return result, err
}

// isNotification reports whether the message whose handler runs with ctx is
// a notification.
func (c *UnstableProxySideConnection) isNotification(ctx context.Context) bool {
	_, ok := c.conn.notificationSeq(ctx)
	return ok
}

// forward sends method and params on to side to, as a request or a
// notification as notification says, and returns the answer unchanged.
func (c *UnstableProxySideConnection) forward(ctx context.Context, to proxySide, method string, params json.RawMessage, notification bool) (any, *RequestError) {
	if method == ProtocolMethodCancelRequest {
		// $/cancel_request is per hop, so no proxy forwards one.
		return nil, NewInvalidRequest(map[string]string{"error": "$/cancel_request is never forwarded"})
	}
	// A nil json.RawMessage in an any encodes as "params":null.
	var p any
	if !noParams(params) {
		p = params
	}
	if notification {
		return nil, toReqErr(c.notify(ctx, to, method, p))
	}
	result, err := c.request(ctx, to, method, p)
	if err != nil {
		return nil, toReqErr(err)
	}
	return result, nil
}

// undecodable handles a message from the conductor for a handler, with err
// from decoding it. Unless RejectUndecodable, it forwards the message to side
// to as if the handler were nil.
func (c *UnstableProxySideConnection) undecodable(ctx context.Context, to proxySide, method string, params json.RawMessage, notification bool, err error) (any, *RequestError) {
	if c.handlers.RejectUndecodable {
		return nil, NewInvalidParams(map[string]any{"error": err.Error()})
	}
	c.conn.loggerOrDefault().Debug("forwarding a message that its handler cannot decode", "method", method, "err", err)
	return c.forward(ctx, to, method, params, notification)
}

// decodeParams decodes params into p and validates it.
func decodeParams(params json.RawMessage, p interface{ Validate() error }) error {
	if err := json.Unmarshal(params, p); err != nil {
		return err
	}
	return p.Validate()
}

// notify sends method and params to side to as a notification, in its turn
// when ctx is a handler's (see forwardOrder.awaitTurn).
func (c *UnstableProxySideConnection) notify(ctx context.Context, to proxySide, method string, params any) error {
	method, params, err := onWire(to, method, params, true)
	if err != nil {
		return err
	}
	if h, ok := c.handlerOf(ctx); ok {
		if _, err := c.order.awaitTurn(ctx, h, to); err != nil {
			return err
		}
	}
	return c.conn.SendNotification(ctx, method, params)
}

// request sends method and params to side to as a request, in its turn when
// ctx is a handler's, and returns the result. The handler then waits for the
// answer, so the later messages that it holds back go on (see forwardOrder).
func (c *UnstableProxySideConnection) request(ctx context.Context, to proxySide, method string, params any) (json.RawMessage, error) {
	method, params, err := onWire(to, method, params, false)
	if err != nil {
		return nil, err
	}
	h, ok := c.handlerOf(ctx)
	if !ok {
		sent, err := c.conn.sendRequest(method, params, nil)
		if err != nil {
			return nil, err
		}
		return sent.wait(ctx)
	}
	held, err := c.order.awaitTurn(ctx, h, to)
	if err != nil {
		return nil, err
	}
	sent, err := c.conn.sendRequest(method, params, func(arrival uint64) { c.order.holdAnswer(to, h.arrival, arrival) })
	c.order.release(h.arrival, held)
	if err != nil {
		return nil, err
	}
	return sent.wait(ctx)
}

// proxyRequest sends a request to side to and decodes its result.
func proxyRequest[T any](c *UnstableProxySideConnection, ctx context.Context, to proxySide, method string, params any) (T, error) {
	var result T
	raw, err := c.request(ctx, to, method, params)
	if err != nil {
		return result, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &result); err != nil {
			return result, NewInternalError(map[string]any{"error": err.Error()})
		}
	}
	return result, nil
}

// onWire returns the method and params of a message to side to as the
// conductor gets it: as it is to the client side, wrapped in proxy/successor
// to the successor.
func onWire(to proxySide, method string, params any, notification bool) (string, any, error) {
	if to == clientSide {
		return method, params, nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return "", nil, NewInvalidParams(map[string]any{"error": err.Error()})
	}
	// No params leave the envelope's params out.
	var wrapped json.RawMessage
	if !noParams(b) {
		wrapped = b
	}
	if notification {
		return ProxyMethodProxySuccessor, UnstableProxySuccessorNotification{Method: method, Params: wrapped}, nil
	}
	return ProxyMethodProxySuccessor, UnstableProxySuccessorRequest{Method: method, Params: wrapped}, nil
}

// noParams reports whether params, as a message carries them, means no
// params: absent or null.
func noParams(params json.RawMessage) bool {
	return len(params) == 0 || bytes.Equal(params, []byte("null"))
}

// wrappedCancel is the connection hook that finds a $/cancel_request in a
// proxy/successor notification. The RFD has the receiver treat it as a plain
// one: its requestId names a proxy/successor request on this connection.
func wrappedCancel(method string, params json.RawMessage) (json.RawMessage, bool) {
	// JSON spells each character of the name as itself or as a \u escape, so
	// params with neither the name nor an escape wrap no cancel.
	if method != ProxyMethodProxySuccessor || !bytes.Contains(params, []byte("cancel_request")) && !bytes.Contains(params, []byte(`\u`)) {
		return nil, false
	}
	var envelope struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(params, &envelope) != nil || envelope.Method != ProtocolMethodCancelRequest {
		return nil, false
	}
	return envelope.Params, true
}

// forwardOrder keeps the order of each side of a proxy (see
// UnstableProxySideConnection). A handler holds the messages from a side
// that it has yet to send on: its own request, and the answers to its
// requests. It holds each from its arrival until the handler sends a
// request, and so waits for an answer, or until it returns and the
// connection has written its reply. A handler's send to a side waits until
// no other handler holds a message from the other side that arrived before
// its own.
//
// Notifications take no hold: the connection runs a notification's handler
// after the handlers of the notifications before it and before the handlers
// of the requests after it start, except while it waits for an answer. A
// hold there would make a request that the peer needs answered first wait
// for a notification that waits for the peer.
//
// A hold is a message, and only releaseLocked takes holds away, so only it
// can let a waiting send go, and it closes changed whenever it does. A new
// hold cannot let a send go: the connection calls requestArrived and
// holdAnswer in the order the messages arrive, so a new hold is the newest
// message, and a send waits only for messages older than its own.
type forwardOrder struct {
	mu sync.Mutex
	// held maps each side to the messages from it that a handler holds, each
	// by its arrival number to the arrival number of the handler's message.
	held [2]map[uint64]uint64
	// running holds the handlers that can take a hold on an answer: request
	// handlers from their arrival until they return, notification handlers
	// while they run.
	running map[uint64]struct{}
	// changed closes, and gets a new channel, when a hold ends.
	changed chan struct{}
}

func newForwardOrder() *forwardOrder {
	return &forwardOrder{
		held:    [2]map[uint64]uint64{{}, {}},
		running: map[uint64]struct{}{},
		changed: make(chan struct{}),
	}
}

// requestArrived is the connection hook for each request from the conductor.
// The request holds back its side until its handler waits for an answer or
// the request is done.
func (o *forwardOrder) requestArrived(arrival uint64, method string) (done func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.running[arrival] = struct{}{}
	o.held[sideOf(method)][arrival] = arrival
	return func() { o.finish(arrival) }
}

// start records that the handler of the notification arrival runs.
func (o *forwardOrder) start(arrival uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.running[arrival] = struct{}{}
}

// stop records that the handler of arrival has returned. Its holds stay
// until finish.
func (o *forwardOrder) stop(arrival uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.running, arrival)
}

// finish ends the handler of arrival and its holds.
func (o *forwardOrder) finish(arrival uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.running, arrival)
	o.releaseLocked(arrival, math.MaxUint64)
}

// holdAnswer makes the handler owner hold the answer to its request, which
// arrived from side from as message arrival, while the handler runs.
func (o *forwardOrder) holdAnswer(from proxySide, owner, arrival uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.running[owner]; ok {
		o.held[from][arrival] = owner
	}
}

// release ends the holds of the handler owner on messages that arrived up to
// upTo.
func (o *forwardOrder) release(owner, upTo uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.releaseLocked(owner, upTo)
}

func (o *forwardOrder) releaseLocked(owner, upTo uint64) {
	ended := false
	for _, held := range o.held {
		for arrival, by := range held {
			if by == owner && arrival <= upTo {
				delete(held, arrival)
				ended = true
			}
		}
	}
	if ended {
		close(o.changed)
		o.changed = make(chan struct{})
	}
}

// awaitTurn waits until the handler h may send to side to: until no other
// handler holds a message from the side from, the other side, that arrived
// before h's own. h's own is the newest message from that side that h holds,
// or else h's message when it came from that side. A handler with neither
// sends at once. awaitTurn returns the arrival number of the newest message
// that h holds, for release. It returns early with ctx's cause when ctx ends.
func (o *forwardOrder) awaitTurn(ctx context.Context, h proxyHandler, to proxySide) (held uint64, err error) {
	from := to.other()
	for {
		o.mu.Lock()
		own, ordered := o.newestLocked(from, h.arrival)
		if !ordered && h.from == from {
			own, ordered = h.arrival, true
		}
		if !ordered || !o.holdsBeforeLocked(from, h.arrival, own) {
			for side := range o.held {
				newest, _ := o.newestLocked(proxySide(side), h.arrival)
				held = max(held, newest)
			}
			o.mu.Unlock()
			return held, nil
		}
		changed := o.changed
		o.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return 0, toReqErr(context.Cause(ctx))
		}
	}
}

// newestLocked returns the arrival number of the newest message from side
// from that the handler owner holds, if it holds one.
func (o *forwardOrder) newestLocked(from proxySide, owner uint64) (newest uint64, ok bool) {
	for arrival, by := range o.held[from] {
		if by == owner && arrival >= newest {
			newest, ok = arrival, true
		}
	}
	return newest, ok
}

// holdsBeforeLocked reports whether a handler other than owner holds a
// message from side from that arrived before arrival.
func (o *forwardOrder) holdsBeforeLocked(from proxySide, owner, arrival uint64) bool {
	for a, by := range o.held[from] {
		if by != owner && a < arrival {
			return true
		}
	}
	return false
}
