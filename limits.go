package acp

import (
	"errors"
	"fmt"
	"sync"
)

// ConnectionOption configures the limits of a Connection, an
// AgentSideConnection or a ClientSideConnection on what it holds from the
// peer.
type ConnectionOption func(*connectionLimits)

type connectionLimits struct {
	byteBudget             int
	maxInboundRequests     int
	maxQueuedNotifications int
}

const (
	defaultByteBudget             = 64 << 20
	defaultMaxInboundRequests     = 1024
	defaultMaxQueuedNotifications = 1024
)

// WithByteBudget sets how many bytes of the peer's requests and notifications
// the connection holds at once. A message counts the length of its line. A
// request is held until its reply is written, and a notification until its
// handler returns. A request that does not fit gets a -32603 reply, and its
// handler does not run. A notification that does not fit closes the
// connection. Responses to this side's own requests do not count, nor does the
// line that the connection is reading, which can be up to 10 MiB. The default
// is 64 MiB. WithByteBudget panics if n < 1.
func WithByteBudget(n int) ConnectionOption {
	mustBePositive("WithByteBudget", n)
	return func(l *connectionLimits) { l.byteBudget = n }
}

// WithMaxInboundRequests sets how many requests from the peer the connection
// holds at once, each from when it is read until its reply is written. A
// request over the limit gets a -32603 reply, and its handler does not run.
// The default is 1024. WithMaxInboundRequests panics if n < 1.
func WithMaxInboundRequests(n int) ConnectionOption {
	mustBePositive("WithMaxInboundRequests", n)
	return func(l *connectionLimits) { l.maxInboundRequests = n }
}

// WithMaxQueuedNotifications sets how many notifications from the peer wait
// at once for the notification handler that runs. One more closes the
// connection. The default is 1024. WithMaxQueuedNotifications panics if n < 1.
func WithMaxQueuedNotifications(n int) ConnectionOption {
	mustBePositive("WithMaxQueuedNotifications", n)
	return func(l *connectionLimits) { l.maxQueuedNotifications = n }
}

func mustBePositive(option string, n int) {
	if n < 1 {
		panic(fmt.Sprintf("acp: %s(%d): the limit must be at least 1", option, n))
	}
}

var errByteBudgetExceeded = errors.New("notification exceeds the byte budget")

var (
	errTooManyInboundRequests   = NewInternalError(map[string]any{"error": "too many requests from the peer in flight"})
	errInboundRequestOverBudget = NewInternalError(map[string]any{"error": "request from the peer exceeds the byte budget"})
)

type peerBudget struct {
	maxBytes, maxRequests int
	mu                    sync.Mutex
	bytes                 int
	requests              int
}

func (b *peerBudget) holdRequest(size int) *RequestError {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.requests >= b.maxRequests {
		return errTooManyInboundRequests
	}
	if size > b.maxBytes-b.bytes {
		return errInboundRequestOverBudget
	}
	b.requests++
	b.bytes += size
	return nil
}

func (b *peerBudget) releaseRequest(size int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests--
	b.bytes -= size
}

func (b *peerBudget) holdNotification(size int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if size > b.maxBytes-b.bytes {
		return false
	}
	b.bytes += size
	return true
}

func (b *peerBudget) releaseNotification(size int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bytes -= size
}
