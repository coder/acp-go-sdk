package acp

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newNotificationBarrierTestPair(t *testing.T, client Client, agent Agent) (*ClientSideConnection, *AgentSideConnection) {
	t.Helper()

	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	clientConn := NewClientSideConnection(client, c2aW, a2cR)
	agentConn := NewAgentSideConnection(agent, a2cW, c2aR)

	t.Cleanup(func() {
		_ = c2aR.Close()
		_ = c2aW.Close()
		_ = a2cR.Close()
		_ = a2cW.Close()
	})

	return clientConn, agentConn
}

func testLoadSessionRequest(sessionID string) LoadSessionRequest {
	return LoadSessionRequest{
		SessionId:  SessionId(sessionID),
		Cwd:        "/",
		McpServers: []McpServer{},
	}
}

func testSessionUpdate(sessionID SessionId, seq int) SessionNotification {
	return SessionNotification{
		Meta:      map[string]any{"seq": seq},
		SessionId: sessionID,
		Update: SessionUpdate{
			AgentMessageChunk: &SessionUpdateAgentMessageChunk{
				Content: TextBlock(fmt.Sprintf("chunk-%d", seq)),
			},
		},
	}
}

func notificationSequence(t *testing.T, n SessionNotification) int {
	t.Helper()

	value, ok := n.Meta["seq"]
	if !ok {
		t.Fatalf("notification missing seq metadata")
	}

	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		t.Fatalf("unexpected seq metadata type %T", value)
		return 0
	}
}

func waitForPendingRequests(t *testing.T, c *Connection, want int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := len(c.pending)
		c.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}

	c.mu.Lock()
	got := len(c.pending)
	c.mu.Unlock()
	t.Fatalf("pending requests = %d, want %d", got, want)
}

func waitForNotificationBarrierDrain(t *testing.T, c *Connection, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.notifyMu.Lock()
		completed := c.completedNotificationSeq
		enqueued := c.lastEnqueuedNotificationSeq
		c.notifyMu.Unlock()
		if completed >= enqueued {
			return
		}
		time.Sleep(time.Millisecond)
	}

	c.notifyMu.Lock()
	completed := c.completedNotificationSeq
	enqueued := c.lastEnqueuedNotificationSeq
	c.notifyMu.Unlock()
	t.Fatalf("notification barrier did not drain: completed=%d enqueued=%d", completed, enqueued)
}

func TestSendRequest_WaitsForPreResponseNotification(t *testing.T) {
	handlerStarted := make(chan struct{})
	handlerFinished := make(chan struct{})
	releaseHandler := make(chan struct{})
	allowResponse := make(chan struct{})

	client := &clientFuncs{
		SessionUpdateFunc: func(context.Context, SessionNotification) error {
			close(handlerStarted)
			<-releaseHandler
			close(handlerFinished)
			return nil
		},
	}

	var agentConn *AgentSideConnection
	agent := agentFuncs{
		LoadSessionFunc: func(ctx context.Context, req LoadSessionRequest) (LoadSessionResponse, error) {
			if err := agentConn.SessionUpdate(ctx, testSessionUpdate(req.SessionId, 1)); err != nil {
				return LoadSessionResponse{}, err
			}
			<-allowResponse
			return LoadSessionResponse{}, nil
		},
	}

	clientConn, createdAgentConn := newNotificationBarrierTestPair(t, client, agent)
	agentConn = createdAgentConn

	resultCh := make(chan error, 1)
	go func() {
		_, err := clientConn.LoadSession(context.Background(), testLoadSessionRequest("pre-response"))
		resultCh <- err
	}()

	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for pre-response notification handler to start")
	}

	close(allowResponse)
	waitForPendingRequests(t, clientConn.conn, 0, time.Second)

	select {
	case err := <-resultCh:
		t.Fatalf("LoadSession returned before notification handler finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseHandler)

	select {
	case <-handlerFinished:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for pre-response notification handler to finish")
	}

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("LoadSession returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for LoadSession to return after notification handler finished")
	}
}

func TestSendRequest_DoesNotWaitForPostResponseNotification(t *testing.T) {
	handlerStarted := make(chan struct{})
	handlerFinished := make(chan struct{})
	releaseHandler := make(chan struct{})
	notificationErrCh := make(chan error, 1)

	client := &clientFuncs{
		SessionUpdateFunc: func(context.Context, SessionNotification) error {
			close(handlerStarted)
			<-releaseHandler
			close(handlerFinished)
			return nil
		},
	}

	var agentConn *AgentSideConnection
	agent := agentFuncs{
		LoadSessionFunc: func(context.Context, LoadSessionRequest) (LoadSessionResponse, error) {
			go func() {
				time.Sleep(25 * time.Millisecond)
				notificationErrCh <- agentConn.SessionUpdate(context.Background(), testSessionUpdate(SessionId("post-response"), 1))
			}()
			return LoadSessionResponse{}, nil
		},
	}

	clientConn, createdAgentConn := newNotificationBarrierTestPair(t, client, agent)
	agentConn = createdAgentConn

	resultCh := make(chan error, 1)
	go func() {
		_, err := clientConn.LoadSession(context.Background(), testLoadSessionRequest("post-response"))
		resultCh <- err
	}()

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("LoadSession returned error: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("LoadSession blocked on post-response notification")
	}

	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for post-response notification handler to start")
	}

	select {
	case err := <-notificationErrCh:
		if err != nil {
			t.Fatalf("post-response notification send failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for post-response notification send to complete")
	}

	select {
	case <-handlerFinished:
		t.Fatalf("post-response notification handler finished before release")
	default:
	}

	close(releaseHandler)

	select {
	case <-handlerFinished:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for post-response notification handler to finish")
	}

	select {
	case err := <-notificationErrCh:
		if err != nil {
			t.Fatalf("post-response notification send failed: %v", err)
		}
	default:
	}
}

func TestSendRequest_ConcurrentRequestsDoNotPanic(t *testing.T) {
	const concurrentRequests = 32
	const notificationsPerRequest = 2

	client := &clientFuncs{
		SessionUpdateFunc: func(context.Context, SessionNotification) error {
			time.Sleep(2 * time.Millisecond)
			return nil
		},
	}

	var agentConn *AgentSideConnection
	agent := agentFuncs{
		LoadSessionFunc: func(ctx context.Context, req LoadSessionRequest) (LoadSessionResponse, error) {
			for i := 0; i < notificationsPerRequest; i++ {
				if err := agentConn.SessionUpdate(ctx, testSessionUpdate(req.SessionId, i)); err != nil {
					return LoadSessionResponse{}, err
				}
				time.Sleep(time.Millisecond)
			}
			return LoadSessionResponse{}, nil
		},
	}

	clientConn, createdAgentConn := newNotificationBarrierTestPair(t, client, agent)
	agentConn = createdAgentConn

	start := make(chan struct{})
	errCh := make(chan error, concurrentRequests)
	var wg sync.WaitGroup

	for i := 0; i < concurrentRequests; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			_, err := clientConn.LoadSession(ctx, testLoadSessionRequest(fmt.Sprintf("concurrent-%d", i)))
			errCh <- err
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("LoadSession returned error: %v", err)
		}
	}
}

func TestLoadSession_NotificationReplayOrdering(t *testing.T) {
	const replayUpdates = 5

	var (
		mu   sync.Mutex
		seen []int
	)

	client := &clientFuncs{
		SessionUpdateFunc: func(_ context.Context, n SessionNotification) error {
			mu.Lock()
			seen = append(seen, notificationSequence(t, n))
			mu.Unlock()
			return nil
		},
	}

	var agentConn *AgentSideConnection
	agent := agentFuncs{
		LoadSessionFunc: func(ctx context.Context, req LoadSessionRequest) (LoadSessionResponse, error) {
			for i := 0; i < replayUpdates; i++ {
				if err := agentConn.SessionUpdate(ctx, testSessionUpdate(req.SessionId, i)); err != nil {
					return LoadSessionResponse{}, err
				}
			}
			return LoadSessionResponse{}, nil
		},
	}

	clientConn, createdAgentConn := newNotificationBarrierTestPair(t, client, agent)
	agentConn = createdAgentConn

	if _, err := clientConn.LoadSession(context.Background(), testLoadSessionRequest("replay-order")); err != nil {
		t.Fatalf("LoadSession returned error: %v", err)
	}

	mu.Lock()
	got := append([]int(nil), seen...)
	mu.Unlock()

	if len(got) != replayUpdates {
		t.Fatalf("replayed notifications = %d, want %d", len(got), replayUpdates)
	}
	for i, seq := range got {
		if seq != i {
			t.Fatalf("replayed notifications out of order: got %v", got)
		}
	}
}

func TestShutdownDrainsNotifications_WithBarrier(t *testing.T) {
	handlerStarted := make(chan struct{})
	var handlerCompleted atomic.Bool

	client := &clientFuncs{
		SessionUpdateFunc: func(context.Context, SessionNotification) error {
			close(handlerStarted)
			time.Sleep(50 * time.Millisecond)
			handlerCompleted.Store(true)
			return nil
		},
	}

	clientConn, agentConn := newNotificationBarrierTestPair(t, client, agentFuncs{})

	if err := agentConn.SessionUpdate(context.Background(), testSessionUpdate(SessionId("shutdown"), 1)); err != nil {
		t.Fatalf("SessionUpdate returned error: %v", err)
	}

	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for shutdown notification handler to start")
	}

	if writer, ok := agentConn.conn.w.(*io.PipeWriter); ok {
		_ = writer.Close()
	} else {
		t.Fatalf("expected io.PipeWriter, got %T", agentConn.conn.w)
	}

	select {
	case <-clientConn.conn.inboundCtx.Done():
		if !handlerCompleted.Load() {
			t.Fatalf("shutdown canceled inbound context before notification handler completed")
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for shutdown to drain notifications")
	}
}
