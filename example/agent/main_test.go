package main

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
)

// testClient records the agent's session updates. The embedded nil
// acp.Client panics on any other method that the agent calls.
type testClient struct {
	acp.Client
	updates chan acp.SessionNotification
}

func (c *testClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	select {
	case c.updates <- n:
	default:
	}
	return nil
}

func (c *testClient) RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, errors.New("the test does not expect a permission request")
}

// connect wires the example agent to an SDK client over pipes, as main
// wires it to stdio.
func connect(t *testing.T) (*acp.ClientSideConnection, *testClient) {
	t.Helper()
	c, client, _ := connectAgent(t)
	return c, client
}

// connectAgent is connect that also returns the agent, for a test that calls
// its methods directly.
func connectAgent(t *testing.T) (*acp.ClientSideConnection, *testClient, *exampleAgent) {
	t.Helper()
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	t.Cleanup(func() {
		_ = c2aW.Close()
		_ = a2cW.Close()
	})
	ag := newExampleAgent()
	ag.SetAgentConnection(acp.NewAgentSideConnection(ag, a2cW, c2aR))
	client := &testClient{updates: make(chan acp.SessionNotification, 64)}
	return acp.NewClientSideConnection(client, c2aW, a2cR), client, ag
}

func newSession(t *testing.T, c *acp.ClientSideConnection, cwd string) acp.SessionId {
	t.Helper()
	resp, err := c.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	return resp.SessionId
}

func listSessions(t *testing.T, c *acp.ClientSideConnection, cwd *string) []acp.SessionInfo {
	t.Helper()
	resp, err := c.ListSessions(context.Background(), acp.ListSessionsRequest{Cwd: cwd})
	if err != nil {
		t.Fatalf("session/list: %v", err)
	}
	if resp.NextCursor != nil {
		t.Fatalf("session/list returned a next cursor %q, want one page", *resp.NextCursor)
	}
	return resp.Sessions
}

func TestExampleAgent_ListSessions(t *testing.T) {
	c, _ := connect(t)
	a := newSession(t, c, "/w1")
	b := newSession(t, c, "/w2")
	d := newSession(t, c, "/w1")

	tests := []struct {
		name string
		cwd  *string
		want []acp.SessionInfo
	}{
		{"every session in creation order", nil, []acp.SessionInfo{{SessionId: a, Cwd: "/w1"}, {SessionId: b, Cwd: "/w2"}, {SessionId: d, Cwd: "/w1"}}},
		{"sessions of one cwd", acp.Ptr("/w1"), []acp.SessionInfo{{SessionId: a, Cwd: "/w1"}, {SessionId: d, Cwd: "/w1"}}},
		{"cwd without sessions", acp.Ptr("/none"), []acp.SessionInfo{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := listSessions(t, c, tt.cwd); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("session/list = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestExampleAgent_DeleteSession(t *testing.T) {
	ctx := context.Background()
	c, _ := connect(t)
	a := newSession(t, c, "/w")
	b := newSession(t, c, "/w")

	for _, id := range []acp.SessionId{a, a, "sess_unknown"} {
		if _, err := c.DeleteSession(ctx, acp.DeleteSessionRequest{SessionId: id}); err != nil {
			t.Fatalf("session/delete %s: %v", id, err)
		}
	}
	want := []acp.SessionInfo{{SessionId: b, Cwd: "/w"}}
	if got := listSessions(t, c, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("session/list after the delete = %+v, want %+v", got, want)
	}
	if _, err := c.Prompt(ctx, acp.PromptRequest{SessionId: a, Prompt: []acp.ContentBlock{acp.TextBlock("hi")}}); err == nil {
		t.Fatal("session/prompt on the deleted session succeeded, want an error")
	}
}

func TestExampleAgent_DeleteSessionCancelsRunningTurn(t *testing.T) {
	ctx := context.Background()
	c, client := connect(t)
	sid := newSession(t, c, "/w")

	type result struct {
		resp acp.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := c.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock("hi")}})
		done <- result{resp, err}
	}()
	select {
	case <-client.updates:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn sent no update")
	}
	if _, err := c.DeleteSession(ctx, acp.DeleteSessionRequest{SessionId: sid}); err != nil {
		t.Fatalf("session/delete: %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.resp.StopReason != acp.StopReasonCancelled {
			t.Fatalf("session/prompt = %+v, %v; want stop reason %q", r.resp, r.err, acp.StopReasonCancelled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not end after session/delete")
	}
}

// TestExampleAgent_CancelDuringPrompts calls Cancel while prompts install and
// clear their cancel funcs. Run it with -race: Cancel must read the session's
// cancel func under the lock that Prompt writes it under.
func TestExampleAgent_CancelDuringPrompts(t *testing.T) {
	ctx := context.Background()
	c, _, ag := connectAgent(t)
	sid := newSession(t, c, "/w")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = ag.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock("hi")}})
		}()
		go func() {
			defer wg.Done()
			_ = ag.Cancel(ctx, acp.CancelNotification{SessionId: sid})
		}()
	}
	// The last prompt may still run: cancel it until every prompt returns.
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	for {
		select {
		case <-finished:
			return
		case <-time.After(10 * time.Millisecond):
			_ = ag.Cancel(ctx, acp.CancelNotification{SessionId: sid})
		}
	}
}
