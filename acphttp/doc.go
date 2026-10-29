// Package acphttp defines constants and types shared by the ACP HTTP
// transport server and client implementations.
//
// The transport itself is specified in the ACP "Streamable HTTP & WebSocket
// Transport" RFD:
//
//	https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/streamable-http-websocket-transport.mdx
//
// The /acp endpoint serves the entire transport: POST/GET/DELETE for the
// Streamable HTTP profile and a future GET-with-Upgrade-websocket profile.
// Both profiles share the headers and lifecycle defined here.
//
// Implementations live in sub-packages:
//
//   - github.com/coder/acp-go-sdk/acphttp/server — the agent-side Server.
//   - github.com/coder/acp-go-sdk/acphttp/client — the client-side Transport.
package acphttp

// HTTP header names defined by the ACP HTTP transport.
const (
	// HeaderConnectionID identifies the transport-level connection. Set
	// by the server in the initialize response (both as a header and in
	// the response body) and echoed by the client on every subsequent
	// request.
	HeaderConnectionID = "Acp-Connection-Id"

	// HeaderSessionID identifies a session within a connection. Required
	// on session-scoped POSTs and on the session-scoped GET stream.
	HeaderSessionID = "Acp-Session-Id"
)

// MIME types used on the wire.
const (
	MimeJSON = "application/json"
	MimeSSE  = "text/event-stream"
)
