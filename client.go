package acp

import (
	"io"
	"log/slog"
)

// ClientSideConnection provides the client's view of the connection and implements Agent calls.
type ClientSideConnection struct {
	conn   *Connection
	client Client
}

// NewClientSideConnection creates a new client-side connection bound to the
// provided Client implementation.
func NewClientSideConnection(client Client, peerInput io.Writer, peerOutput io.Reader) *ClientSideConnection {
	csc := &ClientSideConnection{}
	csc.client = client
	csc.conn = NewConnection(csc.handleWithExtensions, peerInput, peerOutput)
	return csc
}

// Done exposes a channel that closes when the peer disconnects.
// Queued notification handlers may still be running.
func (c *ClientSideConnection) Done() <-chan struct{} { return c.conn.Done() }

// NotificationsDone exposes Connection.NotificationsDone. It closes after
// disconnect once all queued notification handlers have returned.
func (c *ClientSideConnection) NotificationsDone() <-chan struct{} {
	return c.conn.NotificationsDone()
}

// SetLogger directs connection diagnostics to the provided logger.
func (c *ClientSideConnection) SetLogger(l *slog.Logger) { c.conn.SetLogger(l) }
