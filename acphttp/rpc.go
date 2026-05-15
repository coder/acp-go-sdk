package acphttp

import (
	"bytes"
	"encoding/json"
)

// JSON-RPC introspection helpers shared by the server and client
// implementations.
//
// They operate on raw JSON-RPC message bytes without fully unmarshalling
// the payload — the transport only needs to peek at a small subset of
// fields (the method name, sessionId, JSON-RPC id) to route messages, and
// avoiding full decoding keeps the hot path allocation-light.
//
// All helpers are tolerant of malformed input: an unparseable message
// returns the zero value rather than an error. Callers that need to
// validate JSON should use json.Valid before treating the result as
// authoritative.

// PeekMethod returns the JSON-RPC "method" field of raw, or "" if the
// message is a response or unparseable.
func PeekMethod(raw []byte) string {
	var probe struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Method
}

// HasMethod reports whether raw carries a non-empty "method" field — i.e.
// is a JSON-RPC request or notification rather than a response.
func HasMethod(raw []byte) bool {
	return PeekMethod(raw) != ""
}

// PeekParamsSessionID returns params.sessionId from raw, or "" if absent.
// Used to identify session-scoped requests and notifications on either
// side of the wire.
func PeekParamsSessionID(raw []byte) string {
	var probe struct {
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Params.SessionID
}

// PeekResultSessionID returns result.sessionId from raw. Used by clients
// to detect the response to a session/new request so they can pre-open
// the session-scoped GET stream.
func PeekResultSessionID(raw []byte) string {
	var probe struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Result.SessionID
}

// PeekID returns the raw bytes of the JSON-RPC "id" field (trimmed of
// surrounding whitespace), or an empty slice if no id is present.
//
// JSON-RPC ids may be strings, numbers, or null. The transport never
// needs to interpret the id as a typed value; it only uses the canonical
// byte form as a routing key.
func PeekID(raw []byte) json.RawMessage {
	var probe struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(raw, &probe)
	return bytes.TrimSpace(probe.ID)
}

// CanonicalID returns a stable string representation of the JSON-RPC id
// in raw, suitable for use as a map key when correlating responses with
// requests. Returns "" if the id is absent or JSON-null.
func CanonicalID(raw []byte) string {
	id := PeekID(raw)
	if len(id) == 0 || bytes.Equal(id, []byte("null")) {
		return ""
	}
	return string(id)
}

// CanonicalIDFromRaw is the json.RawMessage equivalent of CanonicalID,
// used when the caller has already extracted the id field via a partial
// unmarshal. Returns "" for absent or JSON-null ids.
func CanonicalIDFromRaw(id json.RawMessage) string {
	trimmed := bytes.TrimSpace(id)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	return string(trimmed)
}

// IsInitialize reports whether raw is an `initialize` request: a JSON-RPC
// request (has a non-null id) with `method == "initialize"`. This is the
// only POST that returns 200 with a JSON body rather than 202; transports
// use this helper to special-case it.
func IsInitialize(raw []byte) bool {
	if PeekMethod(raw) != "initialize" {
		return false
	}
	return CanonicalID(raw) != ""
}

// IsSessionScoped reports whether the JSON-RPC method must carry an
// Acp-Session-Id header on a POST and is logically associated with a
// single session. Derived from the ACP schema by listing every
// agent-side request type whose params include a required "sessionId"
// field.
//
// Note: session/load is included even though its *response* is delivered
// on the connection-scoped GET stream (the client cannot have opened the
// session-scoped stream yet at that point). The POST itself still
// carries Acp-Session-Id; servers should special-case the response
// routing.
//
// session/set_model belongs to the unstable schema today but is listed
// here so transports speaking the unstable protocol get correct header
// behaviour out of the box; servers that only implement stable methods
// will simply never see it.
func IsSessionScoped(method string) bool {
	switch method {
	case "session/cancel",
		"session/close",
		"session/load",
		"session/prompt",
		"session/resume",
		"session/set_config_option",
		"session/set_mode",
		"session/set_model":
		return true
	}
	return false
}

// OutboundTarget identifies which outbound SSE stream a server-to-client
// JSON-RPC message should be routed to.
//
// The transport always has exactly two flavours of outbound stream open
// per connection: a connection-scoped stream (one per ACP connection)
// and zero-or-more session-scoped streams (one per active session). See
// ClassifyOutbound for the routing rules.
type OutboundTarget struct {
	// SessionID is non-empty iff the message belongs on the
	// session-scoped stream for that session. An empty string means the
	// message belongs on the connection-scoped stream.
	SessionID string
}

// IsSession reports whether the target is a session-scoped stream.
func (t OutboundTarget) IsSession() bool { return t.SessionID != "" }

// ConnectionTarget is the zero value of OutboundTarget: the
// connection-scoped stream.
func ConnectionTarget() OutboundTarget { return OutboundTarget{} }

// SessionTarget constructs an OutboundTarget for the given session id.
func SessionTarget(sessionID string) OutboundTarget {
	return OutboundTarget{SessionID: sessionID}
}

// ResponseRouteLookup is a callback used by ClassifyOutbound to ask the
// caller's pending-response table where a particular JSON-RPC response
// should be routed. The lookup should remove the entry from the table on
// hit so the table stays bounded.
//
// idKey is the result of CanonicalID applied to the outbound message; it
// will be empty (and the callback will not be invoked) for messages
// without a JSON-RPC id.
type ResponseRouteLookup func(idKey string) (OutboundTarget, bool)

// ClassifyOutbound decides which outbound stream a server-to-client
// JSON-RPC message belongs on.
//
// Routing rules:
//
//  1. If the message has a "method" field and a non-empty
//     params.sessionId, route to the session-scoped stream for that
//     session.
//  2. If the message has a "method" field but no params.sessionId, route
//     to the connection-scoped stream. (This covers server-initiated
//     connection-level notifications.)
//  3. If the message is a response (no "method" field), look up its id
//     in the caller-supplied response-route table; if a session-scoped
//     route is stored, use it. Otherwise fall back to the
//     connection-scoped stream.
//
// Responses to session/new and session/load always land on the
// connection-scoped stream — the client has not yet opened a
// session-scoped GET when it issues those requests — and the server's
// response-route bookkeeping should reflect that by not recording a
// session entry for them.
func ClassifyOutbound(raw []byte, lookup ResponseRouteLookup) OutboundTarget {
	if HasMethod(raw) {
		if sid := PeekParamsSessionID(raw); sid != "" {
			return SessionTarget(sid)
		}
		return ConnectionTarget()
	}
	if lookup == nil {
		return ConnectionTarget()
	}
	idKey := CanonicalID(raw)
	if idKey == "" {
		return ConnectionTarget()
	}
	if target, ok := lookup(idKey); ok {
		return target
	}
	return ConnectionTarget()
}
