package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
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
// know are lost. A nil handler forwards the bytes as they are. The _meta of a
// proxy/successor envelope belongs to its hop and is dropped.
//
// Cancellation is per hop. A $/cancel_request for a request that the proxy
// handles cancels the handler's ctx, and a request that the handler sent
// with that ctx then sends a $/cancel_request of its own. session/cancel is
// a notification like any other: the proxy forwards it.
//
// Messages keep the order of Connection. Notifications are handled one at a
// time in arrival order, and the answer to a request that the proxy forwards
// goes out after the notifications that arrived before the successor's
// answer, such as the session/update notifications of a prompt. Each request
// runs on its own goroutine, though, so a notification that arrives right
// after a request can reach the other side before that request, such as a
// session/cancel sent at once after its session/prompt.
type UnstableProxySideConnection struct {
	conn      *Connection
	handlers  UnstableProxyHandlers
	client    *UnstableProxyClient
	successor *UnstableProxySuccessor
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
}

// UnstableProxyClient sends messages to the client side of a proxy, as they
// are. It has a method per client method.
//
// UNSTABLE: proxy chains are not part of the spec yet.
type UnstableProxyClient struct{ conn *Connection }

// UnstableProxySuccessor sends messages to a proxy's successor, each wrapped
// in proxy/successor. It has a method per agent method, initialize included.
//
// UNSTABLE: proxy chains are not part of the spec yet.
type UnstableProxySuccessor struct{ conn *Connection }

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
	c := &UnstableProxySideConnection{ready: make(chan struct{})}
	c.conn = NewConnection(c.handleWhenReady, peerInput, peerOutput, opts...)
	c.client = &UnstableProxyClient{conn: c.conn}
	c.successor = &UnstableProxySuccessor{conn: c.conn}
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

func (c *UnstableProxySideConnection) handleWhenReady(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
	<-c.ready
	return c.handle(ctx, method, params)
}

// isNotification reports whether the message whose handler runs with ctx is
// a notification.
func (c *UnstableProxySideConnection) isNotification(ctx context.Context) bool {
	_, ok := c.conn.notificationSeq(ctx)
	return ok
}

// forwardToSuccessor sends method and params to the successor, as a request
// or a notification as notification says, and returns the successor's result
// or error unchanged.
func (c *UnstableProxySideConnection) forwardToSuccessor(ctx context.Context, method string, params json.RawMessage, notification bool) (any, *RequestError) {
	if notification {
		return nil, toReqErr(c.successor.notify(ctx, method, params))
	}
	result, err := successorRequest[json.RawMessage](c.successor, ctx, method, params)
	if err != nil {
		return nil, toReqErr(err)
	}
	return result, nil
}

// forwardToClient sends method and params to the client side, as a request
// or a notification as notification says, and returns the client's result or
// error unchanged.
func (c *UnstableProxySideConnection) forwardToClient(ctx context.Context, method string, params json.RawMessage, notification bool) (any, *RequestError) {
	// A nil json.RawMessage in an any encodes as "params":null.
	var p any
	if !noParams(params) {
		p = params
	}
	if notification {
		return nil, toReqErr(c.conn.SendNotification(ctx, method, p))
	}
	result, err := SendRequest[json.RawMessage](c.conn, ctx, method, p)
	if err != nil {
		return nil, toReqErr(err)
	}
	return result, nil
}

// successorRequest sends method and params to the successor in a
// proxy/successor request and returns the result.
func successorRequest[T any](s *UnstableProxySuccessor, ctx context.Context, method string, params any) (T, error) {
	wrapped, err := successorParams(params)
	if err != nil {
		var zero T
		return zero, err
	}
	return SendRequest[T](s.conn, ctx, ProxyMethodProxySuccessor, UnstableProxySuccessorRequest{Method: method, Params: wrapped})
}

// notify sends method and params to the successor in a proxy/successor
// notification.
func (s *UnstableProxySuccessor) notify(ctx context.Context, method string, params any) error {
	wrapped, err := successorParams(params)
	if err != nil {
		return err
	}
	return s.conn.SendNotification(ctx, ProxyMethodProxySuccessor, UnstableProxySuccessorNotification{Method: method, Params: wrapped})
}

// successorParams encodes params as the params of a proxy/successor envelope:
// nil when there are none, so that the envelope leaves them out.
func successorParams(params any) (json.RawMessage, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return nil, NewInvalidParams(map[string]any{"error": err.Error()})
	}
	if noParams(b) {
		return nil, nil
	}
	return b, nil
}

// noParams reports whether params, as a message carries them, means no
// params: absent or null.
func noParams(params json.RawMessage) bool {
	return len(params) == 0 || bytes.Equal(params, []byte("null"))
}
