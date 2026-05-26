package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coder/acp-go-sdk/acphttp"
)

// Read copies bytes from the demultiplexed inbound queue into p. Each
// inbound message is delivered with a trailing newline so the SDK's
// bufio.Scanner sees one JSON-RPC message per line.
func (t *Transport) Read(p []byte) (int, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()

	if t.readBuf.Len() > 0 {
		return t.readBuf.Read(p)
	}

	select {
	case msg, ok := <-t.inbound:
		if !ok {
			return 0, io.EOF
		}
		t.readBuf.Write(msg)
		t.readBuf.WriteByte('\n')
		return t.readBuf.Read(p)
	case <-t.closedCh:
		return 0, io.EOF
	case <-t.ctx.Done():
		return 0, io.EOF
	}
}

// Write consumes one or more newline-delimited JSON-RPC messages from p and
// dispatches each one to the appropriate HTTP endpoint. The SDK sends one
// complete message per Write call with a trailing '\n'; we split
// defensively in case that changes.
func (t *Transport) Write(p []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if t.isClosed() {
		return 0, io.ErrClosedPipe
	}

	// Walk through p one line at a time. We do NOT split eagerly on the
	// final newline because JSON payloads may legitimately contain
	// embedded newlines *only* inside strings, and the SDK never emits
	// those in its wire format — it marshals a single-line JSON followed
	// by one '\n'. So splitting on '\n' is safe and matches what the
	// stdio path does.
	lines := bytes.Split(bytes.TrimRight(p, "\n"), []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if err := t.dispatch(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Close terminates the transport. If initialize has completed, it sends a
// best-effort DELETE to the server so connection and session state on the
// server side is cleaned up promptly.
func (t *Transport) Close() error {
	t.closeOnce.Do(func() {
		// Best-effort DELETE /acp. Use a short timeout and a fresh
		// context so Close remains responsive even if the main
		// context is already cancelled.
		if cid := t.getConnID(); cid != "" {
			dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer dcancel()
			req, err := http.NewRequestWithContext(dctx, http.MethodDelete, t.url, nil)
			if err == nil {
				req.Header.Set(headerConnectionID, cid)
				t.applyExtraHeaders(req)
				resp, err := t.httpClient.Do(req)
				if err != nil {
					t.logger.Debug("DELETE /acp failed", "err", err)
				} else {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
		}

		close(t.closedCh)
		t.cancel()
	})
	t.streams.Wait()
	return nil
}

// dispatch routes a single JSON-RPC message to the correct HTTP method.
// The bulk of the transport's complexity lives here: initialize is a
// synchronous POST → 200, session-scoped POSTs need Acp-Session-Id, and
// session/prompt requires its session-scoped GET stream to be open BEFORE
// the POST lands so no notifications are missed.
func (t *Transport) dispatch(msg []byte) error {
	method := acphttp.PeekMethod(msg)

	switch method {
	case "initialize":
		return t.doInitialize(msg)
	}

	connID := t.getConnID()
	if connID == "" {
		// The SDK guarantees `initialize` is sent first, so this is
		// either a bug or an attempt to send something before init
		// completed. Drop on the floor with an error to the caller.
		return fmt.Errorf("httpclient: cannot send %q before initialize completes", method)
	}

	sessionID := acphttp.PeekParamsSessionID(msg)

	// Eagerly open the session-scoped GET stream before sending any
	// session-scoped POST. This prevents a race where a fast server emits
	// session/update notifications before we've opened the stream. The
	// pre-subscribe buffer on the server side (goose uses 1024 entries)
	// absorbs events emitted before the stream is opened, but opening
	// early minimizes the window.
	if sessionID != "" {
		t.ensureSessionStream(sessionID)
	}

	ctx, cancel := context.WithTimeout(t.ctx, t.httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(msg))
	if err != nil {
		return fmt.Errorf("httpclient: build POST: %w", err)
	}
	req.Header.Set("Content-Type", mimeJSON)
	req.Header.Set(headerConnectionID, connID)
	if sessionID != "" {
		req.Header.Set(headerSessionID, sessionID)
	}
	t.applyExtraHeaders(req)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("httpclient: POST /acp (%s): %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("httpclient: POST /acp (%s) returned %d: %s", method, resp.StatusCode, bytes.TrimSpace(body))
	}
	// Drain the (empty) body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	t.logger.Debug("POST → /acp", "method", method, "id", peekID(msg), "session_id", sessionID)
	return nil
}

// doInitialize sends the initialize POST synchronously and reads the JSON
// response from the body (not from an SSE stream). It captures
// Acp-Connection-Id, forwards the response body to the SDK via the inbound
// channel, and starts the connection-scoped GET stream.
func (t *Transport) doInitialize(msg []byte) error {
	ctx, cancel := context.WithTimeout(t.ctx, t.httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(msg))
	if err != nil {
		return fmt.Errorf("httpclient: build initialize POST: %w", err)
	}
	req.Header.Set("Content-Type", mimeJSON)
	req.Header.Set("Accept", mimeJSON)
	t.applyExtraHeaders(req)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("httpclient: initialize POST: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("httpclient: initialize returned %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	connID := resp.Header.Get(headerConnectionID)
	if connID == "" {
		return fmt.Errorf("httpclient: initialize response missing %s header", headerConnectionID)
	}
	t.setConnID(connID)

	// Read the JSON body (should be a complete JSON-RPC response object).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("httpclient: read initialize body: %w", err)
	}
	// Validate it parses; forward raw bytes to the SDK otherwise.
	trimmed := bytes.TrimSpace(body)
	if !json.Valid(trimmed) {
		return fmt.Errorf("httpclient: initialize body is not valid JSON: %s", trimmed)
	}

	t.logger.Info("initialize OK", "connection_id", connID)

	// Open the connection-scoped GET stream BEFORE surfacing the
	// initialize response to the SDK. This way, any server-initiated
	// notifications that immediately follow (there shouldn't be any
	// before session/new, but be safe) land on the stream rather than
	// being dropped.
	t.openConnectionStream()

	t.pushInbound(trimmed)
	return nil
}
