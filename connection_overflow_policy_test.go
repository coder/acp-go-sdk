package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// overflowWait bounds each wait in these tests. It guards against a hang; it is
// not a latency budget, so it is generous for slow CI runners under -race.
const overflowWait = 5 * time.Second

// overflowFixture drives a Connection whose single notification consumer is
// held inside its first notification, so that every later notification must
// queue — and the queue can be overfilled deterministically.
type overflowFixture struct {
	c          *Connection
	incoming   *io.PipeWriter
	release    chan struct{}
	deliveries chan int

	mu        sync.Mutex
	delivered []int
}

func newOverflowFixture(t *testing.T, queueSize int, opts ...ConnectionOption) *overflowFixture {
	t.Helper()
	incomingR, incomingW := io.Pipe()
	f := &overflowFixture{
		incoming:   incomingW,
		release:    make(chan struct{}),
		deliveries: make(chan int, 64),
	}
	started := make(chan struct{})
	var once sync.Once

	opts = append([]ConnectionOption{WithMaxQueuedNotifications(queueSize)}, opts...)
	f.c = NewConnection(func(_ context.Context, _ string, params json.RawMessage) (any, *RequestError) {
		var p struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(params, &p)
		f.mu.Lock()
		f.delivered = append(f.delivered, p.N)
		f.mu.Unlock()
		f.deliveries <- p.N
		once.Do(func() {
			close(started)
			<-f.release
		})
		return nil, nil
	}, io.Discard, incomingR, opts...)
	t.Cleanup(func() { _ = incomingW.Close() })

	f.notify(t, 0)
	select {
	case <-started:
	case <-time.After(overflowWait):
		t.Fatal("first notification handler did not start")
	}
	return f
}

// notify writes one test/notify notification numbered n.
//
// The write is bounded: io.Pipe blocks a writer until the connection reads, so
// if a regression stopped the reader, an unbounded write would hang the test
// until go test's own timeout instead of failing it here.
func (f *overflowFixture) notify(t *testing.T, n int) {
	t.Helper()
	line := fmt.Sprintf(`{"jsonrpc":"2.0","method":"test/notify","params":{"n":%d}}`+"\n", n)
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(f.incoming, line)
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("write notification %d: %v", n, err)
		}
	case <-time.After(overflowWait):
		t.Fatalf("write of notification %d blocked: the connection stopped reading", n)
	}
}

// waitDelivered blocks until notification n has been handled.
func (f *overflowFixture) waitDelivered(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(overflowWait)
	for {
		select {
		case got := <-f.deliveries:
			if got == n {
				return
			}
		case <-deadline:
			t.Fatalf("notification %d was never delivered; delivered %v", n, f.deliveredSoFar())
		}
	}
}

func (f *overflowFixture) deliveredSoFar() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.delivered...)
}

// waitForDroppedNotifications polls until c has dropped want notifications. It
// is shaped like waitForNotificationBarrierDrain for the one case with no event
// to wait on: a connection with no drop handler installed.
func waitForDroppedNotifications(t *testing.T, c *Connection, want uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.DroppedNotifications() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("DroppedNotifications() = %d, want %d", c.DroppedNotifications(), want)
}

// TestConnection_NotificationOverflowPolicyClosesByDefault pins the default:
// without the option, and with any policy other than OverflowDropNewest, an
// overflow closes the connection exactly as it did before the option existed,
// and nothing is counted or reported as dropped.
func TestConnection_NotificationOverflowPolicyClosesByDefault(t *testing.T) {
	cases := []struct {
		name string
		opts []ConnectionOption
	}{
		{"no option", nil},
		{"explicit close", []ConnectionOption{WithNotificationOverflowPolicy(OverflowCloseConnection)}},
		{"undefined policy", []ConnectionOption{WithNotificationOverflowPolicy(NotificationOverflowPolicy(99))}},
	}
	for _, tc := range cases {
		tc := tc // go.mod targets go 1.21: range variables are per-loop, not per-iteration
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			opts := append(slices.Clone(tc.opts), WithNotificationDropHandler(func(string, uint64) { calls.Add(1) }))
			f := newOverflowFixture(t, 4, opts...)

			for n := 1; n <= 5; n++ { // 1-4 fill the queue, 5 overflows
				f.notify(t, n)
			}
			select {
			case <-f.c.Done():
			case <-time.After(overflowWait):
				t.Fatal("connection stayed open after overflow; the default must close it")
			}
			if cause := context.Cause(f.c.ctx); !errors.Is(cause, errNotificationQueueOverflow) {
				t.Fatalf("close cause = %v, want errNotificationQueueOverflow", cause)
			}
			if got := f.c.DroppedNotifications(); got != 0 {
				t.Errorf("DroppedNotifications() = %d, want 0 when closing", got)
			}
			close(f.release)
			waitForNotificationBarrierDrain(t, f.c, overflowWait)
			if n := calls.Load(); n != 0 {
				t.Errorf("drop handler called %d times, want 0 when closing", n)
			}
		})
	}
}

// TestConnection_NotificationOverflowPolicyDropNewestKeepsConnectionOpen pins
// the new policy: overflowing notifications are dropped and reported, the
// connection stays open, the queued ones are delivered in order, and a
// notification sent after the backlog clears is still delivered — which also
// shows the sequence numbers released by each drop were reused cleanly.
func TestConnection_NotificationOverflowPolicyDropNewestKeepsConnectionOpen(t *testing.T) {
	type drop struct {
		method string
		total  uint64
	}
	drops := make(chan drop, 16)
	f := newOverflowFixture(
		t, 4,
		WithNotificationOverflowPolicy(OverflowDropNewest),
		WithNotificationDropHandler(func(method string, total uint64) {
			drops <- drop{method, total}
		}),
	)

	for n := 1; n <= 7; n++ { // 1-4 fill the queue; 5, 6 and 7 must be dropped
		f.notify(t, n)
	}
	for want := uint64(1); want <= 3; want++ {
		select {
		case d := <-drops:
			if d.method != "test/notify" || d.total != want {
				t.Fatalf("drop %d reported (%q, %d), want (%q, %d)", want, d.method, d.total, "test/notify", want)
			}
		case <-time.After(overflowWait):
			t.Fatalf("drop %d was never reported", want)
		}
	}
	select {
	case <-f.c.Done():
		t.Fatalf("connection closed on overflow under OverflowDropNewest: %v", context.Cause(f.c.ctx))
	default:
	}
	if got := f.c.DroppedNotifications(); got != 3 {
		t.Fatalf("DroppedNotifications() = %d, want 3", got)
	}

	close(f.release)
	f.waitDelivered(t, 4)
	waitForNotificationBarrierDrain(t, f.c, overflowWait)
	if got, want := f.deliveredSoFar(), []int{0, 1, 2, 3, 4}; !slices.Equal(got, want) {
		t.Fatalf("delivered %v, want %v: queued notifications in order, dropped ones never", got, want)
	}

	// The reader kept running through the drops: a later notification arrives.
	f.notify(t, 8)
	f.waitDelivered(t, 8)
	if got, want := f.deliveredSoFar(), []int{0, 1, 2, 3, 4, 8}; !slices.Equal(got, want) {
		t.Fatalf("delivered %v after the backlog cleared, want %v", got, want)
	}
	if got := f.c.DroppedNotifications(); got != 3 {
		t.Errorf("DroppedNotifications() = %d after recovery, want still 3", got)
	}
}

// TestConnection_NotificationOverflowPolicyDropNewestWithoutHandler checks that
// the handler is optional: with none installed, drops are still counted and
// the connection stays open.
func TestConnection_NotificationOverflowPolicyDropNewestWithoutHandler(t *testing.T) {
	f := newOverflowFixture(t, 2, WithNotificationOverflowPolicy(OverflowDropNewest))
	for n := 1; n <= 4; n++ { // 1-2 fill the queue; 3 and 4 are dropped
		f.notify(t, n)
	}
	waitForDroppedNotifications(t, f.c, 2, overflowWait)
	select {
	case <-f.c.Done():
		t.Fatal("connection closed on overflow under OverflowDropNewest")
	default:
	}
	close(f.release)
	f.waitDelivered(t, 2)
	if got, want := f.deliveredSoFar(), []int{0, 1, 2}; !slices.Equal(got, want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
	if got := f.c.DroppedNotifications(); got != 2 {
		t.Errorf("DroppedNotifications() = %d, want exactly 2", got)
	}
}

// TestClientSideConnection_DroppedNotificationsForwards covers the accessor on
// the type most consumers hold: ClientSideConnection does not expose its
// underlying Connection, so without the forwarder the counter is unreachable.
func TestClientSideConnection_DroppedNotificationsForwards(t *testing.T) {
	agentOut, agentOutW := io.Pipe()
	t.Cleanup(func() { _ = agentOutW.Close() })
	csc := NewClientSideConnection(&clientFuncs{}, io.Discard, agentOut)
	csc.conn.droppedNotifications.Store(7)
	if got := csc.DroppedNotifications(); got != 7 {
		t.Fatalf("ClientSideConnection.DroppedNotifications() = %d, want 7", got)
	}
}

// TestAgentSideConnection_DroppedNotificationsForwards is the agent-side twin.
func TestAgentSideConnection_DroppedNotificationsForwards(t *testing.T) {
	clientOut, clientOutW := io.Pipe()
	t.Cleanup(func() { _ = clientOutW.Close() })
	asc := NewAgentSideConnection(agentFuncs{}, io.Discard, clientOut)
	asc.conn.droppedNotifications.Store(5)
	if got := asc.DroppedNotifications(); got != 5 {
		t.Fatalf("AgentSideConnection.DroppedNotifications() = %d, want 5", got)
	}
}
