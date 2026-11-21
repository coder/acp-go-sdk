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
// provided Client implementation. opts set the limits on what it holds from
// the peer (see ConnectionOption).
func NewClientSideConnection(client Client, peerInput io.Writer, peerOutput io.Reader, opts ...ConnectionOption) *ClientSideConnection {
	csc := &ClientSideConnection{}
	csc.client = client
	csc.conn = NewConnection(csc.handleWithExtensions, peerInput, peerOutput, opts...)
	return csc
}

// Done exposes a channel that closes when the peer disconnects. It does not
// end a write blocked on a peer that stopped reading. Closing the agent's
// stdin, or ending the agent process, does (see NewConnection).
func (c *ClientSideConnection) Done() <-chan struct{} { return c.conn.Done() }

// SetLogger directs connection diagnostics to the provided logger.
func (c *ClientSideConnection) SetLogger(l *slog.Logger) { c.conn.SetLogger(l) }
