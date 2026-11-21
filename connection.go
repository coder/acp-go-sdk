package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const notificationQueueDrainTimeout = 5 * time.Second

var errNotificationQueueOverflow = errors.New("notification queue overflow")

type anyMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RequestError    `json:"error,omitempty"`
}

type queuedNotification struct {
	seq     uint64
	arrival uint64
	msg     *anyMessage
	size    int
}

// handlingNotification is the ctx value that processNotifications gives each
// notification handler. arrival is the notification's place among the
// messages that receive dispatched (see Connection.inboundMessages).
type handlingNotification struct {
	conn    *Connection
	seq     uint64
	arrival uint64
}

type handlingNotificationKey struct{}

// handlingRequest is the ctx value that receive gives each handler of a
// request from the peer. arrival is the request's place among the messages
// that receive dispatched (see Connection.inboundMessages).
type handlingRequest struct {
	conn    *Connection
	arrival uint64
}

type handlingRequestKey struct{}

// notificationSeq returns the sequence number of the notification from c
// whose handler runs with ctx.
func (c *Connection) notificationSeq(ctx context.Context) (uint64, bool) {
	h, ok := ctx.Value(handlingNotificationKey{}).(handlingNotification)
	if !ok || h.conn != c {
		return 0, false
	}
	return h.seq, true
}

// messageArrival returns the arrival number of the request or notification
// from c whose handler runs with ctx. A message with a greater number arrived
// after it.
func (c *Connection) messageArrival(ctx context.Context) (uint64, bool) {
	if h, ok := ctx.Value(handlingRequestKey{}).(handlingRequest); ok && h.conn == c {
		return h.arrival, true
	}
	if h, ok := ctx.Value(handlingNotificationKey{}).(handlingNotification); ok && h.conn == c {
		return h.arrival, true
	}
	return 0, false
}

type responseEnvelope struct {
	msg                   anyMessage
	notificationWatermark uint64
}

type pendingResponse struct {
	ch chan responseEnvelope
	// arrived, when set, runs in handleResponse with the response's arrival
	// number, before receive reads the next message.
	arrived func(arrival uint64)
}

type cancelRequestParams struct {
	RequestID json.RawMessage `json:"requestId"`
}

// MethodHandler handles a request or a notification from the peer. It never
// gets a $/cancel_request notification: the connection handles those itself.
// The connection calls it by these rules:
//
//   - Notification handlers run one at a time, in arrival order, on one
//     goroutine.
//   - Each request handler runs on its own goroutine. It starts after the
//     handlers of all notifications that arrived before the request have
//     returned, so a slow notification handler delays every later
//     notification and, except as the next two rules say, every later
//     request. Notifications that arrive after the request do not delay it.
//   - A request handler also starts while the running notification handler
//     waits for the response to a request that it made with its ctx, because
//     the peer can need this request's answer first.
//   - If the peer disconnects while a request waits to start, its handler
//     starts at once with a cancelled ctx.
//   - If a $/cancel_request for the request arrives while the request waits to
//     start, the peer gets a -32800 error response and the handler never runs.
//     When the peer disconnects right after the cancel, the waiting request
//     can see the disconnect first. The handler then starts with a cancelled
//     ctx, as on any disconnect during the wait.
//     A later $/cancel_request, even one just before the handler starts, only cancels its ctx.
//
// A request that a handler makes waits for notification handlers too, as
// SendRequest describes. A request that a request handler makes with its ctx
// can return while the running notification handler waits for the peer, so
// the request handler can go on before earlier notifications have run. On an
// AgentSideConnection, a session/cancel queued behind that notification
// handler then reaches a running prompt late: RequestPermission in Prompt can
// return the client's answer, which the client sent after the cancel, while
// the prompt's ctx is still live. The ctx ends once the earlier handler
// returns and the cancel runs.
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
	inboundMessages      uint64              // requests, queued notifications and responses that receive dispatched; only receive touches it
	inflightArrivals     map[uint64]struct{} // arrival numbers of the requests in inflight
	pendingCancelRequest []string
	cancelRequestSignal  chan struct{}

	// ctx/cancel govern connection lifetime and are used for Done() and for canceling
	// callers waiting on responses when the peer disconnects.
	ctx    context.Context
	cancel context.CancelCauseFunc

	// inboundCtx/inboundCancel are used when invoking the inbound MethodHandler.
	// This ctx is intentionally kept alive long enough to process notifications
	// that were successfully received and queued just before a peer disconnect.
	// Otherwise, handlers that respect context cancellation may drop end-of-connection
	// messages that we already read off the wire.
	inboundCtx    context.Context
	inboundCancel context.CancelCauseFunc

	// logger is atomic because SetLogger may be called after NewConnection has
	// already spawned the receive/processNotifications goroutines that read it
	// via log.
	logger atomic.Pointer[slog.Logger]

	notifyMu sync.Mutex
	// notifyCond coordinates response-scoped waits for sequential notification processing.
	notifyCond *sync.Cond
	// invariant: completedNotificationSeq <= lastEnqueuedNotificationSeq.
	lastEnqueuedNotificationSeq uint64
	completedNotificationSeq    uint64
	// peerWaits counts, per notification sequence number, the requests that
	// were made with the ctx of that notification's running handler and still
	// wait for the peer's response.
	peerWaits map[uint64]int

	// notificationQueue serializes notification processing to maintain order.
	// It is bounded to keep memory usage predictable.
	notificationQueue chan queuedNotification

	// rejections holds the replies to requests that the connection does not
	// hold, for writeRejections, so that receive never writes to the peer.
	rejections chan []byte

	budget peerBudget

	hooks connectionHooks
}

// connectionHooks let a role built on Connection see how the messages from
// the peer arrive. Only the proxy role sets them. receive calls each of them,
// so they must return at once.
type connectionHooks struct {
	// requestArrived runs for each request from the peer that the connection
	// holds, before receive reads the next message. The func that it returns
	// runs when the request's goroutine ends, after the reply went out.
	requestArrived func(arrival uint64, method string) (done func())
	// wrappedCancel returns the params of the $/cancel_request that a
	// notification from the peer wraps, if it wraps one. The connection
	// handles that $/cancel_request as a plain one.
	wrappedCancel func(method string, params json.RawMessage) (json.RawMessage, bool)
}

// withHooks sets the hooks of a connection. Only the proxy role passes it.
func withHooks(h connectionHooks) ConnectionOption {
	return func(c *connectionConfig) { c.hooks = h }
}

// NewConnection returns a Connection that writes messages to peerInput and
// reads them from peerOutput. It starts reading at once and calls handler for
// each request and notification from the peer that fits its limits, except a
// $/cancel_request notification. The connection handles that one itself, at
// once: it cancels the ctx of the in-flight request from the peer with that
// id, or ignores an unknown id. Notification handlers run one at a time, in
// arrival order. A request handler waits for the handlers of the notifications
// that arrived before the request. MethodHandler lists these rules and their
// exceptions. opts set the limits on what it holds from the peer: a request
// over a limit gets a -32603 reply instead of a handler call, and a
// notification over a limit closes the connection (see ConnectionOption).
//
// The connection never closes peerInput or peerOutput. A write to a peer that
// stopped reading blocks, and every later write waits behind it: a handler's
// reply, SendRequest and SendNotification. Nothing in the connection ends
// that write, not even Done or a ctx. Done closes when the peer's output ends
// or the connection closes itself on a limit, so a peer that hangs and sends
// nothing may never close it. The owner of the
// transport ends the write by closing it, or by ending the process behind it.
// Closing an io.Pipe, an os.Pipe, a net.Conn or a child's stdin
// (exec.Cmd.StdinPipe) ends a blocked write. Close it when you are done with
// the connection, for example when Done has closed and the handlers that
// still reply have returned, or when a call outlives a deadline of your own.
// Do not close it at once when Done closes: a peer that closed only its
// output still reads, and it would lose the replies of the running handlers.
//
// Closing os.Stdout ends a blocked write only when stdout is in non-blocking
// mode. A child's stdout pipe from os/exec, Node or Python is in blocking
// mode: there Close returns nil and the write stays blocked until the process
// exits, so an agent on os.Stdout must exit to end it.
func NewConnection(handler MethodHandler, peerInput io.Writer, peerOutput io.Reader, opts ...ConnectionOption) *Connection {
	cfg := connectionConfig{
		byteBudget:             defaultByteBudget,
		maxInboundRequests:     defaultMaxInboundRequests,
		maxQueuedNotifications: defaultMaxQueuedNotifications,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	inboundCtx, inboundCancel := context.WithCancelCause(context.Background())
	c := &Connection{
		w:                   peerInput,
		r:                   peerOutput,
		handler:             handler,
		pending:             make(map[string]*pendingResponse),
		inflight:            make(map[string]context.CancelCauseFunc),
		inflightArrivals:    make(map[uint64]struct{}),
		peerWaits:           make(map[uint64]int),
		cancelRequestSignal: make(chan struct{}, 1),
		ctx:                 ctx,
		cancel:              cancel,
		inboundCtx:          inboundCtx,
		inboundCancel:       inboundCancel,
		notificationQueue:   make(chan queuedNotification, cfg.maxQueuedNotifications),
		rejections:          make(chan []byte, maxQueuedRejections),
		budget:              peerBudget{maxBytes: cfg.byteBudget, maxRequests: cfg.maxInboundRequests},
		hooks:               cfg.hooks,
	}
	c.notifyCond = sync.NewCond(&c.notifyMu)
	go func() {
		<-c.ctx.Done()
		c.notifyMu.Lock()
		c.notifyCond.Broadcast()
		c.notifyMu.Unlock()
	}()
	go c.sendCancelRequests()
	go c.receive()
	go c.processNotifications()
	go c.writeRejections()
	return c
}

// SetLogger installs a logger used for internal connection diagnostics.
// If unset, logs are written via the default logger. A record carries at most
// the first 1 KiB of each text value, such as a bad input line or an error
// that quotes the peer, and then that value's length.
func (c *Connection) SetLogger(l *slog.Logger) { c.logger.Store(l) }

// log writes a connection diagnostic. Every log call in the connection goes
// through it, because any text value can come from the peer: a line, an id, a
// method, or an error that quotes them, such as a decode error that copies a
// number. args are key-value pairs, and log cuts each value with logValue.
func (c *Connection) log(level slog.Level, msg string, args ...any) {
	l := c.logger.Load()
	if l == nil {
		l = slog.Default()
	}
	ctx := context.Background()
	if !l.Enabled(ctx, level) {
		return
	}
	var pc [1]uintptr
	runtime.Callers(2, pc[:]) // the caller of log, so a source attribute names it
	r := slog.NewRecord(time.Now(), level, msg, pc[0])
	bounded := make([]any, len(args))
	copy(bounded, args)
	for i := 1; i < len(bounded); i += 2 {
		bounded[i] = logValue(bounded[i])
	}
	r.Add(bounded...)
	_ = l.Handler().Handle(ctx, r)
}

// logValue returns v for a log attribute, with its text cut by peerText. A
// number, bool, duration or time keeps its kind.
func logValue(v any) slog.Value {
	switch v := v.(type) {
	case json.RawMessage:
		return slog.StringValue(peerText(v))
	case []byte:
		return slog.StringValue(peerText(v))
	case error:
		return slog.StringValue(peerText(v.Error()))
	}
	val := slog.AnyValue(v).Resolve()
	switch val.Kind() {
	case slog.KindString, slog.KindAny, slog.KindGroup:
		return slog.StringValue(peerText(val.String()))
	}
	return val
}

// maxLoggedPeerBytes bounds the text in one log attribute.
const maxLoggedPeerBytes = 1024

// peerText returns s, text that the peer controls, for a log attribute: whole
// up to maxLoggedPeerBytes, else cut there at a rune start and followed by
// its length. One input line can be 10 MiB, and a record must not carry it.
func peerText[T ~string | ~[]byte](s T) string {
	if len(s) <= maxLoggedPeerBytes {
		return string(s)
	}
	n := maxLoggedPeerBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return string(s[:n]) + "... (" + strconv.Itoa(len(s)) + " bytes)"
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
		if err := json.Unmarshal(line, &msg); err != nil && !c.readUnreadableResponse(line, err, &msg) {
			c.log(slog.LevelError, "failed to parse incoming message", "err", err, "raw", line, "len", len(line))
			continue
		}

		// Handle $/cancel_request notifications synchronously so cancellations take effect
		// immediately and do not participate in notification ordering.
		if params, ok := c.cancelRequestParams(&msg); ok {
			c.handleCancelRequest(params)
			continue
		}

		switch {
		case msg.ID != nil && msg.Method == "":
			c.handleResponse(&msg)
		case msg.Method != "":
			size := len(line)
			if msg.ID != nil {
				idKey, err := canonicalJSONRPCIDKey(*msg.ID)
				if err != nil {
					c.log(slog.LevelError, "failed to canonicalize inbound request id", "err", err, "id", *msg.ID)
					idKey = string(*msg.ID)
				}
				// The request keeps its id twice: in msg for the reply, and
				// as the key in c.inflight.
				size += len(idKey)
				if rejection := c.budget.holdRequest(size); rejection != nil {
					c.log(slog.LevelWarn, "rejected request from the peer", "method", msg.Method, "err", rejection)
					if !c.queueRejection(&msg, rejection) {
						c.log(slog.LevelError, "the peer does not read the rejections of its requests; closing connection", "err", errRejectionQueueOverflow, "method", msg.Method, "queued", len(c.rejections))
						c.shutdownReceive(errRejectionQueueOverflow)
						return
					}
					continue
				}
				// Requests observe the notification queue as it stood when they arrived.
				// Notifications already accepted onto the queue must finish before the
				// handler starts, while later notifications must not delay the request.
				// The handler also starts while the running notification handler waits
				// for the peer, because the peer can need this answer first.
				c.notifyMu.Lock()
				watermark := c.lastEnqueuedNotificationSeq
				c.notifyMu.Unlock()

				reqCtx, cancel := context.WithCancelCause(c.ctx)
				c.inboundMessages++
				arrival := c.inboundMessages
				reqCtx = context.WithValue(reqCtx, handlingRequestKey{}, handlingRequest{conn: c, arrival: arrival})

				c.mu.Lock()
				c.inflight[idKey] = cancel
				c.inflightArrivals[arrival] = struct{}{}
				c.mu.Unlock()
				arrivedDone := func() {}
				if c.hooks.requestArrived != nil {
					arrivedDone = c.hooks.requestArrived(arrival, msg.Method)
				}

				m := msg
				go func(m *anyMessage, idKey string, reqCtx context.Context, cancel context.CancelCauseFunc, watermark uint64) {
					defer func() {
						c.mu.Lock()
						delete(c.inflight, idKey)
						delete(c.inflightArrivals, arrival)
						c.mu.Unlock()

						cancel(nil)
						c.budget.releaseRequest(size)
						arrivedDone()
					}()
					if err := c.waitInboundBarrier(reqCtx, watermark); err != nil {
						select {
						case <-c.Done():
							// Preserve the existing disconnect contract: start accepted request
							// handlers with a canceled context so they can run cleanup even when
							// an earlier notification is still draining. The peer can no longer
							// observe notification/request ordering after disconnect.
							c.handleInbound(reqCtx, m)
							return
						default:
						}
						c.sendInboundRequestError(m, toReqErr(err))
						return
					}
					c.handleInbound(reqCtx, m)
				}(&m, idKey, reqCtx, cancel, watermark)
				continue
			}

			if !c.budget.holdNotification(size) {
				c.log(slog.LevelError, "notification does not fit the byte budget; closing connection", "err", errByteBudgetExceeded, "method", msg.Method, "size", size, "budget", c.budget.maxBytes)
				c.shutdownReceive(errByteBudgetExceeded)
				return
			}

			// Queue the notification for sequential processing. The sequence number marks
			// the response-scoped barrier boundary for requests that observe later responses.
			m := msg
			c.inboundMessages++
			c.notifyMu.Lock()
			c.lastEnqueuedNotificationSeq++
			seq := c.lastEnqueuedNotificationSeq
			select {
			case c.notificationQueue <- queuedNotification{seq: seq, arrival: c.inboundMessages, msg: &m, size: size}:
				c.notifyMu.Unlock()
			default:
				if c.lastEnqueuedNotificationSeq != seq {
					c.notifyMu.Unlock()
					panic("notification sequence advanced while receive goroutine was queueing")
				}
				c.lastEnqueuedNotificationSeq--
				// invariant: completedNotificationSeq never exceeds the highest accepted enqueue.
				if c.completedNotificationSeq > c.lastEnqueuedNotificationSeq {
					c.notifyMu.Unlock()
					panic("completed notification sequence exceeded enqueued notification sequence")
				}
				c.notifyMu.Unlock()
				c.budget.releaseNotification(size)
				c.log(slog.LevelError, "failed to queue notification; closing connection", "err", errNotificationQueueOverflow, "capacity", cap(c.notificationQueue), "queued", len(c.notificationQueue))
				c.shutdownReceive(errNotificationQueueOverflow)
				return
			}
		default:
			c.log(slog.LevelError, "received message with neither id nor method", "raw", line, "len", len(line))
		}
	}

	cause := errors.New("peer connection closed")
	if err := scanner.Err(); err != nil {
		cause = err
	}
	c.shutdownReceive(cause)
}

func (c *Connection) shutdownReceive(cause error) {
	if cause == nil {
		cause = errors.New("connection closed")
	}

	// First, signal disconnect to callers waiting on responses.
	c.cancel(cause)

	// Then close the notification queue so already-received messages can drain.
	// IMPORTANT: Do not block this receive goroutine waiting for the drain to complete;
	// notification handlers may legitimately block until their context is canceled.
	close(c.notificationQueue)
	close(c.rejections)

	c.notifyMu.Lock()
	finalEnqueuedSeq := c.lastEnqueuedNotificationSeq
	if c.completedNotificationSeq > finalEnqueuedSeq {
		c.notifyMu.Unlock()
		panic("completed notification sequence exceeded final enqueued sequence during shutdown")
	}
	c.notifyMu.Unlock()

	// Cancel inboundCtx after notifications finish, but ensure we don't leak forever if a
	// handler blocks waiting for cancellation.
	go func(finalEnqueuedSeq uint64) {
		c.waitForNotificationDrain(finalEnqueuedSeq, notificationQueueDrainTimeout)
		c.inboundCancel(cause)
	}(finalEnqueuedSeq)

	c.log(slog.LevelInfo, "connection closed", "cause", cause)
}

// processNotifications processes notifications sequentially to maintain order.
// It terminates when notificationQueue is closed (e.g. on disconnect in receive()).
func (c *Connection) processNotifications() {
	for queued := range c.notificationQueue {
		handlerCtx := context.WithValue(c.inboundCtx, handlingNotificationKey{}, handlingNotification{conn: c, seq: queued.seq, arrival: queued.arrival})
		c.handleInbound(handlerCtx, queued.msg)
		c.budget.releaseNotification(queued.size)

		c.notifyMu.Lock()
		expectedSeq := c.completedNotificationSeq + 1
		if queued.seq != expectedSeq {
			c.notifyMu.Unlock()
			panic("notification sequence completed out of order")
		}
		c.completedNotificationSeq = queued.seq
		if c.completedNotificationSeq > c.lastEnqueuedNotificationSeq {
			c.notifyMu.Unlock()
			panic("completed notification sequence exceeded enqueued notification sequence")
		}
		c.notifyCond.Broadcast()
		c.notifyMu.Unlock()
	}
}

func (c *Connection) handleResponse(msg *anyMessage) {
	idStr, err := canonicalJSONRPCIDKey(*msg.ID)
	if err != nil {
		c.log(slog.LevelError, "failed to canonicalize response id", "err", err, "id", *msg.ID)
		idStr = string(*msg.ID)
	}

	c.mu.Lock()
	pr := c.pending[idStr]
	if pr != nil {
		delete(c.pending, idStr)
	}
	c.mu.Unlock()

	if pr != nil {
		c.inboundMessages++
		if pr.arrived != nil {
			pr.arrived(c.inboundMessages)
		}
		c.notifyMu.Lock()
		watermark := c.lastEnqueuedNotificationSeq
		if c.completedNotificationSeq > watermark {
			c.notifyMu.Unlock()
			panic("completed notification sequence exceeded response watermark")
		}
		c.notifyMu.Unlock()
		pr.ch <- responseEnvelope{msg: *msg, notificationWatermark: watermark}
	}
}

// cancelRequestParams returns the params of the $/cancel_request that msg is:
// a plain one, or one that hooks.wrappedCancel finds in it.
func (c *Connection) cancelRequestParams(msg *anyMessage) (json.RawMessage, bool) {
	switch {
	case msg.ID != nil:
		return nil, false
	case msg.Method == "$/cancel_request":
		return msg.Params, true
	case c.hooks.wrappedCancel != nil:
		return c.hooks.wrappedCancel(msg.Method, msg.Params)
	}
	return nil, false
}

func (c *Connection) handleCancelRequest(params json.RawMessage) {
	var p cancelRequestParams
	if err := json.Unmarshal(params, &p); err != nil {
		c.log(slog.LevelError, "failed to parse $/cancel_request params", "err", err)
		return
	}
	if len(bytes.TrimSpace(p.RequestID)) == 0 {
		c.log(slog.LevelError, "received $/cancel_request without requestId")
		return
	}

	idKey, err := canonicalJSONRPCIDKey(p.RequestID)
	if err != nil {
		c.log(slog.LevelError, "failed to canonicalize $/cancel_request requestId", "err", err, "requestId", p.RequestID)
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
			if err.Code == -32601 {
				// Per ACP, unknown extension notifications should be ignored.
				if strings.HasPrefix(req.Method, "_") {
					return
				}
				// A notification needs no answer, so one that this side does
				// not handle, such as an optional method that the Client or
				// Agent does not implement, is not a failure.
				c.log(slog.LevelDebug, "ignored unhandled notification", "method", req.Method, "err", err)
				return
			}
			c.log(slog.LevelError, "failed to handle notification", "method", req.Method, "err", err)
		}
		return
	}
	if err != nil {
		res.Error = c.encodableError(req.Method, err)
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

func (c *Connection) sendInboundRequestError(req *anyMessage, err *RequestError) {
	if req.ID == nil {
		return
	}
	_ = c.sendMessage(anyMessage{
		JSONRPC: "2.0",
		ID:      req.ID,
		Error:   err,
	})
}

// queueRejection queues the reply to a request that the connection does not
// hold, charged at its length. It reports false when the queue is full: the
// peer does not read its replies.
func (c *Connection) queueRejection(req *anyMessage, rejection *RequestError) bool {
	line, err := encodeLine(anyMessage{ID: req.ID, Error: rejection})
	if err != nil {
		c.log(slog.LevelError, "failed to encode a rejection", "err", err)
		return true
	}
	if !c.budget.holdRejection(len(line)) {
		return false
	}
	select {
	case c.rejections <- line:
		return true
	default:
		c.budget.releaseRejection(len(line))
		return false
	}
}

// writeRejections writes the queued rejections until shutdownReceive closes
// the queue. It is the only goroutine that waits for the peer to read them.
func (c *Connection) writeRejections() {
	for line := range c.rejections {
		_ = c.writeLine(line)
		c.budget.releaseRejection(len(line))
	}
}

func (c *Connection) sendMessage(msg anyMessage) error {
	line, err := encodeLine(msg)
	if err != nil {
		return err
	}
	return c.writeLine(line)
}

func encodeLine(msg anyMessage) ([]byte, error) {
	msg.JSONRPC = "2.0"
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func (c *Connection) writeLine(line []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.w.Write(line)
	return err
}

// SendRequest sends a JSON-RPC request and returns a typed result.
// For methods that do not return a result, use SendRequestNoResult instead.
//
// SendRequest returns after the handlers of all notifications that arrived
// before the response have returned. A notification handler that makes a
// request on the same connection must pass its own ctx, or a ctx derived from
// it. While the handler runs, such a request waits only for the notifications
// before the handler's own. With any other ctx, such as context.Background(),
// the request waits for the handler that made it, so it returns only when ctx
// ends or the peer disconnects. The shorter wait applies to every goroutine that
// uses the handler's ctx while the handler runs: a goroutine that the handler
// starts and that needs the full wait must get a ctx that does not derive from
// the handler's ctx.
//
// A request that the handler of a request from the peer makes with its own
// ctx, or a ctx derived from it, gets the full wait with one exception. While
// the request handler runs and the running notification handler waits for a
// response to a request that it made with its ctx, SendRequest does not wait
// for that notification handler or the notifications after it, because the
// peer can need the request handler's answer before it sends that response.
// A ctx that outlives the request handler, such as one from
// context.WithoutCancel, gets the full wait once the handler has returned.
func SendRequest[T any](c *Connection, ctx context.Context, method string, params any) (T, error) {
	var result T
	sent, err := c.sendRequest(method, params, nil)
	if err != nil {
		return result, err
	}
	raw, err := sent.wait(ctx)
	if err != nil {
		return result, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &result); err != nil {
			return result, NewInternalError(map[string]any{"error": err.Error()})
		}
	}
	return result, nil
}

// sentRequest is a request that this side has written to the peer. Its wait
// waits for the response.
type sentRequest struct {
	c     *Connection
	idKey string
	pr    *pendingResponse
}

// sendRequest writes a request to the peer, the first step of SendRequest.
// arrived, when set, runs when the response arrives, with its arrival number
// (see pendingResponse).
func (c *Connection) sendRequest(method string, params any, arrived func(arrival uint64)) (*sentRequest, error) {
	msg, idKey, err := c.prepareRequest(method, params)
	if err != nil {
		return nil, err
	}

	pr := &pendingResponse{ch: make(chan responseEnvelope, 1), arrived: arrived}
	c.mu.Lock()
	c.pending[idKey] = pr
	c.mu.Unlock()

	if err := c.sendMessage(msg); err != nil {
		c.cleanupPending(idKey)
		return nil, NewInternalError(map[string]any{"error": err.Error()})
	}
	return &sentRequest{c: c, idKey: idKey, pr: pr}, nil
}

// wait is the second step of SendRequest: it waits for the response to r, as
// SendRequest describes, and returns its result or error.
func (r *sentRequest) wait(ctx context.Context) (json.RawMessage, error) {
	c, pr, idKey := r.c, r.pr, r.idKey
	donePeerWait := c.startPeerWait(ctx)
	resp, err := c.waitForResponse(ctx, pr, idKey)
	donePeerWait()
	if err != nil {
		return nil, err
	}
	if err := c.waitNotificationsUpTo(ctx, resp.notificationWatermark); err != nil {
		return nil, err
	}

	if resp.msg.Error != nil {
		return nil, resp.msg.Error
	}
	return resp.msg.Result, nil
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
					c.log(slog.LevelDebug, "failed to send $/cancel_request", "err", err)
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
		c.log(slog.LevelDebug, "dropping $/cancel_request due to full queue", "queue_len", maxPendingCancelRequests)
		return
	}

	select {
	case c.cancelRequestSignal <- struct{}{}:
	default:
	}
}

func (c *Connection) waitForResponse(ctx context.Context, pr *pendingResponse, idKey string) (responseEnvelope, error) {
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
			return responseEnvelope{}, peerDisconnectedErr
		default:
		}

		c.sendCancelRequest(idKey)
		c.cleanupPending(idKey)

		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		}
		if cause != nil {
			return responseEnvelope{}, toReqErr(cause)
		}
		return responseEnvelope{}, NewInternalError(map[string]any{"error": "request context ended without cause"})
	case <-c.Done():
		c.cleanupPending(idKey)
		return responseEnvelope{}, peerDisconnectedErr
	}
}

// waitNotificationsUpTo is the wait of SendRequest and SendRequestNoResult
// after the response. For the ctx of a running handler of a request from the
// peer, it also ends while the running notification handler waits on the
// peer, as waitInboundBarrier does before that handler starts. The handler
// can return during the wait. The peer then has its answer, so a wait that
// ended early goes on.
func (c *Connection) waitNotificationsUpTo(ctx context.Context, target uint64) error {
	if c.runningRequestHandler(ctx) {
		err := c.waitNotifications(ctx, target, true)
		if err != nil || c.runningRequestHandler(ctx) {
			return err
		}
	}
	return c.waitNotifications(ctx, target, false)
}

// runningRequestHandler reports whether ctx belongs to the handler of a
// request from the peer on c that has not returned. A ctx that outlives the
// handler, such as one from context.WithoutCancel, keeps the handlingRequest
// value, but the peer no longer waits for that handler's answer.
func (c *Connection) runningRequestHandler(ctx context.Context) bool {
	h, ok := ctx.Value(handlingRequestKey{}).(handlingRequest)
	if !ok || h.conn != c {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, running := c.inflightArrivals[h.arrival]
	return running
}

// waitNotifications waits until the handlers of the notifications up to
// target have returned. With stopAtPeerWait, it also returns while the running
// notification handler waits for a response from the peer: the peer can need
// the answer to a request of its own first, and that request's handler waits
// here, before it starts or in a request that it makes with its ctx.
func (c *Connection) waitNotifications(ctx context.Context, target uint64, stopAtPeerWait bool) error {
	if target == 0 {
		return nil
	}

	peerDisconnectedErr := NewInternalError(map[string]any{"error": "peer disconnected while waiting for pre-response notifications"})
	stopWake := make(chan struct{})
	defer close(stopWake)

	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	if target > c.lastEnqueuedNotificationSeq {
		panic("response watermark exceeded last enqueued notification sequence")
	}
	// While the handler of notification h.seq runs, it can be the caller, on the
	// goroutine that completes h.seq. A wait for h.seq or a later notification
	// would then never end. After h.seq completes, its ctx gets the full wait.
	if h, ok := ctx.Value(handlingNotificationKey{}).(handlingNotification); ok && h.conn == c && c.completedNotificationSeq < h.seq {
		target = min(target, h.seq-1)
	}

	go func() {
		select {
		case <-ctx.Done():
		case <-stopWake:
			return
		}
		c.notifyMu.Lock()
		c.notifyCond.Broadcast()
		c.notifyMu.Unlock()
	}()

	for c.completedNotificationSeq < target && !(stopAtPeerWait && c.peerWaits[c.completedNotificationSeq+1] > 0) {
		if c.completedNotificationSeq > c.lastEnqueuedNotificationSeq {
			panic("completed notification sequence exceeded enqueued notification sequence while waiting")
		}

		select {
		case <-c.Done():
			return peerDisconnectedErr
		default:
		}
		select {
		case <-ctx.Done():
			select {
			case <-c.Done():
				return peerDisconnectedErr
			default:
			}
			cause := context.Cause(ctx)
			if cause == nil {
				cause = ctx.Err()
			}
			if cause != nil {
				return toReqErr(cause)
			}
			return NewInternalError(map[string]any{"error": "request context ended without cause while waiting for notifications"})
		default:
		}

		c.notifyCond.Wait()
	}
	return nil
}

// waitInboundBarrier waits until the handler of a request from the peer with
// watermark can start. A $/cancel_request or a disconnect can end ctx just as
// the barrier clears. It then returns the cancel error, as if ctx had ended the
// wait.
func (c *Connection) waitInboundBarrier(ctx context.Context, watermark uint64) error {
	if err := c.waitNotifications(ctx, watermark, true); err != nil {
		return err
	}
	if cause := context.Cause(ctx); cause != nil {
		return toReqErr(cause)
	}
	return nil
}

// startPeerWait records that a request made with ctx waits for the peer's
// response, if ctx belongs to the running notification handler. The returned
// func ends the record.
func (c *Connection) startPeerWait(ctx context.Context) func() {
	seq, ok := c.notificationSeq(ctx)
	if !ok {
		return func() {}
	}
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	if c.completedNotificationSeq >= seq {
		// The handler has returned. Requests with its ctx get the full wait.
		return func() {}
	}
	c.peerWaits[seq]++
	c.notifyCond.Broadcast()
	return func() {
		c.notifyMu.Lock()
		defer c.notifyMu.Unlock()
		if c.peerWaits[seq] <= 0 {
			panic("peer wait count went below zero")
		}
		if c.peerWaits[seq]--; c.peerWaits[seq] == 0 {
			delete(c.peerWaits, seq)
		}
	}
}

// oldestInflightArrival returns the smallest arrival number of the requests
// from the peer whose handlers have not returned. ok is false when there are
// none.
func (c *Connection) oldestInflightArrival() (oldest uint64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for a := range c.inflightArrivals {
		if !ok || a < oldest {
			oldest, ok = a, true
		}
	}
	return oldest, ok
}

func (c *Connection) waitForNotificationDrain(target uint64, timeout time.Duration) {
	if target == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	stopWake := make(chan struct{})
	defer close(stopWake)

	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	if target > c.lastEnqueuedNotificationSeq {
		panic("drain target exceeded last enqueued notification sequence")
	}

	go func() {
		select {
		case <-ctx.Done():
		case <-stopWake:
			return
		}
		c.notifyMu.Lock()
		c.notifyCond.Broadcast()
		c.notifyMu.Unlock()
	}()

	for c.completedNotificationSeq < target {
		if c.completedNotificationSeq > c.lastEnqueuedNotificationSeq {
			panic("completed notification sequence exceeded enqueued notification sequence during drain")
		}
		if ctx.Err() != nil {
			return
		}
		c.notifyCond.Wait()
	}
}

func (c *Connection) cleanupPending(idKey string) {
	c.mu.Lock()
	delete(c.pending, idKey)
	c.mu.Unlock()
}

// SendRequestNoResult sends a JSON-RPC request that returns no result payload.
// It waits for notification handlers as SendRequest does.
func (c *Connection) SendRequestNoResult(ctx context.Context, method string, params any) error {
	sent, err := c.sendRequest(method, params, nil)
	if err != nil {
		return err
	}
	_, err = sent.wait(ctx)
	return err
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
// (typically when the peer disconnects or the input stream is closed), or when
// the connection closes itself on a limit (see ConnectionOption). A peer that
// stopped reading but keeps its output open and sends nothing never closes
// it, and Done does not end a write that blocks on such a peer: NewConnection
// says how to end that write.
func (c *Connection) Done() <-chan struct{} {
	return c.ctx.Done()
}

// encodableError returns err, the error of the handler of method, when it
// encodes as JSON. Otherwise, as when its Data is not valid JSON, it logs the
// encode error and returns an internal error, so that the peer gets a response
// instead of waiting for one that never comes.
func (c *Connection) encodableError(method string, err *RequestError) *RequestError {
	if _, mErr := json.Marshal(err); mErr != nil {
		c.log(slog.LevelError, "failed to encode the error of a request handler", "method", method, "err", mErr)
		return NewInternalError(map[string]any{"error": mErr.Error()})
	}
	return err
}

// readUnreadableResponse reads line, which failed to decode into an
// anyMessage with err, as a response when it is a JSON object with an id and
// no method, such as one whose error object has a code that is not an
// integer or does not fit an int. It sets msg to that response with a local
// -32603 error that carries err and the error object as it was read, so that
// the request that waits for it fails instead of waiting until its ctx ends,
// and reports true. It reports false for any other line.
func (c *Connection) readUnreadableResponse(line []byte, err error, msg *anyMessage) bool {
	var head struct {
		ID     *json.RawMessage `json:"id"`
		Method json.RawMessage  `json:"method"`
		Error  json.RawMessage  `json:"error"`
	}
	if json.Unmarshal(line, &head) != nil || head.ID == nil || head.Method != nil {
		return false
	}
	c.log(slog.LevelError, "failed to parse incoming response", "err", err, "id", *head.ID)
	data := map[string]any{"error": "invalid response: " + err.Error()}
	if head.Error != nil {
		data["response"] = head.Error
	}
	*msg = anyMessage{JSONRPC: "2.0", ID: head.ID, Error: NewInternalError(data)}
	return true
}
