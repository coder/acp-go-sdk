// Package acp provides Go types and connection plumbing for the
// Agent Client Protocol (ACP). It contains generated dispatchers,
// outbound helpers, shared request/response types, and related
// utilities used by agents and clients to communicate over ACP.
//
// A client must initialize an agent before anything else. Until the Agent's
// Initialize returns without an error, an AgentSideConnection answers every
// other request from the client, extension requests included, with -32600
// (invalid request) and drops every notification, so neither reaches the
// Agent. A failed initialize leaves the connection uninitialized.
package acp
