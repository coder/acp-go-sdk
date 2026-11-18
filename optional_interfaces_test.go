package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// bareAgent and bareClient have only the methods of Agent and Client.
// Embedding an interface type promotes its methods and nothing else, so they
// implement no optional interface.
type (
	bareAgent  struct{ Agent }
	bareClient struct{ Client }
)

// syncBuffer is a log sink that a connection's goroutines may write to while
// the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// connectPair connects agent and client over pipes and returns both sides,
// with the agent side logging to agentLog from the DEBUG level up.
func connectPair(t *testing.T, agent Agent, client Client, agentLog io.Writer) (*AgentSideConnection, *ClientSideConnection) {
	t.Helper()
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	t.Cleanup(func() {
		_ = c2aW.Close()
		_ = a2cW.Close()
	})
	agentConn := NewAgentSideConnection(agent, a2cW, c2aR)
	agentConn.SetLogger(slog.New(slog.NewTextHandler(agentLog, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return agentConn, NewClientSideConnection(client, c2aW, a2cR)
}

func wantMethodNotFound(t *testing.T, err error, method string) {
	t.Helper()
	var re *RequestError
	if !errors.As(err, &re) || re.Code != -32601 {
		t.Fatalf("%s: got error %v, want -32601 (method not found)", method, err)
	}
	var data struct{ Method string }
	if err := json.Unmarshal(re.Data, &data); err != nil || data.Method != method {
		t.Errorf("%s: error data %s, want the method name", method, re.Data)
	}
}

var (
	loadRequest = LoadSessionRequest{SessionId: "s1", Cwd: "/work", McpServers: []McpServer{}}
	forkRequest = UnstableForkSessionRequest{SessionId: "s1", Cwd: "/work"}
	mcpRequest  = UnstableMessageMcpRequest{ServerId: "docs", RequestId: "1", Method: "tools/list"}
	mcpNote     = UnstableMessageMcpNotification{ServerId: "docs", RequestId: "1", Method: "notifications/progress"}
)

func TestOptionalInterface_AbsentMethodIsMethodNotFound(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	agentLog := &syncBuffer{}
	agentConn, clientConn := connectPair(t, bareAgent{agentFuncs{}}, bareClient{&clientFuncs{}}, agentLog)

	// The client's mcp/message notification goes first: the agent handles the
	// session/load request that follows only after it.
	if err := clientConn.UnstableMessageMcp(ctx, mcpNote); err != nil {
		t.Fatalf("send mcp/message notification: %v", err)
	}
	_, err := clientConn.LoadSession(ctx, loadRequest)
	wantMethodNotFound(t, err, AgentMethodSessionLoad)
	// A notification needs no answer, so the agent ignores it at DEBUG.
	if got := agentLog.String(); !strings.Contains(got, `level=DEBUG msg="ignored unhandled notification" method=mcp/message`) || !strings.Contains(got, "Method not found") || strings.Contains(got, "level=ERROR") {
		t.Errorf("agent log %q, want only a DEBUG method-not-found record for the mcp/message notification", got)
	}

	_, err = clientConn.UnstableForkSession(ctx, forkRequest)
	wantMethodNotFound(t, err, AgentMethodSessionFork)

	_, err = agentConn.UnstableMessageMcp(ctx, mcpRequest)
	wantMethodNotFound(t, err, ClientMethodMcpMessage)
}

func TestOptionalInterface_ImplementedMethodIsDispatched(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan string, 4)
	agent := agentFuncs{
		LoadSessionFunc: func(_ context.Context, p LoadSessionRequest) (LoadSessionResponse, error) {
			got <- "session/load " + string(p.SessionId)
			return LoadSessionResponse{}, nil
		},
		UnstableForkSessionFunc: func(_ context.Context, p UnstableForkSessionRequest) (UnstableForkSessionResponse, error) {
			got <- "session/fork " + string(p.SessionId)
			return UnstableForkSessionResponse{SessionId: "s2"}, nil
		},
		UnstableMessageMcpFunc: func(_ context.Context, n UnstableMessageMcpNotification) error {
			got <- "mcp/message " + n.Method
			return nil
		},
	}
	client := &mcpClient{got: make(chan UnstableMessageMcpRequest, 1)}
	agentLog := &syncBuffer{}
	agentConn, clientConn := connectPair(t, agent, client, agentLog)

	if err := clientConn.UnstableMessageMcp(ctx, mcpNote); err != nil {
		t.Fatalf("send mcp/message notification: %v", err)
	}
	if _, err := clientConn.LoadSession(ctx, loadRequest); err != nil {
		t.Fatalf("session/load: %v", err)
	}
	fork, err := clientConn.UnstableForkSession(ctx, forkRequest)
	if err != nil {
		t.Fatalf("session/fork: %v", err)
	}
	if fork.SessionId != "s2" {
		t.Errorf("session/fork returned session %q, want s2", fork.SessionId)
	}
	for _, want := range []string{"mcp/message notifications/progress", "session/load s1", "session/fork s1"} {
		if g := <-got; g != want {
			t.Errorf("agent handled %q, want %q", g, want)
		}
	}
	if _, err := agentConn.UnstableMessageMcp(ctx, mcpRequest); err != nil {
		t.Fatalf("mcp/message request: %v", err)
	}
	if req := <-client.got; req.Method != "tools/list" {
		t.Errorf("client handled %q, want tools/list", req.Method)
	}
	if log := agentLog.String(); log != "" {
		t.Errorf("agent logged %q, want nothing", log)
	}
}

// Every agent and client implements Agent or Client, so a method that either
// gains, loses or changes breaks them all. Both are frozen at the stable
// methods of schema 1.25.0 (cmd/generate/internal/ir/baseline.go): a method
// that a later schema adds or makes stable gets its own optional interface.
// The lists are literal, so a shrunk baseline or a renamed request type fails
// here instead of in an implementer's build.
func TestAgentAndClient_KeepTheirMethods(t *testing.T) {
	tests := []struct {
		iface reflect.Type
		want  []string
	}{
		{reflect.TypeOf((*Agent)(nil)).Elem(), []string{
			"Authenticate func(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error)",
			"Cancel func(context.Context, acp.CancelNotification) error",
			"CloseSession func(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error)",
			"DeleteSession func(context.Context, acp.DeleteSessionRequest) (acp.DeleteSessionResponse, error)",
			"Initialize func(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error)",
			"ListSessions func(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error)",
			"Logout func(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error)",
			"NewSession func(context.Context, acp.NewSessionRequest) (acp.NewSessionResponse, error)",
			"Prompt func(context.Context, acp.PromptRequest) (acp.PromptResponse, error)",
			"ResumeSession func(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error)",
			"SetSessionConfigOption func(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error)",
			"SetSessionMode func(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error)",
		}},
		{reflect.TypeOf((*Client)(nil)).Elem(), []string{
			"CompleteElicitation func(context.Context, acp.CompleteElicitationNotification) error",
			"CreateElicitation func(context.Context, acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error)",
			"CreateTerminal func(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error)",
			"KillTerminal func(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error)",
			"ReadTextFile func(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error)",
			"ReleaseTerminal func(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error)",
			"RequestPermission func(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)",
			"SessionUpdate func(context.Context, acp.SessionNotification) error",
			"TerminalOutput func(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error)",
			"WaitForTerminalExit func(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error)",
			"WriteTextFile func(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error)",
		}},
	}
	for _, tt := range tests {
		got := make([]string, 0, tt.iface.NumMethod())
		for i := 0; i < tt.iface.NumMethod(); i++ {
			m := tt.iface.Method(i)
			got = append(got, m.Name+" "+m.Type.String())
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s has the methods\n%s\nwant\n%s", tt.iface.Name(), strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
		}
	}
}
