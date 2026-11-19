package acp

import (
	"errors"
	"fmt"
	"sync"
)

// ConnectionOption configures the limits of a Connection, an
// AgentSideConnection or a ClientSideConnection on what it holds from the
// peer. A request over a limit gets a -32603 reply, and its handler does not
// run. The read loop queues that reply for one writer goroutine, so it never
// blocks on writing a rejection, even to a peer that is itself blocked writing
// to this side. When 1024 rejections wait to be written, or they do not fit
// the byte budget, the next one closes the connection: the peer does not read
// its replies.
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

// WithByteBudget sets how many bytes the connection holds at once of each of
// three kinds: the peer's requests, its notifications, and the rejections that
// wait to be written to it. Each kind has the whole budget, so the connection
// can hold three times n, and requests that fill their part never stop a
// notification such as session/cancel. A message counts the length of its
// line, and a request also the length of its id, which the connection keeps a
// second time to find the request by id. A request is held until its reply is
// written, a notification until its handler returns, and a rejection until it
// is written. A request that does not fit gets a -32603 reply. A notification
// that does not fit closes the connection, and so does a rejection, unless no
// other rejection waits. Responses to this side's own requests do not count,
// nor does the line that the connection is reading, which can be up to 10 MiB.
// The default is 64 MiB. WithByteBudget panics if n < 1.
func WithByteBudget(n int) ConnectionOption {
	mustBePositive("WithByteBudget", n)
	return func(l *connectionLimits) { l.byteBudget = n }
}

// WithMaxInboundRequests sets how many requests from the peer the connection
// holds at once, each from when it is read until its reply is written. A
// request over the limit gets a -32603 reply (see ConnectionOption). The
// default is 1024. WithMaxInboundRequests panics if n < 1.
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

var (
	errByteBudgetExceeded     = errors.New("notification exceeds the byte budget")
	errRejectionQueueOverflow = errors.New("rejection queue overflow")
)

var (
	errTooManyInboundRequests   = NewInternalError(map[string]any{"error": "too many requests from the peer in flight"})
	errInboundRequestOverBudget = NewInternalError(map[string]any{"error": "request from the peer exceeds the byte budget"})
)

// maxQueuedRejections bounds the rejections that wait for writeRejections.
const maxQueuedRejections = 1024

// peerBudget counts what a Connection holds from the peer. Requests,
// notifications and queued rejections each have the whole byte budget, so
// requests that fill it never close the connection on the next notification.
type peerBudget struct {
	maxBytes, maxRequests int
	mu                    sync.Mutex
	requests              int
	requestBytes          int
	notificationBytes     int
	rejectionBytes        int
}

func (b *peerBudget) holdRequest(size int) *RequestError {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.requests >= b.maxRequests {
		return errTooManyInboundRequests
	}
	if size > b.maxBytes-b.requestBytes {
		return errInboundRequestOverBudget
	}
	b.requests++
	b.requestBytes += size
	return nil
}

func (b *peerBudget) releaseRequest(size int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests--
	b.requestBytes -= size
}

func (b *peerBudget) holdNotification(size int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if size > b.maxBytes-b.notificationBytes {
		return false
	}
	b.notificationBytes += size
	return true
}

func (b *peerBudget) releaseNotification(size int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notificationBytes -= size
}

// holdRejection always holds a rejection when none waits, so that a budget
// smaller than a rejection still answers one request at a time.
func (b *peerBudget) holdRejection(size int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rejectionBytes > 0 && size > b.maxBytes-b.rejectionBytes {
		return false
	}
	b.rejectionBytes += size
	return true
}

func (b *peerBudget) releaseRejection(size int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rejectionBytes -= size
}
