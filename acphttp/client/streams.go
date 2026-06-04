package httpclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk/acphttp"
)

// openConnectionStream starts the long-lived connection-scoped GET stream
// if it isn't already open. The stream carries responses to session/new,
// session/load, and any connection-level server→client messages.
func (t *Transport) openConnectionStream() {
	t.sessionsMu.Lock()
	if t.connGetOpen || t.closedLocked() {
		t.sessionsMu.Unlock()
		return
	}
	t.connGetOpen = true
	// Add under the lock, ordered against Close (which closes closedCh and
	// then calls streams.Wait under the same lock), so the counter can never
	// go 0→1 after Wait has already returned.
	t.streams.Add(1)
	t.sessionsMu.Unlock()

	go func() {
		defer t.streams.Done()
		defer func() {
			t.sessionsMu.Lock()
			t.connGetOpen = false
			t.sessionsMu.Unlock()
		}()
		t.runStream(t.ctx, "" /* sessionID */, "connection")
	}()
}

// ensureSessionStream starts a session-scoped GET stream for sessionID if
// one isn't already active. Session streams carry all session-scoped
// notifications and responses to session-scoped POSTs.
func (t *Transport) ensureSessionStream(sessionID string) {
	t.sessionsMu.Lock()
	if _, ok := t.sessionGets[sessionID]; ok || t.closedLocked() {
		t.sessionsMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(t.ctx)
	t.sessionGets[sessionID] = cancel
	// Add under the lock; see openConnectionStream for the ordering rationale.
	t.streams.Add(1)
	t.sessionsMu.Unlock()

	go func() {
		defer t.streams.Done()
		defer func() {
			t.sessionsMu.Lock()
			delete(t.sessionGets, sessionID)
			t.sessionsMu.Unlock()
		}()
		t.runStream(ctx, sessionID, "session")
	}()
}

// runStream maintains a single long-lived SSE GET stream with automatic
// reconnect-on-transient-error behavior. sessionID may be empty to indicate
// the connection-scoped stream.
func (t *Transport) runStream(ctx context.Context, sessionID, label string) {
	// Backoff for reconnect attempts when the network drops but the
	// transport is still alive (e.g. server restart).
	backoff := 250 * time.Millisecond
	const maxBackoff = 5 * time.Second

	// failingSince marks when the current run of consecutive *failed*
	// reconnect attempts began. It is zeroed whenever a stream delivers an
	// event (onConnected) or the server closes a stream cleanly — both prove
	// the server is reachable. If the give-up budget is exhausted without
	// progress the transport is torn down so Read() returns io.EOF instead of
	// the SDK hanging on a response that will never come.
	var failingSince time.Time

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		connID := t.getConnID()
		if connID == "" {
			// The caller must have violated ordering (called
			// ensureSessionStream before initialize completed).
			// Bail rather than spinning.
			t.logger.Warn("stream aborted: no connection id", "stream", label, "session_id", sessionID)
			return
		}

		// onConnected is invoked by runSingleStream once the stream has
		// successfully opened and delivered an event, so a long-lived stream
		// that later drops reconnects quickly instead of inheriting the
		// 5s ceiling accumulated across earlier failures.
		err := t.runSingleStream(ctx, connID, sessionID, label, func() {
			backoff = 250 * time.Millisecond
			failingSince = time.Time{}
		})
		if errors.Is(err, context.Canceled) || t.isClosed() {
			return
		}

		if err == nil {
			// Clean EOF: the server was reachable (it answered 200) and closed
			// the stream — an idle-proxy timeout, or a deliberate
			// slow-subscriber force-close (see acphttp/server/connection.go)
			// that expects us to reconnect. Reopen the stream, otherwise the
			// SDK goes permanently deaf to all later server→client messages on
			// it. The server is healthy, so this attempt does not count
			// against the give-up budget.
			failingSince = time.Time{}
			t.logger.Debug("stream closed by server, reconnecting", "stream", label, "session_id", sessionID, "backoff", backoff)
		} else {
			// Transport-level failure (dial error, non-200, mid-stream read
			// error). If these persist past the give-up budget the connection
			// is effectively dead; surface that to the SDK as EOF.
			now := time.Now()
			if failingSince.IsZero() {
				failingSince = now
			} else if t.reconnectGiveUp > 0 && now.Sub(failingSince) >= t.reconnectGiveUp {
				t.logger.Error("stream giving up after repeated reconnect failures; tearing down transport",
					"stream", label, "session_id", sessionID, "elapsed", now.Sub(failingSince), "err", err)
				t.cancel()
				return
			}
			t.logger.Warn("stream disconnected, reconnecting", "stream", label, "session_id", sessionID, "err", err, "backoff", backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-t.closedCh:
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// runSingleStream opens one GET SSE connection and pumps events into the
// inbound channel. It returns nil on clean EOF (server closed stream) and
// an error on transport failure.
func (t *Transport) runSingleStream(ctx context.Context, connID, sessionID, label string, onConnected func()) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url, nil)
	if err != nil {
		return fmt.Errorf("httpclient: build GET: %w", err)
	}
	req.Header.Set("Accept", mimeSSE)
	req.Header.Set(headerConnectionID, connID)
	if sessionID != "" {
		req.Header.Set(headerSessionID, sessionID)
	}
	t.applyExtraHeaders(req)

	resp, err := t.streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("httpclient: GET /acp: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("httpclient: GET /acp returned %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	t.logger.Debug("stream open", "stream", label, "session_id", sessionID)
	// Stream is open; reset the caller's reconnect backoff on the first
	// delivered event.
	delivered := false

	// Parse the SSE stream. The reference implementation emits each
	// JSON-RPC message as the `data` field of a single event with no
	// `id` or `event` type. We accept multi-line `data:` values per the
	// SSE spec (multiple data: lines are joined with '\n') and deliver
	// the payload as one inbound message.
	return parseSSE(resp.Body, func(eventType, data string) {
		if eventType != "" && eventType != "message" {
			// ignore non-default events
			return
		}
		payload := strings.TrimSpace(data)
		if payload == "" {
			return
		}
		if !delivered {
			delivered = true
			if onConnected != nil {
				onConnected()
			}
		}
		t.logger.Debug("SSE event",
			"stream", label,
			"session_id", sessionID,
			"bytes", len(payload),
			"method", acphttp.PeekMethod([]byte(payload)),
			"id", peekID([]byte(payload)),
		)
		// Intercept session/new responses on the connection-scoped
		// stream so we can open the session-scoped stream as soon as
		// a sessionId appears. This is belt-and-suspenders: dispatch()
		// also opens the stream on the first session-scoped POST.
		if sessionID == "" {
			if sid := acphttp.PeekResultSessionID([]byte(payload)); sid != "" {
				t.ensureSessionStream(sid)
			}
		}
		t.pushInbound([]byte(payload))
	})
}

// parseSSE reads a text/event-stream body one event at a time and invokes
// onEvent for each complete event. It returns nil on EOF and an error on
// I/O failure.
//
// Per the SSE spec (https://html.spec.whatwg.org/multipage/server-sent-events.html):
//   - events are separated by blank lines
//   - lines starting with ":" are comments (we ignore them)
//   - "event:" sets the event type
//   - "data:" appends to the event's data buffer (multiple data: lines are
//     joined with '\n')
//   - other fields (id:, retry:) are ignored here.
func parseSSE(body io.Reader, onEvent func(eventType, data string)) error {
	scanner := bufio.NewScanner(body)
	const (
		initialBufSize = 1024 * 1024
		maxBufSize     = 16 * 1024 * 1024
	)
	scanner.Buffer(make([]byte, 0, initialBufSize), maxBufSize)

	var eventType string
	var dataBuf strings.Builder

	dispatch := func() {
		if dataBuf.Len() == 0 && eventType == "" {
			return
		}
		onEvent(eventType, dataBuf.String())
		eventType = ""
		dataBuf.Reset()
	}

	for scanner.Scan() {
		line := scanner.Text()
		// Normalize a stray \r that some servers send before the \n.
		line = strings.TrimRight(line, "\r")

		if line == "" {
			dispatch()
			continue
		}
		if strings.HasPrefix(line, ":") {
			// comment / keep-alive
			continue
		}
		name, value, ok := splitSSEField(line)
		if !ok {
			continue
		}
		switch name {
		case "event":
			eventType = value
		case "data":
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(value)
		}
	}
	// Dispatch trailing event if the body ended without a final blank line.
	dispatch()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("httpclient: SSE scan: %w", err)
	}
	return nil
}

// splitSSEField parses an SSE field line of the form "name:value". Per the
// spec, a single leading space after the colon is stripped. Lines without a
// colon are treated as a field with an empty value.
func splitSSEField(line string) (name, value string, ok bool) {
	name, value, found := strings.Cut(line, ":")
	if !found {
		return line, "", true
	}
	return name, strings.TrimPrefix(value, " "), true
}
