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
func (c *ClientSideConnection) Done() <-chan struct{} { return c.conn.Done() }

// SetLogger directs connection diagnostics to the provided logger.
func (c *ClientSideConnection) SetLogger(l *slog.Logger) { c.conn.SetLogger(l) }

// Connection returns the underlying JSON-RPC connection.
//
// It is for requests the typed methods cannot make: a method the agent
// implements that this SDK's schema does not carry, or one whose response has
// fields the generated type drops. CallExtension covers only "_"-prefixed
// methods; for anything else, use SendRequest or SendNotification on the
// returned connection. It is the same connection the typed methods use, not a
// copy.
func (c *ClientSideConnection) Connection() *Connection { return c.conn }
