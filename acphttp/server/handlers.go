package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk/acphttp"
)

// maxPostBodyBytes caps a single POST /acp body. Oversized bodies are
// rejected with 413 (the Rust reference server does the same at 16 MiB).
const maxPostBodyBytes = 32 * 1024 * 1024

// sseKeepAliveInterval is how often an SSE comment is written to otherwise
// idle GET streams. 15s matches both the Rust and TypeScript reference
// servers. A variable so tests can shorten it.
var sseKeepAliveInterval = 15 * time.Second

// handlePost handles POST /acp.
//
// Two flavors:
//   - initialize (no Acp-Connection-Id header): creates a fresh connection,
//     forwards the message to the new agent, waits for the agent's
//     response, returns it synchronously as 200 OK with an
//     Acp-Connection-Id header.
//   - everything else: requires Acp-Connection-Id; validates the JSON-RPC
//     envelope, records the pending response route (connection- or
//     session-scoped), forwards the message to the agent, returns 202
//     Accepted.
func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != mimeJSON {
		http.Error(w, "unsupported media type: expected application/json", http.StatusUnsupportedMediaType)
		return
	}

	// Bound the request body. A declared Content-Length over the limit is
	// rejected up front; otherwise read one byte past the limit so an
	// oversized chunked body is detected as such (413) rather than being
	// truncated into invalid JSON (400).
	if r.ContentLength > maxPostBodyBytes {
		http.Error(w, "POST body too large", http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPostBodyBytes+1))
	discardBody(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > maxPostBodyBytes {
		http.Error(w, "POST body too large", http.StatusRequestEntityTooLarge)
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}
	if body[0] == '[' {
		http.Error(w, "batch requests are not supported", http.StatusNotImplemented)
		return
	}

	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// initialize is the only POST returning 200 with a body; detect it from
	// the already-parsed envelope rather than re-unmarshalling via
	// acphttp.IsInitialize.
	if envelope.Method == "initialize" && acphttp.CanonicalIDFromRaw(envelope.ID) != "" {
		// initialize always creates a fresh connection; carrying an
		// Acp-Connection-Id here means the client is confused about its
		// own state (matching the TypeScript reference server's check).
		if r.Header.Get(HeaderConnectionID) != "" {
			http.Error(w, "initialize not allowed on existing connection", http.StatusBadRequest)
			return
		}
		s.handleInitialize(w, r, body)
		return
	}

	connID := r.Header.Get(HeaderConnectionID)
	if connID == "" {
		http.Error(w, "missing "+HeaderConnectionID, http.StatusBadRequest)
		return
	}
	conn := s.getConn(connID)
	if conn == nil {
		http.Error(w, "unknown "+HeaderConnectionID, http.StatusNotFound)
		return
	}

	sessionHeader := r.Header.Get(HeaderSessionID)
	if acphttp.IsSessionScoped(envelope.Method) && sessionHeader == "" {
		http.Error(w, "missing "+HeaderSessionID+" for session-scoped method", http.StatusBadRequest)
		return
	}
	// An Acp-Session-Id header that contradicts params.sessionId is a
	// client bug; both reference servers reject it (the Rust server when
	// reconciling the header into params, the TypeScript server explicitly).
	if envelope.Method != "" && sessionHeader != "" &&
		envelope.Params.SessionID != "" && envelope.Params.SessionID != sessionHeader {
		http.Error(w, "mismatched "+HeaderSessionID, http.StatusBadRequest)
		return
	}

	// Validate responses to session-scoped server → client requests
	// (request_permission, fs/read, ...). Per the RFD these response POSTs
	// are session-scoped and must echo the Acp-Session-Id of the stream
	// the request was delivered on; a contradicting header is rejected. A
	// *missing* header is tolerated — the Rust reference client never sends
	// one on responses, and the header is not needed for routing here — so
	// we stay interoperable where the TypeScript reference server would 400.
	var respondedIDKey string
	if envelope.Method == "" {
		if idKey := acphttp.CanonicalIDFromRaw(envelope.ID); idKey != "" {
			if sid, ok := conn.peekClientResponseSession(idKey); ok {
				if sessionHeader != "" && sessionHeader != sid {
					http.Error(w, "mismatched "+HeaderSessionID, http.StatusBadRequest)
					return
				}
				respondedIDKey = idKey
			}
		}
	}

	// Record where this request's response (if any) should be routed.
	if len(envelope.ID) > 0 && envelope.Method != "" {
		route := pendingResponse{route: routeConnection}
		// session/load and session/fork are session-scoped POSTs (they carry
		// Acp-Session-Id) but per the RFD their responses are delivered on the
		// connection-scoped stream alongside session/new: the client hasn't
		// opened the (new, for fork) session-scoped GET when it issues them,
		// so the connection stream is the only place the response is
		// guaranteed to land. The client then opens the session stream once it
		// sees the sessionId in the result.
		//
		// We consult IsSessionScoped rather than the raw header so a
		// non-session-scoped POST (e.g. an adversarial session/new carrying a
		// spurious Acp-Session-Id) cannot divert its response onto a session
		// stream the client isn't listening on.
		deliverOnConnStream := envelope.Method == "session/load" || envelope.Method == "session/fork"
		if acphttp.IsSessionScoped(envelope.Method) && sessionHeader != "" && !deliverOnConnStream {
			route = pendingResponse{route: routeSession, sessionID: sessionHeader}
			// Ensure the session stream exists so the response has
			// somewhere to land even if the client hasn't yet
			// opened the session-scoped GET stream.
			conn.getOrCreateSessionStream(sessionHeader)
		}
		conn.recordPendingRoute(acphttp.CanonicalIDFromRaw(envelope.ID), route)
	}

	if err := conn.writeToAgent(body); err != nil {
		http.Error(w, fmt.Sprintf("failed to forward %s to agent %s: %v", envelope.Method, connID, err), http.StatusInternalServerError)
		return
	}
	if respondedIDKey != "" {
		conn.dropClientResponseSession(respondedIDKey)
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleInitialize creates a fresh connection, forwards the initialize
// message, and synchronously returns the agent's response with the
// Acp-Connection-Id header.
func (s *Server) handleInitialize(w http.ResponseWriter, r *http.Request, body []byte) {
	conn, err := s.createConnection()
	if err != nil {
		if errors.Is(err, ErrTooManyConnections) {
			http.Error(w, "too many connections", http.StatusServiceUnavailable)
			return
		}
		// The factory error may embed internal detail (connection strings,
		// paths, stack traces). Log it server-side; return a generic message.
		s.logger.Error("failed to create connection", "err", err)
		http.Error(w, "failed to create connection", http.StatusInternalServerError)
		return
	}

	if err := conn.writeToAgent(body); err != nil {
		s.removeConn(conn.id)
		conn.shutdown()
		http.Error(w, "failed to forward initialize: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Start the router BEFORE waiting for the initialize response: the
	// router is what drains fromAgentR and surfaces the first message on
	// initResponseCh.
	conn.startRouter()

	var initResponse string
	select {
	case initResponse = <-conn.initResponseCh:
	case <-conn.ctx.Done():
		s.removeConn(conn.id)
		conn.shutdown()
		http.Error(w, "connection closed before initialize response", http.StatusInternalServerError)
		return
	case <-r.Context().Done():
		// Client gave up on initialize; tear it all down.
		s.removeConn(conn.id)
		conn.shutdown()
		return
	}

	w.Header().Set("Content-Type", mimeJSON)

	// A JSON-RPC error response to initialize means the agent rejected the
	// connection. Match the Rust reference server: tear the connection down
	// and return the error body WITHOUT an Acp-Connection-Id header, so the
	// client does not hold a handle to a connection that no longer exists.
	if acphttp.IsErrorResponse([]byte(initResponse)) {
		s.removeConn(conn.id)
		conn.shutdown()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(initResponse))
		conn.logger.Info("initialize rejected")
		return
	}

	w.Header().Set(HeaderConnectionID, conn.id)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(initResponse))
	conn.logger.Info("initialize complete")
}

// handleGet opens a long-lived SSE stream. With only Acp-Connection-Id the
// stream is connection-scoped; adding Acp-Session-Id narrows it to a
// single session.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.Header.Get("Accept"), mimeSSE) {
		http.Error(w, "not acceptable: expected text/event-stream", http.StatusNotAcceptable)
		return
	}
	connID := r.Header.Get(HeaderConnectionID)
	if connID == "" {
		http.Error(w, "missing "+HeaderConnectionID, http.StatusBadRequest)
		return
	}
	conn := s.getConn(connID)
	if conn == nil {
		http.Error(w, "unknown "+HeaderConnectionID, http.StatusNotFound)
		return
	}
	sessionID := r.Header.Get(HeaderSessionID)

	// http.NewResponseController unwraps middleware-wrapped ResponseWriters
	// (logging, metrics, auth) to find the underlying Flusher, where a bare
	// w.(http.Flusher) assertion would fail. Flush also surfaces errors.
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", mimeSSE)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set(HeaderConnectionID, connID)
	if sessionID != "" {
		w.Header().Set(HeaderSessionID, sessionID)
	}
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		// Streaming is unsupported by this writer chain (e.g. an HTTP/1.0
		// client, or a wrapper that does not implement Flush). The status is
		// already committed, so we can only log and abandon the stream.
		conn.logger.Warn("get: flush unsupported, abandoning stream", "err", err)
		return
	}

	var stream *outboundStream
	if sessionID == "" {
		stream = conn.connStream
	} else {
		stream = conn.getOrCreateSessionStream(sessionID)
	}

	replay, sub := stream.subscribe()
	defer stream.unsubscribe(sub)

	for _, msg := range replay {
		if !writeSSEEvent(w, rc, msg) {
			return
		}
	}

	// Emit an SSE comment on idle streams so proxies and load balancers do
	// not reap the connection, and so a dead client is detected by the
	// failed write. Both reference servers do this at the same interval.
	keepAlive := time.NewTicker(sseKeepAliveInterval)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-conn.ctx.Done():
			return
		case <-sub.done:
			return
		case <-keepAlive.C:
			if !writeSSEKeepAlive(w, rc) {
				return
			}
		case msg, ok := <-sub.ch:
			if !ok {
				return
			}
			if !writeSSEEvent(w, rc, msg) {
				return
			}
		}
	}
}

// handleDelete tears the connection down. Returns 202 on success, 404 if
// the connection id is unknown.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	connID := r.Header.Get(HeaderConnectionID)
	if connID == "" {
		http.Error(w, "missing "+HeaderConnectionID, http.StatusBadRequest)
		return
	}
	conn := s.removeConn(connID)
	if conn == nil {
		http.Error(w, "unknown "+HeaderConnectionID, http.StatusNotFound)
		return
	}
	conn.shutdown()
	w.WriteHeader(http.StatusAccepted)
}

// writeSSEEvent writes one `data:` event followed by a blank line and
// flushes. Returns false if the client connection is gone (write or flush
// error).
func writeSSEEvent(w http.ResponseWriter, rc *http.ResponseController, msg string) bool {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", msg); err != nil {
		return false
	}
	return rc.Flush() == nil
}

// writeSSEKeepAlive writes an SSE comment line (which clients must ignore
// per the SSE spec) and flushes. Returns false if the client is gone.
func writeSSEKeepAlive(w http.ResponseWriter, rc *http.ResponseController) bool {
	if _, err := io.WriteString(w, ":\n\n"); err != nil {
		return false
	}
	return rc.Flush() == nil
}
