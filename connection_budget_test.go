package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func (b *peerBudget) held() (bytes, requests int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requestBytes + b.notificationBytes, b.requests
}

func liveHeap() int64 {
	// The second GC frees what sync.Pool keeps for one cycle, such as the
	// buffers that encoding/json reuses.
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.HeapAlloc)
}

func retained(t *testing.T, c *Connection, before int64) (delta int64, heldBytes, heldRequests int) {
	t.Helper()
	delta = liveHeap() - before
	heldBytes, heldRequests = c.budget.held()
	t.Logf("heap delta %d bytes, held %d bytes and %d requests", delta, heldBytes, heldRequests)
	return delta, heldBytes, heldRequests
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the connection did not close")
	}
}

func isOpen(done <-chan struct{}) bool {
	select {
	case <-done:
		return false
	default:
		return true
	}
}

const lineNumberDigits = 8

type wireLine struct {
	buf  []byte
	nums []int
}

func newWireLine(t *testing.T, template string, size int) *wireLine {
	t.Helper()
	fixed := len(template) - len("{pad}") + strings.Count(template, "{n}")*(lineNumberDigits-len("{n}"))
	pad := size - fixed
	if pad < 0 || strings.Count(template, "{pad}") != 1 {
		t.Fatalf("SETUP: %q makes no line of %d bytes", template, size)
	}
	l := &wireLine{buf: make([]byte, 0, size+1)}
	for rest := template; rest != ""; {
		switch {
		case strings.HasPrefix(rest, "{n}"):
			l.nums = append(l.nums, len(l.buf))
			l.buf = append(l.buf, strings.Repeat("0", lineNumberDigits)...)
			rest = rest[len("{n}"):]
		case strings.HasPrefix(rest, "{pad}"):
			l.buf = append(l.buf, bytes.Repeat([]byte{'x'}, pad)...)
			rest = rest[len("{pad}"):]
		default:
			l.buf = append(l.buf, rest[0])
			rest = rest[1:]
		}
	}
	l.buf = append(l.buf, '\n')
	return l
}

func requestLine(t *testing.T, method string, size int) *wireLine {
	return newWireLine(t, `{"jsonrpc":"2.0","id":"{n}","method":"`+method+`","params":{"pad":"{pad}"}}`, size)
}

func notificationLine(t *testing.T, method string, size int) *wireLine {
	return newWireLine(t, `{"jsonrpc":"2.0","method":"`+method+`","params":{"n":"{n}","pad":"{pad}"}}`, size)
}

func (l *wireLine) number(n int) []byte {
	digits := fmt.Sprintf("%0*d", lineNumberDigits, n)
	for _, at := range l.nums {
		copy(l.buf[at:at+lineNumberDigits], digits)
	}
	return l.buf
}

func (l *wireLine) size() int { return len(l.buf) - 1 }

// charge is what a line from requestLine holds of the byte budget: the line,
// and the key of its id, which the connection keeps a second time.
func (l *wireLine) charge() int { return l.size() + len(`"`+lineID(0)+`"`) }

func lineID(n int) string { return fmt.Sprintf("%0*d", lineNumberDigits, n) }

type budgetPeer struct {
	t          *testing.T
	inR        *io.PipeReader
	inW        *io.PipeWriter
	outR       *io.PipeReader
	peerInput  io.Writer
	peerOutput io.Reader
	replies    chan anyMessage
	logs       lockedBuffer
	logger     *slog.Logger
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newBudgetPeer(t *testing.T) *budgetPeer {
	t.Helper()
	p := newSilentPeer(t)
	go func() {
		s := bufio.NewScanner(p.outR)
		s.Buffer(nil, 16<<20)
		for s.Scan() {
			var m anyMessage
			if json.Unmarshal(s.Bytes(), &m) == nil {
				p.replies <- m
			}
		}
	}()
	return p
}

// newSilentPeer makes a peer that reads nothing that the connection writes.
func newSilentPeer(t *testing.T) *budgetPeer {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &budgetPeer{t: t, inR: inR, inW: inW, outR: outR, peerInput: outW, peerOutput: inR, replies: make(chan anyMessage, 4096)}
	p.logger = slog.New(slog.NewTextHandler(&p.logs, nil))
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outW.Close()
	})
	return p
}

func (p *budgetPeer) write(b []byte) {
	p.t.Helper()
	if _, err := p.inW.Write(b); err != nil {
		p.t.Fatalf("write: %v", err)
	}
}

func (p *budgetPeer) writeAll(line *wireLine, first, last int) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := first; n <= last; n++ {
			if _, err := p.inW.Write(line.number(n)); err != nil {
				return
			}
		}
	}()
	return done
}

// writeLines writes lines in the background, so that a test that fails while
// the connection reads nothing does not hang on the write.
func (p *budgetPeer) writeLines(lines ...[]byte) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, line := range lines {
			if _, err := p.inW.Write(line); err != nil {
				return
			}
		}
	}()
	return done
}

func (p *budgetPeer) cutOff(writes <-chan struct{}) {
	p.t.Helper()
	_ = p.inR.CloseWithError(io.ErrClosedPipe)
	select {
	case <-writes:
	case <-time.After(10 * time.Second):
		p.t.Fatal("the writes did not end")
	}
}

func (p *budgetPeer) reply() anyMessage {
	p.t.Helper()
	select {
	case m := <-p.replies:
		return m
	case <-time.After(10 * time.Second):
		p.t.Fatal("no reply from the connection")
		return anyMessage{}
	}
}

func (p *budgetPeer) noReply() {
	p.t.Helper()
	select {
	case m := <-p.replies:
		p.t.Fatalf("unexpected reply %s", replyID(p.t, m))
	default:
	}
}

func replyID(t *testing.T, m anyMessage) string {
	t.Helper()
	if m.ID == nil {
		t.Fatal("reply without id")
	}
	var id string
	if err := json.Unmarshal(*m.ID, &id); err != nil {
		t.Fatalf("reply id %s: %v", *m.ID, err)
	}
	return id
}

type gate struct {
	method  string
	entered atomic.Int64
	release chan struct{}
	once    sync.Once
}

func newGate(t *testing.T, method string) *gate {
	g := &gate{method: method, release: make(chan struct{})}
	t.Cleanup(g.open)
	return g
}

func (g *gate) open() { g.once.Do(func() { close(g.release) }) }

func (g *gate) handler(ctx context.Context, method string, _ json.RawMessage) (any, *RequestError) {
	if method == g.method {
		g.entered.Add(1)
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	}
	return map[string]any{}, nil
}

func newBudgetConnection(t *testing.T, p *budgetPeer, handler MethodHandler, opts ...ConnectionOption) *Connection {
	t.Helper()
	c := NewConnection(handler, p.peerInput, p.peerOutput, opts...)
	c.SetLogger(p.logger)
	return c
}

func warmUp(t *testing.T, p *budgetPeer) {
	t.Helper()
	p.write([]byte(`{"jsonrpc":"2.0","id":"warm-up","method":"_ok"}` + "\n"))
	if m := p.reply(); replyID(t, m) != "warm-up" || m.Error != nil {
		t.Fatalf("warm-up reply: %+v", m)
	}
}

func waitHeld(t *testing.T, c *Connection, wantBytes, wantRequests int) {
	t.Helper()
	waitUntil(t, fmt.Sprintf("the connection holds %d bytes and %d requests, and no rejection waits", wantBytes, wantRequests), func() bool {
		b, r := c.budget.held()
		c.budget.mu.Lock()
		rejections := c.budget.rejectionBytes
		c.budget.mu.Unlock()
		return b == wantBytes && r == wantRequests && rejections == 0
	})
}

func rejectedIDs(t *testing.T, p *budgetPeer, n int) map[string]bool {
	t.Helper()
	ids := make(map[string]bool)
	for i := 0; i < n; i++ {
		m := p.reply()
		if m.Error == nil || m.Error.Code != -32603 {
			t.Fatalf("reply %s: error %+v, want code -32603", replyID(t, m), m.Error)
		}
		ids[replyID(t, m)] = true
	}
	return ids
}

func TestConnectionOption_PanicsBelowOne(t *testing.T) {
	options := map[string]func(int) ConnectionOption{
		"WithByteBudget":             WithByteBudget,
		"WithMaxInboundRequests":     WithMaxInboundRequests,
		"WithMaxQueuedNotifications": WithMaxQueuedNotifications,
	}
	for name, option := range options {
		for _, n := range []int{0, -1} {
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("%s(%d) did not panic", name, n)
					}
				}()
				option(n)
			}()
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s(1) panicked: %v", name, r)
				}
			}()
			option(1)
		}()
	}
}

func TestConnectionBudget_RejectsRequestsOverLimit(t *testing.T) {
	const maxRequests, sent = 4, 6
	p := newBudgetPeer(t)
	g := newGate(t, "_block")
	c := newBudgetConnection(t, p, g.handler, WithMaxInboundRequests(maxRequests))
	warmUp(t, p)
	waitHeld(t, c, 0, 0)

	line := requestLine(t, "_block", 128)
	goroutines := runtime.NumGoroutine()
	before := liveHeap()
	for n := 1; n <= sent; n++ {
		p.write(line.number(n))
	}
	rejected := rejectedIDs(t, p, sent-maxRequests)
	waitUntil(t, "the held requests run", func() bool { return g.entered.Load() == maxRequests })
	grown := runtime.NumGoroutine() - goroutines
	t.Logf("goroutines +%d", grown)
	_, heldBytes, heldRequests := retained(t, c, before)

	for n := maxRequests + 1; n <= sent; n++ {
		if !rejected[lineID(n)] {
			t.Errorf("request %d over the cap got no -32603; rejected %v", n, rejected)
		}
	}
	p.noReply()
	if heldRequests != maxRequests || heldBytes != maxRequests*line.charge() {
		t.Errorf("held %d bytes and %d requests, want %d and %d", heldBytes, heldRequests, maxRequests*line.charge(), maxRequests)
	}
	if grown > maxRequests {
		t.Errorf("goroutines grew by %d for %d held requests", grown, maxRequests)
	}

	g.open()
	for i := 0; i < maxRequests; i++ {
		if m := p.reply(); m.Error != nil {
			t.Errorf("held request %s: %+v", replyID(t, m), m.Error)
		}
	}
	waitHeld(t, c, 0, 0)
	if got := g.entered.Load(); got != maxRequests {
		t.Errorf("the handler ran %d times, want %d", got, maxRequests)
	}
	p.write(line.number(sent + 1))
	if m := p.reply(); replyID(t, m) != lineID(sent+1) || m.Error != nil {
		t.Errorf("request after the burst: %+v", m)
	}
}

func TestConnectionBudget_RejectsRequestsOverByteBudget(t *testing.T) {
	const budget, sent = 8 << 20, 32
	p := newBudgetPeer(t)
	g := newGate(t, "_block")
	c := newBudgetConnection(t, p, g.handler, WithByteBudget(budget))
	warmUp(t, p)

	line := requestLine(t, "_block", 1<<20)
	fit := budget / line.charge()
	before := liveHeap()
	for n := 1; n <= sent; n++ {
		p.write(line.number(n))
	}
	rejected := rejectedIDs(t, p, sent-fit)
	waitUntil(t, "the held requests run", func() bool { return g.entered.Load() == int64(fit) })
	delta, heldBytes, heldRequests := retained(t, c, before)

	for n := fit + 1; n <= sent; n++ {
		if !rejected[lineID(n)] {
			t.Errorf("request %d over the budget got no -32603", n)
		}
	}
	if heldBytes > budget || heldRequests != fit {
		t.Errorf("held %d bytes and %d requests, want at most %d bytes and %d requests", heldBytes, heldRequests, budget, fit)
	}
	if delta >= 3*budget {
		t.Errorf("heap grew by %d bytes, want less than %d", delta, 3*budget)
	}

	g.open()
	for i := 0; i < fit; i++ {
		if m := p.reply(); m.Error != nil {
			t.Errorf("held request %s: %+v", replyID(t, m), m.Error)
		}
	}
	waitHeld(t, c, 0, 0)
}

func TestConnectionBudget_ClosesOnNotificationOverByteBudget(t *testing.T) {
	const budget, sent = 8 << 20, 32
	p := newBudgetPeer(t)
	g := newGate(t, "_block")
	c := newBudgetConnection(t, p, g.handler, WithByteBudget(budget))
	warmUp(t, p)

	line := notificationLine(t, "_block", 1<<20)
	before := liveHeap()
	writes := p.writeAll(line, 1, sent)
	waitDone(t, c.Done())
	p.cutOff(writes)
	delta, heldBytes, _ := retained(t, c, before)

	if !strings.Contains(p.logs.String(), "byte budget") {
		t.Errorf("the log does not name the byte budget:\n%s", p.logs.String())
	}
	if fit := budget / line.size(); heldBytes != fit*line.size() {
		t.Errorf("held %d bytes, want the %d notifications that fit %d bytes", heldBytes, fit, budget)
	}
	if delta >= 3*budget {
		t.Errorf("heap grew by %d bytes, want less than %d", delta, 3*budget)
	}

	g.open()
	waitHeld(t, c, 0, 0)
}

func TestConnectionBudget_KeepsRequestsAndNotificationsApart(t *testing.T) {
	const budget, requests, notes = 1 << 20, 3, 5
	request := requestLine(t, "_block", 300<<10)
	note := notificationLine(t, "_block", 200<<10)
	if requests*request.charge() > budget || (requests+1)*request.charge() <= budget ||
		notes*note.size() > budget || (notes+1)*note.size() <= budget {
		t.Fatal("SETUP: the requests and the notifications must each fill the budget")
	}
	p := newBudgetPeer(t)
	g := newGate(t, "_block")
	c := newBudgetConnection(t, p, g.handler, WithByteBudget(budget))

	before := liveHeap()
	for n := 1; n <= requests+1; n++ {
		p.write(request.number(n))
	}
	if rejected := rejectedIDs(t, p, 1); !rejected[lineID(requests+1)] {
		t.Errorf("rejected %v, want request %d", rejected, requests+1)
	}
	writes := p.writeAll(note, 1, notes)
	want := requests*request.charge() + notes*note.size()
	waitUntil(t, "the notifications are held or close the connection", func() bool {
		b, _ := c.budget.held()
		return b == want || !isOpen(c.Done())
	})
	if !isOpen(c.Done()) {
		t.Fatal("notifications that fit the budget closed the connection beside requests that fill it")
	}
	<-writes

	writes = p.writeAll(note, notes+1, notes+1)
	waitDone(t, c.Done())
	p.cutOff(writes)
	retained(t, c, before)
	if !strings.Contains(p.logs.String(), "byte budget") {
		t.Errorf("the log does not name the byte budget:\n%s", p.logs.String())
	}
	g.open()
	waitHeld(t, c, 0, 0)
}

func TestConnectionBudget_DefaultLimits(t *testing.T) {
	const documentedRequests, documentedBudget = 1024, 64 << 20

	t.Run("requests", func(t *testing.T) {
		p := newBudgetPeer(t)
		g := newGate(t, "_block")
		c := newBudgetConnection(t, p, g.handler)
		line := requestLine(t, "_block", 128)
		before := liveHeap()
		for n := 1; n <= documentedRequests+1; n++ {
			p.write(line.number(n))
		}
		if rejected := rejectedIDs(t, p, 1); !rejected[lineID(documentedRequests+1)] {
			t.Errorf("rejected %v, want request %d", rejected, documentedRequests+1)
		}
		waitHeld(t, c, documentedRequests*line.charge(), documentedRequests)
		p.noReply()
		retained(t, c, before)
		g.open()
		waitHeld(t, c, 0, 0)
	})

	t.Run("notifications", func(t *testing.T) {
		p := newBudgetPeer(t)
		g := newGate(t, "_block")
		c := newBudgetConnection(t, p, g.handler)
		line := notificationLine(t, "_block", 1<<20)
		if fit := documentedBudget / line.size(); fit < 60 || fit >= 65 {
			t.Fatalf("SETUP: %d notifications fit, want 60 to 64", fit)
		}
		before := liveHeap()
		writes := p.writeAll(line, 1, 60)
		<-writes
		waitHeld(t, c, 60*line.size(), 0)
		retained(t, c, before)
		if !isOpen(c.Done()) {
			t.Fatal("60 notifications of 1 MiB closed the connection")
		}
		writes = p.writeAll(line, 61, 65)
		waitDone(t, c.Done())
		p.cutOff(writes)
		retained(t, c, before)
		g.open()
		waitHeld(t, c, 0, 0)
	})
}

func TestConnectionBudget_MaxQueuedNotifications(t *testing.T) {
	const burst = 2000
	tests := []struct {
		name     string
		opts     []ConnectionOption
		wantOpen bool
	}{
		{name: "default", wantOpen: false},
		{name: "4096", opts: []ConnectionOption{WithMaxQueuedNotifications(4096)}, wantOpen: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			p := newBudgetPeer(t)
			g := newGate(t, "_block")
			c := newBudgetConnection(t, p, g.handler, tt.opts...)
			line := notificationLine(t, "_block", 128)
			before := liveHeap()
			writes := p.writeAll(line, 1, burst)
			if tt.wantOpen {
				<-writes
				waitHeld(t, c, burst*line.size(), 0)
				if !isOpen(c.Done()) {
					t.Fatal("the burst closed the connection")
				}
			} else {
				waitDone(t, c.Done())
				p.cutOff(writes)
				if !strings.Contains(p.logs.String(), "failed to queue notification") {
					t.Errorf("the log does not name the full queue:\n%s", p.logs.String())
				}
			}
			retained(t, c, before)
			g.open()
			waitHeld(t, c, 0, 0)
		})
	}
}

func TestConnectionBudget_OptionsReachSideConnections(t *testing.T) {
	t.Run("agent", func(t *testing.T) {
		p := newBudgetPeer(t)
		var called atomic.Bool
		conn := NewAgentSideConnection(agentFuncs{InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
			called.Store(true)
			return InitializeResponse{}, nil
		}}, p.peerInput, p.peerOutput, WithByteBudget(16))
		conn.SetLogger(p.logger)
		p.write([]byte(`{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":1}}` + "\n"))
		if rejected := rejectedIDs(t, p, 1); !rejected["a"] || called.Load() {
			t.Errorf("rejected %v, Initialize called = %v; want a rejected, not called", rejected, called.Load())
		}
	})
	t.Run("client", func(t *testing.T) {
		p := newBudgetPeer(t)
		var called atomic.Bool
		conn := NewClientSideConnection(&clientFuncs{RequestPermissionFunc: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
			called.Store(true)
			return RequestPermissionResponse{}, nil
		}}, p.peerInput, p.peerOutput, WithByteBudget(16))
		conn.SetLogger(p.logger)
		p.write([]byte(`{"jsonrpc":"2.0","id":"c","method":"session/request_permission","params":{}}` + "\n"))
		if rejected := rejectedIDs(t, p, 1); !rejected["c"] || called.Load() {
			t.Errorf("rejected %v, RequestPermission called = %v; want c rejected, not called", rejected, called.Load())
		}
	})
}

func TestConnectionBudget_ReleasesOnEveryPath(t *testing.T) {
	t.Run("request cancelled at the inbound barrier", func(t *testing.T) {
		p := newBudgetPeer(t)
		g := newGate(t, "_block")
		c := newBudgetConnection(t, p, g.handler)
		note := notificationLine(t, "_block", 256)
		request := requestLine(t, "_after", 256)
		p.write(note.number(1))
		p.write(request.number(1))
		waitHeld(t, c, note.size()+request.charge(), 1)
		p.write([]byte(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":"` + lineID(1) + `"}}` + "\n"))
		if m := p.reply(); replyID(t, m) != lineID(1) || m.Error == nil || m.Error.Code != -32800 {
			t.Fatalf("cancelled request: %+v", m)
		}
		waitHeld(t, c, note.size(), 0)
		g.open()
		waitHeld(t, c, 0, 0)
	})

	t.Run("request when the peer disconnects", func(t *testing.T) {
		p := newBudgetPeer(t)
		g := newGate(t, "_block")
		c := newBudgetConnection(t, p, g.handler)
		request := requestLine(t, "_block", 256)
		p.write(request.number(1))
		waitUntil(t, "the request runs", func() bool { return g.entered.Load() == 1 })
		_ = p.inW.Close()
		waitDone(t, c.Done())
		waitHeld(t, c, 0, 0)
	})

	t.Run("queued notifications after a close", func(t *testing.T) {
		const queued = 5
		p := newBudgetPeer(t)
		g := newGate(t, "_block")
		c := newBudgetConnection(t, p, g.handler)
		note := notificationLine(t, "_block", 256)
		for n := 1; n <= queued; n++ {
			p.write(note.number(n))
		}
		waitHeld(t, c, queued*note.size(), 0)
		_ = p.inW.Close()
		waitDone(t, c.Done())
		if heldBytes, _ := c.budget.held(); heldBytes != queued*note.size() {
			t.Errorf("held %d bytes after the close, want the %d queued notifications", heldBytes, queued)
		}
		g.open()
		waitHeld(t, c, 0, 0)
	})
}

func TestAgentSideConnection_CancelMarkersHoldNoSessionIDs(t *testing.T) {
	const cancels, prompts, idSize = 1100, 200, 32 << 10
	p := newBudgetPeer(t)
	var oldRunning atomic.Bool
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	conn := NewAgentSideConnection(agentFuncs{PromptFunc: func(ctx context.Context, req PromptRequest) (PromptResponse, error) {
		if req.SessionId == "old" {
			oldRunning.Store(true)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return PromptResponse{StopReason: StopReasonEndTurn}, nil
	}}, p.peerInput, p.peerOutput)
	conn.SetLogger(p.logger)

	pinsMarkers := []byte(`{"jsonrpc":"2.0","id":"old","method":"session/prompt","params":{"sessionId":"old","prompt":[]}}` + "\n")
	p.write(pinsMarkers)
	waitUntil(t, "the old prompt runs", oldRunning.Load)

	cancel := newWireLine(t, `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"{n}{pad}"}}`, idSize+64)
	prompt := newWireLine(t, `{"jsonrpc":"2.0","id":"{n}","method":"session/prompt","params":{"sessionId":"{n}{pad}","prompt":[]}}`, idSize+128)
	before := liveHeap()
	for n := 1; n <= cancels; n++ {
		p.write(cancel.number(n))
	}
	for n := 1; n <= prompts; n++ {
		p.write(prompt.number(n))
	}
	for n := 1; n <= prompts; n++ {
		if m := p.reply(); m.Error != nil {
			t.Fatalf("prompt %s: %+v", replyID(t, m), m.Error)
		}
	}
	waitCompletedNotifications(t, conn.conn, cancels)
	waitHeld(t, conn.conn, len(pinsMarkers)-1+len(`"old"`), 1)
	delta, _, _ := retained(t, conn.conn, before)
	conn.mu.Lock()
	cancelledAt, newest := len(conn.sessionCancelledAt), len(conn.sessionNewest)
	conn.mu.Unlock()
	t.Logf("%d cancel markers and %d newest-prompt markers", cancelledAt, newest)

	if cancelledAt != maxCancelMarkers || newest != prompts {
		t.Fatalf("SETUP: %d cancel markers and %d newest-prompt markers, want %d and %d", cancelledAt, newest, maxCancelMarkers, prompts)
	}
	if delta >= 4<<20 {
		t.Errorf("heap grew by %d bytes, want less than 4 MiB", delta)
	}
}

func TestAgentSideConnection_CancelMarkersKeepLongSessionIDsApart(t *testing.T) {
	long := strings.Repeat("x", 32<<10)
	cancelLine := func(sessionID string) string {
		return `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"` + sessionID + `"}}`
	}
	promptLine := func(id, sessionID string) string {
		return `{"jsonrpc":"2.0","id":"` + id + `","method":"session/prompt","params":{"sessionId":"` + sessionID + `","prompt":[]}}`
	}
	tests := []struct {
		name          string
		cancelled     string
		wantCancelled bool
	}{
		{name: "cancel of the other session", cancelled: long + "b", wantCancelled: false},
		{name: "own cancel", cancelled: long + "a", wantCancelled: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			p := newBudgetPeer(t)
			started := make(chan context.Context, 1)
			var conn *AgentSideConnection
			conn = NewAgentSideConnection(agentFuncs{
				CancelFunc: func(_ context.Context, n CancelNotification) error {
					if n.SessionId == "held" {
						waitEnqueuedNotifications(t, conn.conn, 2)
					}
					return nil
				},
				PromptFunc: func(ctx context.Context, _ PromptRequest) (PromptResponse, error) {
					started <- ctx
					<-ctx.Done()
					return PromptResponse{StopReason: StopReasonCancelled}, nil
				},
			}, p.peerInput, p.peerOutput)
			conn.SetLogger(p.logger)

			p.write([]byte(cancelLine("held") + "\n" + promptLine("a", long+"a") + "\n" + cancelLine(tt.cancelled) + "\n"))
			var promptCtx context.Context
			select {
			case promptCtx = <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("the prompt did not run")
			}
			waitCompletedNotifications(t, conn.conn, 2)
			if tt.wantCancelled {
				waitUntil(t, "the prompt is cancelled", func() bool { return promptCtx.Err() != nil })
				return
			}
			if promptCtx.Err() != nil {
				t.Fatal("the cancel of the other session cancelled the prompt")
			}
			p.write([]byte(cancelLine(long+"a") + "\n"))
			waitUntil(t, "the prompt is cancelled by its own cancel", func() bool { return promptCtx.Err() != nil })
		})
	}
}

// Prompts with images or resources can fill the byte budget, and a client must
// still be able to cancel them.
func TestAgentSideConnection_CancelFitsBesideHeldPrompts(t *testing.T) {
	const prompts, budget = 8, 64 << 20
	// Each prompt line is 8 MiB less the key of its id, so the eight fill the
	// documented budget exactly.
	promptSize := budget/prompts - len(`"`+lineID(0)+`"`)
	p := newBudgetPeer(t)
	g := newGate(t, "")
	var running atomic.Int64
	conn := NewAgentSideConnection(agentFuncs{
		PromptFunc: func(ctx context.Context, _ PromptRequest) (PromptResponse, error) {
			running.Add(1)
			select {
			case <-g.release:
				return PromptResponse{StopReason: StopReasonEndTurn}, nil
			case <-ctx.Done():
				return PromptResponse{StopReason: StopReasonCancelled}, nil
			}
		},
		CancelFunc: func(context.Context, CancelNotification) error { return nil },
	}, p.peerInput, p.peerOutput)
	conn.SetLogger(p.logger)

	prompt := newWireLine(t, `{"jsonrpc":"2.0","id":"{n}","method":"session/prompt","params":{"sessionId":"{n}","prompt":[{"type":"text","text":"{pad}"}]}}`, promptSize)
	for n := 1; n <= prompts; n++ {
		p.write(prompt.number(n))
	}
	waitUntil(t, "every prompt runs", func() bool { return running.Load() == prompts })
	if heldBytes, _ := conn.conn.budget.held(); heldBytes != budget {
		t.Fatalf("SETUP: the prompts hold %d bytes, want the whole budget of %d", heldBytes, budget)
	}
	cancel := []byte(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"` + lineID(1) + `"}}` + "\n")
	p.write(cancel)

	m := p.reply()
	if !isOpen(conn.Done()) {
		t.Fatalf("a %d-byte session/cancel closed the connection beside %d held prompts of %d bytes, which cancels every prompt", len(cancel)-1, prompts, promptSize)
	}
	if replyID(t, m) != lineID(1) || m.Error != nil || !strings.Contains(string(m.Result), `"cancelled"`) {
		t.Fatalf("reply %s: result %s, error %+v; want prompt 1 cancelled", replyID(t, m), m.Result, m.Error)
	}
	p.noReply()

	g.open()
	for n := 2; n <= prompts; n++ {
		if m := p.reply(); m.Error != nil || !strings.Contains(string(m.Result), `"end_turn"`) {
			t.Errorf("reply %s: result %s, error %+v; want end_turn", replyID(t, m), m.Result, m.Error)
		}
	}
	waitHeld(t, conn.conn, 0, 0)
}

// Two SDK peers that each send the other more requests than it holds must
// both get every answer: neither read loop may wait for the other to read a
// rejection.
func TestConnection_PeersThatFloodEachOtherGetEveryAnswer(t *testing.T) {
	const calls, held = 1500, 1024
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	aIn, bOut := io.Pipe()
	bIn, aOut := io.Pipe()
	t.Cleanup(func() {
		for _, c := range []io.Closer{aIn, bOut, bIn, aOut} {
			_ = c.Close()
		}
	})

	type side struct {
		gate                 *gate
		conn                 *Connection
		ok, rejected, failed atomic.Int64
		returned             sync.WaitGroup
	}
	a := &side{gate: newGate(t, "_block")}
	b := &side{gate: newGate(t, "_block")}
	a.conn = NewConnection(a.gate.handler, bOut, aIn)
	b.conn = NewConnection(b.gate.handler, aOut, bIn)
	a.conn.SetLogger(discard)
	b.conn.SetLogger(discard)

	for _, s := range []*side{a, b} {
		for i := 0; i < calls; i++ {
			s.returned.Add(1)
			go func(s *side) {
				defer s.returned.Done()
				_, err := SendRequest[json.RawMessage](s.conn, context.Background(), "_block", nil)
				var re *RequestError
				switch {
				case err == nil:
					s.ok.Add(1)
				case errors.As(err, &re) && re.Code == -32603 && bytes.Contains(re.Data, []byte("too many requests")):
					s.rejected.Add(1)
				default:
					s.failed.Add(1)
				}
			}(s)
		}
	}
	counts := func() string {
		return fmt.Sprintf("a: %d held by b, %d rejected, %d failed; b: %d held by a, %d rejected, %d failed",
			b.gate.entered.Load(), a.rejected.Load(), a.failed.Load(), a.gate.entered.Load(), b.rejected.Load(), b.failed.Load())
	}
	deadline := time.Now().Add(10 * time.Second)
	for a.gate.entered.Load() != held || b.gate.entered.Load() != held || a.rejected.Load() != calls-held || b.rejected.Load() != calls-held {
		if time.Now().After(deadline) {
			t.Fatalf("after 10s each side should hold %d requests and reject %d: %s", held, calls-held, counts())
		}
		time.Sleep(time.Millisecond)
	}

	a.gate.open()
	b.gate.open()
	returned := make(chan struct{})
	go func() {
		a.returned.Wait()
		b.returned.Wait()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatalf("calls still waiting 10s after the handlers returned: %s", counts())
	}
	for name, s := range map[string]*side{"a": a, "b": b} {
		if s.ok.Load() != held || s.rejected.Load() != calls-held || s.failed.Load() != 0 {
			t.Errorf("%s: %d answered, %d rejected, %d failed; want %d, %d and 0", name, s.ok.Load(), s.rejected.Load(), s.failed.Load(), held, calls-held)
		}
	}
}

// A peer that sends requests over the cap and reads none of the rejections
// fills the rejection queue, by count or by bytes, and then the connection
// closes. Until then the read loop reads on.
func TestConnectionBudget_ClosesWhenThePeerReadsNoRejections(t *testing.T) {
	const budget = 1 << 20
	small := requestLine(t, "_block", 128)
	hugeID := newWireLine(t, `{"jsonrpc":"2.0","id":"{n}{pad}","method":"_block","params":{}}`, 300<<10)
	tests := []struct {
		name      string
		rejected  *wireLine
		fit, more int
	}{
		{name: "count", rejected: small, fit: 10, more: maxQueuedRejections},
		{name: "bytes", rejected: hugeID, fit: budget / hugeID.size(), more: 1},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			p := newSilentPeer(t)
			g := newGate(t, "_block")
			var seen atomic.Int64
			c := newBudgetConnection(t, p, func(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
				if method == "_seen" {
					seen.Add(1)
				}
				return g.handler(ctx, method, params)
			}, WithMaxInboundRequests(1), WithByteBudget(budget))
			seenNote := notificationLine(t, "_seen", 128)

			p.write(small.number(1))
			waitUntil(t, "the held request runs", func() bool { return g.entered.Load() == 1 })
			var lines [][]byte
			for n := 2; n <= 1+tt.fit; n++ {
				lines = append(lines, bytes.Clone(tt.rejected.number(n)))
			}
			writes := p.writeLines(append(lines, seenNote.number(1))...)
			waitUntil(t, "the read loop reads past the waiting rejections", func() bool { return seen.Load() == 1 })
			if !isOpen(c.Done()) {
				t.Fatalf("%d rejections that wait closed the connection", tt.fit)
			}
			<-writes

			writes = p.writeAll(tt.rejected, 2+tt.fit, 1+tt.fit+tt.more)
			waitDone(t, c.Done())
			p.cutOff(writes)
			if !strings.Contains(p.logs.String(), "does not read the rejections") {
				t.Errorf("the log does not name the rejections:\n%s", p.logs.String())
			}
			_ = p.outR.CloseWithError(io.ErrClosedPipe)
			waitHeld(t, c, 0, 0)
		})
	}
}

// The byte budget bounds what requests retain even when their ids are huge:
// the connection keeps each request's id twice, in the message for the reply
// and as the key that $/cancel_request looks up.
func TestConnectionBudget_CountsWhatHugeIDsRetain(t *testing.T) {
	const requests, idSize, budget, slack = 6, 9 << 20, 64 << 20, 4 << 20
	tests := []struct {
		name string
		id   func(n int) string
	}{
		{name: "string", id: func(n int) string { return `"` + lineID(n) + strings.Repeat("x", idSize) + `"` }},
		{name: "number", id: func(n int) string { return "1" + lineID(n) + strings.Repeat("0", idSize) }},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			p := newBudgetPeer(t)
			g := newGate(t, "_block")
			c := newBudgetConnection(t, p, g.handler)
			c.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
			line := func(method string, n int) []byte {
				return []byte(`{"jsonrpc":"2.0","id":` + tt.id(n) + `,"method":"` + method + `","params":{}}` + "\n")
			}
			// The first reply grows the read buffers on both ends to the size of
			// these lines before the baseline.
			p.write(line("_ok", 0))
			if m := p.reply(); m.Error != nil {
				t.Fatalf("warm-up reply: %+v", m.Error)
			}

			before := liveHeap()
			for n := 1; n <= requests; n++ {
				p.write(line("_block", n))
			}
			rejected := 0
			waitUntil(t, "every request runs or gets a -32603", func() bool {
				for {
					select {
					case m := <-p.replies:
						if m.Error == nil || m.Error.Code != -32603 {
							t.Fatalf("reply before the release: error %+v, want -32603", m.Error)
						}
						rejected++
					default:
						return int(g.entered.Load())+rejected == requests
					}
				}
			})
			delta, heldBytes, heldRequests := retained(t, c, before)
			if delta > budget+slack {
				t.Errorf("%d requests held with %d-byte ids retain %d bytes, over the %d-byte budget; the budget counts %d bytes", heldRequests, idSize, delta, budget, heldBytes)
			}

			g.open()
			for i := 0; i < heldRequests; i++ {
				if m := p.reply(); m.Error != nil {
					t.Errorf("held request: %+v", m.Error)
				}
			}
			waitHeld(t, c, 0, 0)
		})
	}
}
