// Package server implements the agent side of the ACP "Streamable HTTP"
// remote transport.
//
// See https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/streamable-http-websocket-transport.mdx
// for the wire-level specification.
//
//   - Each ACP connection is an in-process Agent instance plus a pair of
//     pipes. The client initiates the connection with `POST /acp` carrying
//     a JSON-RPC `initialize` request; the server replies 200 OK with a
//     JSON body and an Acp-Connection-Id header and then accepts further
//     messages on that connection-id until the client DELETEs it or drops
//     all its streams.
//   - All subsequent client → server messages are delivered as
//     `POST /acp` with Acp-Connection-Id and (for session-scoped methods)
//     Acp-Session-Id headers. POSTs return 202 Accepted; the corresponding
//     JSON-RPC response is delivered asynchronously on a long-lived SSE
//     stream.
//   - All server → client messages are delivered on long-lived GET SSE
//     streams: one connection-scoped stream (keyed by Acp-Connection-Id
//     alone) and one stream per sessionId (Acp-Connection-Id +
//     Acp-Session-Id). The router classifies each outbound JSON-RPC
//     message by sessionId and fans it out to the right stream.
//
// The server is transport-only: it takes a caller-provided factory that
// returns a fresh acp.Agent for each new connection. This keeps the
// package decoupled from any specific agent implementation.
package httpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	acp "github.com/coder/acp-go-sdk"
	"github.com/coder/acp-go-sdk/acphttp"
)

// Header names re-exported from the shared acphttp package so callers can
// reference them without importing two packages.
const (
	HeaderConnectionID = acphttp.HeaderConnectionID
	HeaderSessionID    = acphttp.HeaderSessionID
)

const (
	mimeJSON = acphttp.MimeJSON
	mimeSSE  = acphttp.MimeSSE
)

// AgentFactory produces a fresh Agent for each new ACP connection. Along
// with the agent, factories may return:
//   - bindConnection: an optional callback invoked with the SDK-level
//     AgentSideConnection immediately after it is created, so the agent
//     implementation can call methods back into the transport (e.g.
//     fs/read, request_permission). Pass nil if the agent does not need
//     a connection handle.
//   - close: an optional cleanup callback invoked when the connection is
//     torn down. Pass nil if there is nothing to release.
type AgentFactory func(ctx context.Context) (agent acp.Agent, bindConnection func(*acp.AgentSideConnection), close func(), err error)

// Config configures a Server.
type Config struct {
	// Factory is called once per new ACP connection to produce the agent
	// that will serve it. Required.
	Factory AgentFactory

	// Logger receives internal transport diagnostics. Defaults to
	// slog.Default().
	Logger *slog.Logger

	// Path is the endpoint path under which the ACP routes are served.
	// Defaults to "/acp". The same path handles POST, GET (SSE) and
	// DELETE.
	Path string

	// MaxConnections caps the number of concurrent ACP connections. Each
	// connection holds an agent instance, two pipes, and a pair of
	// goroutines, so an unbounded initialize POST loop (a misbehaving client
	// or an attacker) would otherwise grow goroutines, file descriptors, and
	// memory without limit. When the cap is reached, initialize POSTs are
	// rejected with 503 Service Unavailable before the agent factory runs.
	//
	// Zero (the default) means no limit; callers fronting the server with
	// their own connection limiting can leave it unset.
	MaxConnections int
}

// ErrTooManyConnections is returned by createConnection when MaxConnections
// is reached. handleInitialize maps it to 503 Service Unavailable.
var ErrTooManyConnections = fmt.Errorf("httpserver: too many connections")

// Server serves one or more remote ACP connections over Streamable HTTP.
// Register the result of Handler() on your http.Server; Close() tears down
// all in-flight connections.
type Server struct {
	cfg    Config
	logger *slog.Logger
	path   string

	mu          sync.RWMutex
	connections map[string]*connection
	// reserved counts connection slots claimed by in-flight initialize
	// requests that have not yet been added to the connections map. It is
	// folded into the MaxConnections check so concurrent initializes cannot
	// race past the cap in the window between the factory call and addConn.
	reserved int
	closed   bool
}

// New constructs a Server. The Factory config field is required; other
// fields default to sensible values (logger → slog.Default, path → /acp).
func New(cfg Config) (*Server, error) {
	if cfg.Factory == nil {
		return nil, fmt.Errorf("httpserver: Config.Factory is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	path := cfg.Path
	if path == "" {
		path = "/acp"
	}
	return &Server{
		cfg:         cfg,
		logger:      logger.With("component", "acphttp.server"),
		path:        path,
		connections: make(map[string]*connection),
	}, nil
}

// Handler returns an http.Handler that serves the ACP endpoint. Mount it
// at the root of an http.Server (routing is done internally so callers can
// mix with unrelated routes if they use an outer mux).
//
// Connections created through this handler are rooted in a background
// context, not any server-level context; ListenAndServe/Serve call Close()
// on shutdown, but callers wiring Handler() into their own *http.Server
// must call Server.Close() themselves to tear down in-flight connections
// and avoid leaking their goroutines.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(s.path, s.route)
	return mux
}

// Close tears down every active connection. Safe to call multiple times.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]*connection, 0, len(s.connections))
	for _, c := range s.connections {
		conns = append(conns, c)
	}
	s.connections = make(map[string]*connection)
	s.mu.Unlock()

	for _, c := range conns {
		c.shutdown()
	}
	return nil
}

// route dispatches to the method-specific handler. Only the three verbs
// defined by the RFD are accepted; everything else is 405.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handlePost(w, r)
	case http.MethodGet:
		s.handleGet(w, r)
	case http.MethodDelete:
		s.handleDelete(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// getConn looks up a connection, returning nil if it is unknown or the
// server is closed.
func (s *Server) getConn(id string) *connection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connections[id]
}

// removeConn atomically removes and returns the connection identified by
// id, or nil if it is unknown.
func (s *Server) removeConn(id string) *connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.connections[id]
	if c != nil {
		delete(s.connections, id)
	}
	return c
}

// reserveConn claims a connection slot, enforcing MaxConnections before the
// (potentially expensive) agent factory runs. The reservation is converted
// into a real entry by addConn or released by releaseConn on failure.
func (s *Server) reserveConn() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("httpserver: server closed")
	}
	if s.cfg.MaxConnections > 0 && len(s.connections)+s.reserved >= s.cfg.MaxConnections {
		return ErrTooManyConnections
	}
	s.reserved++
	return nil
}

// releaseConn returns a reservation taken by reserveConn without adding a
// connection (used when creation fails after the slot was claimed).
func (s *Server) releaseConn() {
	s.mu.Lock()
	if s.reserved > 0 {
		s.reserved--
	}
	s.mu.Unlock()
}

// addConn promotes a reservation into a registered connection.
func (s *Server) addConn(id string, c *connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserved > 0 {
		s.reserved--
	}
	if s.closed {
		return fmt.Errorf("httpserver: server closed")
	}
	s.connections[id] = c
	return nil
}

// discardBody drains and closes a request body; used when we want the
// underlying connection to be reusable by keep-alive.
func discardBody(r io.ReadCloser) {
	_, _ = io.Copy(io.Discard, r)
	r.Close()
}
