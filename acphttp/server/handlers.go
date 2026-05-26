package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/coder/acp-go-sdk/acphttp"
)

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
	if !strings.Contains(r.Header.Get("Content-Type"), mimeJSON) {
		http.Error(w, "unsupported media type: expected application/json", http.StatusUnsupportedMediaType)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 32*1024*1024))
	discardBody(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body: "+err.Error(), http.StatusBadRequest)
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
	w.WriteHeader(http.StatusAccepted)
}

// handleInitialize creates a fresh connection, forwards the initialize
// message, and synchronously returns the agent's response with the
// Acp-Connection-Id header.
func (s *Server) handleInitialize(w http.ResponseWriter, r *http.Request, body []byte) {
	conn, err := s.createConnection()
	if err != nil {
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

	for {
		select {
		case <-r.Context().Done():
			return
		case <-conn.ctx.Done():
			return
		case <-sub.done:
			return
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
