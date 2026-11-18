package acp

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"time"
)

// mcpClient is a client that hosts MCP-over-ACP servers: it answers the
// agent's mcp/message requests.
type mcpClient struct {
	clientFuncs
	got chan UnstableMessageMcpRequest
}

var _ ClientUnstableMessageMcp = (*mcpClient)(nil)

func (c *mcpClient) UnstableMessageMcp(_ context.Context, p UnstableMessageMcpRequest) (UnstableMessageMcpResponse, error) {
	c.got <- p
	if p.Method == "tools/call" {
		return UnstableMessageMcpResponse{Error: &UnstableMessageMcpError{Error: UnstableMcpError{Code: -32602, Message: "unknown tool"}}}, nil
	}
	return UnstableMessageMcpResponse{Result: &UnstableMessageMcpResult{Result: json.RawMessage(`{"tools":[]}`)}}, nil
}

func TestMcpMessage_ReachesBothSides(t *testing.T) {
	t.Parallel()
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	notes := make(chan UnstableMessageMcpNotification, 1)
	agentConn := NewAgentSideConnection(agentFuncs{
		UnstableMessageMcpFunc: func(_ context.Context, n UnstableMessageMcpNotification) error {
			notes <- n
			return nil
		},
	}, a2cW, c2aR)
	client := &mcpClient{got: make(chan UnstableMessageMcpRequest, 2)}
	clientConn := NewClientSideConnection(client, c2aW, a2cR)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The agent sends an MCP request to a server that the client hosts.
	req := UnstableMessageMcpRequest{ServerId: "docs", RequestId: "1", Method: "tools/list", Params: map[string]json.RawMessage{"cursor": json.RawMessage(`"c1"`)}}
	resp, err := agentConn.UnstableMessageMcp(ctx, req)
	if err != nil {
		t.Fatalf("mcp/message request: %v", err)
	}
	if got := <-client.got; !reflect.DeepEqual(got, req) {
		t.Errorf("client got %+v, want %+v", got, req)
	}
	if resp.Result == nil || string(resp.Result.Result) != `{"tools":[]}` {
		t.Errorf("agent got result %+v, want {tools: []}", resp.Result)
	}

	// An MCP error is a result of the request, not a JSON-RPC error.
	resp, err = agentConn.UnstableMessageMcp(ctx, UnstableMessageMcpRequest{ServerId: "docs", RequestId: "2", Method: "tools/call"})
	if err != nil {
		t.Fatalf("mcp/message request with an MCP error: %v", err)
	}
	<-client.got
	if resp.Error == nil || resp.Error.Error.Code != -32602 || resp.Error.Error.Message != "unknown tool" {
		t.Errorf("agent got %+v, want the MCP error -32602 unknown tool", resp)
	}

	// The client forwards a server notification that belongs to the request.
	note := UnstableMessageMcpNotification{ServerId: "docs", RequestId: "1", Method: "notifications/progress", Params: map[string]json.RawMessage{"progress": json.RawMessage("1")}}
	if err := clientConn.UnstableMessageMcp(ctx, note); err != nil {
		t.Fatalf("mcp/message notification: %v", err)
	}
	select {
	case got := <-notes:
		if !reflect.DeepEqual(got, note) {
			t.Errorf("agent got %+v, want %+v", got, note)
		}
	case <-ctx.Done():
		t.Fatal("the agent did not receive the mcp/message notification")
	}
}
