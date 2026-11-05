package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestNewAgentSideConnection_InputAlreadyWaiting covers a peer that has
// written a request before the connection exists, as a pre-filled stdin
// does. The prompt handler reads the connection's watermarks, so the
// receive loop must not start until NewAgentSideConnection has stored
// conn. Run under -race to see the race this guards against (#58).
func TestNewAgentSideConnection_InputAlreadyWaiting(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"hi"}]}}` + "\n")
	outR, outW := io.Pipe()
	t.Cleanup(func() {
		_ = outR.Close()
		_ = outW.Close()
	})

	asc := NewAgentSideConnection(agentFuncs{
		PromptFunc: func(context.Context, PromptRequest) (PromptResponse, error) {
			return PromptResponse{StopReason: StopReasonEndTurn}, nil
		},
	}, outW, in)
	if asc.conn == nil {
		t.Fatal("NewAgentSideConnection() conn = nil, want a connection")
	}

	line, err := bufio.NewReader(outR).ReadBytes('\n')
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	var resp struct {
		ID     int             `json:"id"`
		Result *PromptResponse `json:"result"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("json.Unmarshal(%s) = %v, want nil", line, err)
	}
	if resp.ID != 1 || resp.Result == nil || resp.Result.StopReason != StopReasonEndTurn {
		t.Errorf("response = %s, want an end_turn result for id 1", line)
	}
}

// TestNewClientSideConnection_InputAlreadyWaiting is the client side of the
// same case: a notification that is already waiting reaches the handler only
// after conn is stored.
func TestNewClientSideConnection_InputAlreadyWaiting(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}` + "\n")
	got := make(chan SessionNotification, 1)
	csc := NewClientSideConnection(&clientFuncs{
		SessionUpdateFunc: func(_ context.Context, n SessionNotification) error {
			got <- n
			return nil
		},
	}, io.Discard, in)
	if csc.conn == nil {
		t.Fatal("NewClientSideConnection() conn = nil, want a connection")
	}
	if n := <-got; n.SessionId != "s1" {
		t.Errorf("SessionUpdate() sessionId = %q, want %q", n.SessionId, "s1")
	}
}
