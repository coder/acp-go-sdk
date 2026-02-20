package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

type anyMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RequestError    `json:"error,omitempty"`
}

type pendingResponse struct {
	ch chan anyMessage
}

type cancelRequestParams struct {
	RequestID json.RawMessage `json:"requestId"`
}

type MethodHandler func(ctx context.Context, method string, params json.RawMessage) (any, *RequestError)

// Connection is a simple JSON-RPC 2.0 connection over line-delimited JSON.
type Connection struct {
	w       io.Writer
	r       io.Reader
	handler MethodHandler

	mu                   sync.Mutex
	writeMu              sync.Mutex
	nextID               atomic.Uint64
	pending              map[string]*pendingResponse
	inflight             map[string]context.CancelCauseFunc
	pendingCancelRequest []string
	cancelRequestSignal  chan struct{}

	ctx    context.Context
	cancel context.CancelCauseFunc

	logger *slog.Logger

	// notificationWg tracks in-flight notification handlers.  This ensures SendRequest waits
	// for all notifications received before the response to complete processing.
	notificationWg sync.WaitGroup
}

func NewConnection(handler MethodHandler, peerInput io.Writer, peerOutput io.Reader) *Connection {
	ctx, cancel := context.WithCancelCause(context.Background())
	c := &Connection{
		w:                   peerInput,
		r:                   peerOutput,
		handler:             handler,
		pending:             make(map[string]*pendingResponse),
		inflight:            make(map[string]context.CancelCauseFunc),
		cancelRequestSignal: make(chan struct{}, 1),
		ctx:                 ctx,
		cancel:              cancel,
	}
	go c.sendCancelRequests()
	go c.receive()
	return c
}

// SetLogger installs a logger used for internal connection diagnostics.
// If unset, logs are written via the default logger.
func (c *Connection) SetLogger(l *slog.Logger) { c.logger = l }

func (c *Connection) loggerOrDefault() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

const (
	maxCanonicalJSONRPCIDKeyLen   = 4096
	maxCanonicalJSONRPCIDAbsExp10 = 4096
	maxPendingCancelRequests      = 1024
)

var (
	errInvalidJSONRPCNumericID  = errors.New("invalid json-rpc numeric id")
	errJSONRPCNumericIDTooLarge = errors.New("json-rpc numeric id too large")
)

func canonicalJSONRPCIDKey(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", errors.New("empty json-rpc id")
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()

	var id any
	if err := dec.Decode(&id); err != nil {
		return "", err
	}

	// Ensure the id contains a single JSON value.
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return "", errors.New("invalid json-rpc id: trailing data")
	} else if !errors.Is(err, io.EOF) {
		return "", err
	}

	switch v := id.(type) {
	case nil:
		return "null", nil
	case string:
		canon, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(canon), nil
	case json.Number:
		return canonicalJSONRPCNumericIDKey(v)
	default:
		return "", errors.New("json-rpc id must be string, number, or null")
	}
}

func canonicalJSONRPCNumericIDKey(v json.Number) (string, error) {
	raw := strings.TrimSpace(v.String())
	if raw == "" {
		return "", errInvalidJSONRPCNumericID
	}

	negative, digits, exp10, err := parseJSONRPCNumericID(raw)
	if err != nil {
		return "", err
	}

	return formatCanonicalJSONRPCNumericID(negative, digits, exp10)
}

func parseJSONRPCNumericID(raw string) (negative bool, digits string, exp10 int, err error) {
	i := 0
	if raw[i] == '-' {
		negative = true
		i++
		if i >= len(raw) {
			return false, "", 0, errInvalidJSONRPCNumericID
		}
	}

	intStart := i
	switch {
	case raw[i] == '0':
		i++
		if i < len(raw) && isASCIIDigit(raw[i]) {
			return false, "", 0, errInvalidJSONRPCNumericID
		}
	case raw[i] >= '1' && raw[i] <= '9':
		for i < len(raw) && isASCIIDigit(raw[i]) {
			i++
		}
	default:
		return false, "", 0, errInvalidJSONRPCNumericID
	}
	intDigits := raw[intStart:i]

	fracDigits := ""
	if i < len(raw) && raw[i] == '.' {
		i++
		fracStart := i
		for i < len(raw) && isASCIIDigit(raw[i]) {
			i++
		}
		if fracStart == i {
			return false, "", 0, errInvalidJSONRPCNumericID
		}
		fracDigits = raw[fracStart:i]
	}

	exponent := 0
	if i < len(raw) && (raw[i] == 'e' || raw[i] == 'E') {
		i++
		if i >= len(raw) {
			return false, "", 0, errInvalidJSONRPCNumericID
		}

		exponentSign := 1
		if raw[i] == '+' || raw[i] == '-' {
			if raw[i] == '-' {
				exponentSign = -1
			}
			i++
			if i >= len(raw) {
				return false, "", 0, errInvalidJSONRPCNumericID
			}
		}

		exponentStart := i
		for i < len(raw) && isASCIIDigit(raw[i]) {
			i++
		}
		if exponentStart == i {
			return false, "", 0, errInvalidJSONRPCNumericID
		}

		exponentMagnitude, parseErr := parseBoundedInt(raw[exponentStart:i], maxCanonicalJSONRPCIDAbsExp10)
		if parseErr != nil {
			return false, "", 0, parseErr
		}
		exponent = exponentSign * exponentMagnitude
	}

	if i != len(raw) {
		return false, "", 0, errInvalidJSONRPCNumericID
	}

	digits = strings.TrimLeft(intDigits+fracDigits, "0")
	if digits == "" {
		return false, "", 0, nil
	}
	if len(digits) > maxCanonicalJSONRPCIDKeyLen {
		return false, "", 0, errJSONRPCNumericIDTooLarge
	}

	exp10 = exponent - len(fracDigits)
	return negative, digits, exp10, nil
}

func parseBoundedInt(raw string, max int) (int, error) {
	if raw == "" {
		return 0, errInvalidJSONRPCNumericID
	}

	value := 0
	for i := 0; i < len(raw); i++ {
		if !isASCIIDigit(raw[i]) {
			return 0, errInvalidJSONRPCNumericID
		}
		digit := int(raw[i] - '0')
		if value > (max-digit)/10 {
			return 0, errJSONRPCNumericIDTooLarge
		}
		value = value*10 + digit
	}
	return value, nil
}

func isASCIIDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

func formatCanonicalJSONRPCNumericID(negative bool, digits string, exp10 int) (string, error) {
	for len(digits) > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		exp10++
	}

	if digits == "" {
		return "0", nil
	}

	sign := ""
	if negative {
		sign = "-"
	}

	if exp10 >= 0 {
		if exp10 > maxCanonicalJSONRPCIDKeyLen-len(digits) {
			return "", errJSONRPCNumericIDTooLarge
		}
		result := digits + strings.Repeat("0", exp10)
		if sign != "" {
			result = sign + result
		}
		if len(result) > maxCanonicalJSONRPCIDKeyLen+len(sign) {
			return "", errJSONRPCNumericIDTooLarge
		}
		return result, nil
	}

	scale := -exp10
	if scale > maxCanonicalJSONRPCIDKeyLen {
		return "", errJSONRPCNumericIDTooLarge
	}

	if len(digits) > scale {
		intPart := digits[:len(digits)-scale]
		fracPart := digits[len(digits)-scale:]
		if len(intPart)+1+len(fracPart) > maxCanonicalJSONRPCIDKeyLen {
			return "", errJSONRPCNumericIDTooLarge
		}
		result := intPart + "." + fracPart
		if sign != "" {
			result = sign + result
		}
		return result, nil
	}

	leadingZeros := scale - len(digits)
	if leadingZeros > maxCanonicalJSONRPCIDKeyLen-len(digits)-2 {
		return "", errJSONRPCNumericIDTooLarge
	}
	result := "0." + strings.Repeat("0", leadingZeros) + digits
	if sign != "" {
		result = sign + result
	}
	return result, nil
}

func (c *Connection) receive() {
	const (
		initialBufSize = 1024 * 1024
		maxBufSize     = 10 * 1024 * 1024
	)

	scanner := bufio.NewScanner(c.r)
	buf := make([]byte, 0, initialBufSize)
	scanner.Buffer(buf, maxBufSize)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var msg anyMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.loggerOrDefault().Error("failed to parse incoming message", "err", err, "raw", string(line))
			continue
		}

		// Handle $/cancel_request notifications synchronously so cancellations take effect
		// immediately and do not participate in notification ordering.
		if msg.ID == nil && msg.Method == "$/cancel_request" {
			c.handleCancelRequest(&msg)
			continue
		}

		switch {
		case msg.ID != nil && msg.Method == "":
			c.handleResponse(&msg)
		case msg.Method != "":
			if msg.ID != nil {
				idKey, err := canonicalJSONRPCIDKey(*msg.ID)
				if err != nil {
					c.loggerOrDefault().Error("failed to canonicalize inbound request id", "err", err, "id", string(*msg.ID))
					idKey = string(*msg.ID)
				}
				reqCtx, cancel := context.WithCancelCause(c.ctx)

				c.mu.Lock()
				c.inflight[idKey] = cancel
				c.mu.Unlock()

				m := msg
				go func(m *anyMessage, idKey string, reqCtx context.Context, cancel context.CancelCauseFunc) {
					defer func() {
						c.mu.Lock()
						delete(c.inflight, idKey)
						c.mu.Unlock()

						cancel(nil)
					}()
					c.handleInbound(reqCtx, m)
				}(&m, idKey, reqCtx, cancel)
				continue
			}

			// Only track notifications (no ID) in the WaitGroup.
			c.notificationWg.Add(1)
			m := msg
			go func(m *anyMessage) {
				defer c.notificationWg.Done()
				c.handleInbound(c.ctx, m)
			}(&m)
		default:
			c.loggerOrDefault().Error("received message with neither id nor method", "raw", string(line))
		}
	}

	c.cancel(errors.New("peer connection closed"))
	c.loggerOrDefault().Info("peer connection closed")
}

func (c *Connection) handleResponse(msg *anyMessage) {
	idStr, err := canonicalJSONRPCIDKey(*msg.ID)
	if err != nil {
		c.loggerOrDefault().Error("failed to canonicalize response id", "err", err, "id", string(*msg.ID))
		idStr = string(*msg.ID)
	}

	c.mu.Lock()
	pr := c.pending[idStr]
	if pr != nil {
		delete(c.pending, idStr)
	}
	c.mu.Unlock()

	if pr != nil {
		pr.ch <- *msg
	}
}

func (c *Connection) handleCancelRequest(msg *anyMessage) {
	var p cancelRequestParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		c.loggerOrDefault().Error("failed to parse $/cancel_request params", "err", err)
		return
	}
	if len(bytes.TrimSpace(p.RequestID)) == 0 {
		c.loggerOrDefault().Error("received $/cancel_request without requestId")
		return
	}

	idKey, err := canonicalJSONRPCIDKey(p.RequestID)
	if err != nil {
		c.loggerOrDefault().Error("failed to canonicalize $/cancel_request requestId", "err", err, "requestId", string(p.RequestID))
		idKey = string(p.RequestID)
	}

	c.mu.Lock()
	cancel := c.inflight[idKey]
	c.mu.Unlock()
	if cancel == nil {
		return
	}

	cancel(context.Canceled)
}

func (c *Connection) handleInbound(ctx context.Context, req *anyMessage) {
	res := anyMessage{JSONRPC: "2.0"}
	// copy ID if present
	if req.ID != nil {
		res.ID = req.ID
	}
	if c.handler == nil {
		if req.ID != nil {
			res.Error = NewMethodNotFound(req.Method)
			_ = c.sendMessage(res)
		}
		return
	}

	result, err := c.handler(ctx, req.Method, req.Params)
	if req.ID == nil {
		// Notification: no response is sent; log handler errors to surface decode failures.
		if err != nil {
			// Per ACP, unknown extension notifications should be ignored.
			if err.Code == -32601 && strings.HasPrefix(req.Method, "_") {
				return
			}
			c.loggerOrDefault().Error("failed to handle notification", "method", req.Method, "err", err)
		}
		return
	}
	if err != nil {
		res.Error = err
	} else {
		// marshal result
		b, mErr := json.Marshal(result)
		if mErr != nil {
			res.Error = NewInternalError(map[string]any{"error": mErr.Error()})
		} else {
			res.Result = b
		}
	}
	_ = c.sendMessage(res)
}

func (c *Connection) sendMessage(msg anyMessage) error {
	msg.JSONRPC = "2.0"
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(b)
	return err
}

// SendRequest sends a JSON-RPC request and returns a typed result.
// For methods that do not return a result, use SendRequestNoResult instead.
func SendRequest[T any](c *Connection, ctx context.Context, method string, params any) (T, error) {
	var result T

	msg, idKey, err := c.prepareRequest(method, params)
	if err != nil {
		return result, err
	}

	pr := &pendingResponse{ch: make(chan anyMessage, 1)}
	c.mu.Lock()
	c.pending[idKey] = pr
	c.mu.Unlock()

	if err := c.sendMessage(msg); err != nil {
		c.cleanupPending(idKey)
		return result, NewInternalError(map[string]any{"error": err.Error()})
	}

	resp, err := c.waitForResponse(ctx, pr, idKey)
	if err != nil {
		return result, err
	}

	// Wait for all notification handlers that were spawned before this response to complete
	// processing.  This ensures that when a request returns, all notifications sent by the
	// server before the response have been fully processed.
	c.notificationWg.Wait()

	if resp.Error != nil {
		return result, resp.Error
	}

	if len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return result, NewInternalError(map[string]any{"error": err.Error()})
		}
	}
	return result, nil
}

func (c *Connection) prepareRequest(method string, params any) (anyMessage, string, error) {
	id := c.nextID.Add(1)
	idRaw, _ := json.Marshal(id)

	msg := anyMessage{
		JSONRPC: "2.0",
		ID:      (*json.RawMessage)(&idRaw),
		Method:  method,
	}

	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return msg, "", NewInvalidParams(map[string]any{"error": err.Error()})
		}
		msg.Params = b
	}

	return msg, string(idRaw), nil
}

func (c *Connection) sendCancelRequests() {
	for {
		select {
		case <-c.Done():
			return
		case <-c.cancelRequestSignal:
			for {
				c.mu.Lock()
				if len(c.pendingCancelRequest) == 0 {
					c.mu.Unlock()
					break
				}
				idKey := c.pendingCancelRequest[0]
				c.pendingCancelRequest = c.pendingCancelRequest[1:]
				c.mu.Unlock()

				requestID := json.RawMessage(append([]byte(nil), idKey...))
				if err := c.SendNotification(context.Background(), "$/cancel_request", cancelRequestParams{RequestID: requestID}); err != nil {
					c.loggerOrDefault().Debug("failed to send $/cancel_request", "err", err)
				}
			}
		}
	}
}

func (c *Connection) sendCancelRequest(idKey string) {
	if strings.TrimSpace(idKey) == "" {
		return
	}

	select {
	case <-c.Done():
		return
	default:
	}

	queueFull := false
	c.mu.Lock()
	if len(c.pendingCancelRequest) >= maxPendingCancelRequests {
		queueFull = true
	} else {
		c.pendingCancelRequest = append(c.pendingCancelRequest, idKey)
	}
	c.mu.Unlock()

	if queueFull {
		c.loggerOrDefault().Debug("dropping $/cancel_request due to full queue", "queue_len", maxPendingCancelRequests)
		return
	}

	select {
	case c.cancelRequestSignal <- struct{}{}:
	default:
	}
}

func (c *Connection) waitForResponse(ctx context.Context, pr *pendingResponse, idKey string) (anyMessage, error) {
	peerDisconnectedErr := NewInternalError(map[string]any{"error": "peer disconnected before response"})

	select {
	case resp := <-pr.ch:
		return resp, nil
	case <-ctx.Done():
		// If the connection dropped at the same time, prefer reporting peer disconnect
		// and avoid queueing a best-effort cancel notification to a dead peer.
		select {
		case <-c.Done():
			c.cleanupPending(idKey)
			return anyMessage{}, peerDisconnectedErr
		default:
		}

		c.sendCancelRequest(idKey)
		c.cleanupPending(idKey)

		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		}
		if cause != nil {
			return anyMessage{}, toReqErr(cause)
		}
		return anyMessage{}, NewInternalError(map[string]any{"error": "request context ended without cause"})
	case <-c.Done():
		c.cleanupPending(idKey)
		return anyMessage{}, peerDisconnectedErr
	}
}

func (c *Connection) cleanupPending(idKey string) {
	c.mu.Lock()
	delete(c.pending, idKey)
	c.mu.Unlock()
}

// SendRequestNoResult sends a JSON-RPC request that returns no result payload.
func (c *Connection) SendRequestNoResult(ctx context.Context, method string, params any) error {
	msg, idKey, err := c.prepareRequest(method, params)
	if err != nil {
		return err
	}

	pr := &pendingResponse{ch: make(chan anyMessage, 1)}
	c.mu.Lock()
	c.pending[idKey] = pr
	c.mu.Unlock()

	if err := c.sendMessage(msg); err != nil {
		c.cleanupPending(idKey)
		return NewInternalError(map[string]any{"error": err.Error()})
	}

	resp, err := c.waitForResponse(ctx, pr, idKey)
	if err != nil {
		return err
	}

	// Wait for all notification handlers that were spawned before this response to complete
	// processing.  This ensures that when a request returns, all notifications sent by the
	// server before the response have been fully processed.
	c.notificationWg.Wait()

	if resp.Error != nil {
		return resp.Error
	}
	return nil
}

func (c *Connection) SendNotification(ctx context.Context, method string, params any) error {
	select {
	case <-ctx.Done():
		return NewInternalError(map[string]any{"error": ctx.Err().Error()})
	default:
	}

	msg, err := c.prepareNotification(method, params)
	if err != nil {
		return err
	}

	if err := c.sendMessage(msg); err != nil {
		return NewInternalError(map[string]any{"error": err.Error()})
	}
	return nil
}

func (c *Connection) prepareNotification(method string, params any) (anyMessage, error) {
	msg := anyMessage{
		JSONRPC: "2.0",
		Method:  method,
	}

	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return msg, NewInvalidParams(map[string]any{"error": err.Error()})
		}
		msg.Params = b
	}

	return msg, nil
}

// Done returns a channel that is closed when the underlying reader loop exits
// (typically when the peer disconnects or the input stream is closed).
func (c *Connection) Done() <-chan struct{} {
	return c.ctx.Done()
}
