package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clientFuncs struct {
	WriteTextFileFunc     func(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error)
	ReadTextFileFunc      func(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error)
	RequestPermissionFunc func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error)
	SessionUpdateFunc     func(context.Context, SessionNotification) error
	// Terminal-related handlers
	CreateTerminalFunc      func(context.Context, CreateTerminalRequest) (CreateTerminalResponse, error)
	KillTerminalCommandFunc func(context.Context, KillTerminalCommandRequest) (KillTerminalCommandResponse, error)
	ReleaseTerminalFunc     func(context.Context, ReleaseTerminalRequest) (ReleaseTerminalResponse, error)
	TerminalOutputFunc      func(context.Context, TerminalOutputRequest) (TerminalOutputResponse, error)
	WaitForTerminalExitFunc func(context.Context, WaitForTerminalExitRequest) (WaitForTerminalExitResponse, error)

	HandleExtensionMethodFunc func(context.Context, string, json.RawMessage) (any, error)
}

var _ ExtensionMethodHandler = (*clientFuncs)(nil)

var _ Client = (*clientFuncs)(nil)

func (c clientFuncs) WriteTextFile(ctx context.Context, p WriteTextFileRequest) (WriteTextFileResponse, error) {
	if c.WriteTextFileFunc != nil {
		return c.WriteTextFileFunc(ctx, p)
	}
	return WriteTextFileResponse{}, nil
}

func (c clientFuncs) ReadTextFile(ctx context.Context, p ReadTextFileRequest) (ReadTextFileResponse, error) {
	if c.ReadTextFileFunc != nil {
		return c.ReadTextFileFunc(ctx, p)
	}
	return ReadTextFileResponse{}, nil
}

func (c clientFuncs) RequestPermission(ctx context.Context, p RequestPermissionRequest) (RequestPermissionResponse, error) {
	if c.RequestPermissionFunc != nil {
		return c.RequestPermissionFunc(ctx, p)
	}
	return RequestPermissionResponse{}, nil
}

func (c clientFuncs) SessionUpdate(ctx context.Context, n SessionNotification) error {
	if c.SessionUpdateFunc != nil {
		return c.SessionUpdateFunc(ctx, n)
	}
	return nil
}

// CreateTerminal implements Client.
func (c *clientFuncs) CreateTerminal(ctx context.Context, params CreateTerminalRequest) (CreateTerminalResponse, error) {
	if c.CreateTerminalFunc != nil {
		return c.CreateTerminalFunc(ctx, params)
	}
	return CreateTerminalResponse{TerminalId: "test-terminal"}, nil
}

// KillTerminalCommand implements Client.
func (c clientFuncs) KillTerminalCommand(ctx context.Context, params KillTerminalCommandRequest) (KillTerminalCommandResponse, error) {
	if c.KillTerminalCommandFunc != nil {
		return c.KillTerminalCommandFunc(ctx, params)
	}
	return KillTerminalCommandResponse{}, nil
}

// ReleaseTerminal implements Client.
func (c clientFuncs) ReleaseTerminal(ctx context.Context, params ReleaseTerminalRequest) (ReleaseTerminalResponse, error) {
	if c.ReleaseTerminalFunc != nil {
		return c.ReleaseTerminalFunc(ctx, params)
	}
	return ReleaseTerminalResponse{}, nil
}

// TerminalOutput implements Client.
func (c *clientFuncs) TerminalOutput(ctx context.Context, params TerminalOutputRequest) (TerminalOutputResponse, error) {
	if c.TerminalOutputFunc != nil {
		return c.TerminalOutputFunc(ctx, params)
	}
	return TerminalOutputResponse{Output: "ok", Truncated: false}, nil
}

// WaitForTerminalExit implements Client.
func (c *clientFuncs) WaitForTerminalExit(ctx context.Context, params WaitForTerminalExitRequest) (WaitForTerminalExitResponse, error) {
	if c.WaitForTerminalExitFunc != nil {
		return c.WaitForTerminalExitFunc(ctx, params)
	}
	return WaitForTerminalExitResponse{}, nil
}

func (c clientFuncs) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if c.HandleExtensionMethodFunc != nil {
		return c.HandleExtensionMethodFunc(ctx, method, params)
	}
	return nil, NewMethodNotFound(method)
}

type agentFuncs struct {
	InitializeFunc     func(context.Context, InitializeRequest) (InitializeResponse, error)
	NewSessionFunc     func(context.Context, NewSessionRequest) (NewSessionResponse, error)
	LoadSessionFunc    func(context.Context, LoadSessionRequest) (LoadSessionResponse, error)
	AuthenticateFunc   func(context.Context, AuthenticateRequest) (AuthenticateResponse, error)
	PromptFunc         func(context.Context, PromptRequest) (PromptResponse, error)
	CancelFunc         func(context.Context, CancelNotification) error
	SetSessionModeFunc func(ctx context.Context, params SetSessionModeRequest) (SetSessionModeResponse, error)
	// Unstable (schema/meta.unstable.json)
	UnstableForkSessionFunc            func(context.Context, UnstableForkSessionRequest) (UnstableForkSessionResponse, error)
	UnstableListSessionsFunc           func(context.Context, UnstableListSessionsRequest) (UnstableListSessionsResponse, error)
	UnstableResumeSessionFunc          func(context.Context, UnstableResumeSessionRequest) (UnstableResumeSessionResponse, error)
	UnstableSetSessionConfigOptionFunc func(context.Context, UnstableSetSessionConfigOptionRequest) (UnstableSetSessionConfigOptionResponse, error)
	UnstableSetSessionModelFunc        func(context.Context, UnstableSetSessionModelRequest) (UnstableSetSessionModelResponse, error)

	HandleExtensionMethodFunc func(context.Context, string, json.RawMessage) (any, error)
}

var (
	_ Agent                  = (*agentFuncs)(nil)
	_ AgentLoader            = (*agentFuncs)(nil)
	_ AgentExperimental      = (*agentFuncs)(nil)
	_ ExtensionMethodHandler = (*agentFuncs)(nil)
)

func (a agentFuncs) Initialize(ctx context.Context, p InitializeRequest) (InitializeResponse, error) {
	if a.InitializeFunc != nil {
		return a.InitializeFunc(ctx, p)
	}
	return InitializeResponse{}, nil
}

func (a agentFuncs) NewSession(ctx context.Context, p NewSessionRequest) (NewSessionResponse, error) {
	if a.NewSessionFunc != nil {
		return a.NewSessionFunc(ctx, p)
	}
	return NewSessionResponse{}, nil
}

func (a agentFuncs) LoadSession(ctx context.Context, p LoadSessionRequest) (LoadSessionResponse, error) {
	if a.LoadSessionFunc != nil {
		return a.LoadSessionFunc(ctx, p)
	}
	return LoadSessionResponse{}, nil
}

func (a agentFuncs) Authenticate(ctx context.Context, p AuthenticateRequest) (AuthenticateResponse, error) {
	if a.AuthenticateFunc != nil {
		return a.AuthenticateFunc(ctx, p)
	}
	return AuthenticateResponse{}, nil
}

func (a agentFuncs) Prompt(ctx context.Context, p PromptRequest) (PromptResponse, error) {
	if a.PromptFunc != nil {
		return a.PromptFunc(ctx, p)
	}
	return PromptResponse{}, nil
}

func (a agentFuncs) Cancel(ctx context.Context, n CancelNotification) error {
	if a.CancelFunc != nil {
		return a.CancelFunc(ctx, n)
	}
	return nil
}

// SetSessionMode implements Agent.
func (a agentFuncs) SetSessionMode(ctx context.Context, params SetSessionModeRequest) (SetSessionModeResponse, error) {
	if a.SetSessionModeFunc != nil {
		return a.SetSessionModeFunc(ctx, params)
	}
	return SetSessionModeResponse{}, nil
}

// UnstableForkSession implements AgentExperimental.
func (a agentFuncs) UnstableForkSession(ctx context.Context, params UnstableForkSessionRequest) (UnstableForkSessionResponse, error) {
	if a.UnstableForkSessionFunc != nil {
		return a.UnstableForkSessionFunc(ctx, params)
	}
	return UnstableForkSessionResponse{}, nil
}

// UnstableListSessions implements AgentExperimental.
func (a agentFuncs) UnstableListSessions(ctx context.Context, params UnstableListSessionsRequest) (UnstableListSessionsResponse, error) {
	if a.UnstableListSessionsFunc != nil {
		return a.UnstableListSessionsFunc(ctx, params)
	}
	return UnstableListSessionsResponse{}, nil
}

// UnstableResumeSession implements AgentExperimental.
func (a agentFuncs) UnstableResumeSession(ctx context.Context, params UnstableResumeSessionRequest) (UnstableResumeSessionResponse, error) {
	if a.UnstableResumeSessionFunc != nil {
		return a.UnstableResumeSessionFunc(ctx, params)
	}
	return UnstableResumeSessionResponse{}, nil
}

// UnstableSetSessionConfigOption implements AgentExperimental.
func (a agentFuncs) UnstableSetSessionConfigOption(ctx context.Context, params UnstableSetSessionConfigOptionRequest) (UnstableSetSessionConfigOptionResponse, error) {
	if a.UnstableSetSessionConfigOptionFunc != nil {
		return a.UnstableSetSessionConfigOptionFunc(ctx, params)
	}
	return UnstableSetSessionConfigOptionResponse{}, nil
}

// UnstableSetSessionModel implements AgentExperimental.
func (a agentFuncs) UnstableSetSessionModel(ctx context.Context, params UnstableSetSessionModelRequest) (UnstableSetSessionModelResponse, error) {
	if a.UnstableSetSessionModelFunc != nil {
		return a.UnstableSetSessionModelFunc(ctx, params)
	}
	return UnstableSetSessionModelResponse{}, nil
}

func (a agentFuncs) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if a.HandleExtensionMethodFunc != nil {
		return a.HandleExtensionMethodFunc(ctx, method, params)
	}
	return nil, NewMethodNotFound(method)
}

// Test bidirectional error handling similar to typescript/acp.test.ts
func TestConnectionHandlesErrorsBidirectional(t *testing.T) {
	ctx := context.Background()
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	c := NewClientSideConnection(&clientFuncs{
		WriteTextFileFunc: func(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error) {
			return WriteTextFileResponse{}, &RequestError{Code: -32603, Message: "Write failed"}
		},
		ReadTextFileFunc: func(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error) {
			return ReadTextFileResponse{}, &RequestError{Code: -32603, Message: "Read failed"}
		},
		RequestPermissionFunc: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
			return RequestPermissionResponse{}, &RequestError{Code: -32603, Message: "Permission denied"}
		},
		SessionUpdateFunc: func(context.Context, SessionNotification) error { return nil },
	}, c2aW, a2cR)
	agentConn := NewAgentSideConnection(agentFuncs{
		InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
			return InitializeResponse{}, &RequestError{Code: -32603, Message: "Failed to initialize"}
		},
		NewSessionFunc: func(context.Context, NewSessionRequest) (NewSessionResponse, error) {
			return NewSessionResponse{}, &RequestError{Code: -32603, Message: "Failed to create session"}
		},
		LoadSessionFunc: func(context.Context, LoadSessionRequest) (LoadSessionResponse, error) {
			return LoadSessionResponse{}, &RequestError{Code: -32603, Message: "Failed to load session"}
		},
		AuthenticateFunc: func(context.Context, AuthenticateRequest) (AuthenticateResponse, error) {
			return AuthenticateResponse{}, &RequestError{Code: -32603, Message: "Authentication failed"}
		},
		PromptFunc: func(context.Context, PromptRequest) (PromptResponse, error) {
			return PromptResponse{}, &RequestError{Code: -32603, Message: "Prompt failed"}
		},
		CancelFunc: func(context.Context, CancelNotification) error { return nil },
	}, a2cW, c2aR)

	// Client->Agent direction: expect error
	if _, err := agentConn.WriteTextFile(ctx, WriteTextFileRequest{Path: "/test.txt", Content: "test", SessionId: "test-session"}); err == nil {
		t.Fatalf("expected error for writeTextFile, got nil")
	}

	// Agent->Client direction: expect error
	if _, err := c.NewSession(ctx, NewSessionRequest{Cwd: "/test", McpServers: []McpServer{}}); err == nil {
		t.Fatalf("expected error for newSession, got nil")
	}
}

// Test concurrent requests handling similar to TS suite
func TestConnectionHandlesConcurrentRequests(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	var mu sync.Mutex
	requestCount := 0

	_ = NewClientSideConnection(&clientFuncs{
		WriteTextFileFunc: func(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error) {
			mu.Lock()
			requestCount++
			mu.Unlock()
			time.Sleep(40 * time.Millisecond)
			return WriteTextFileResponse{}, nil
		},
		ReadTextFileFunc: func(_ context.Context, req ReadTextFileRequest) (ReadTextFileResponse, error) {
			return ReadTextFileResponse{Content: "Content of " + req.Path}, nil
		},
		RequestPermissionFunc: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
			return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &RequestPermissionOutcomeSelected{OptionId: "allow"}}}, nil
		},
		SessionUpdateFunc: func(context.Context, SessionNotification) error { return nil },
	}, c2aW, a2cR)
	agentConn := NewAgentSideConnection(agentFuncs{
		InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
			return InitializeResponse{ProtocolVersion: ProtocolVersionNumber, AgentCapabilities: AgentCapabilities{LoadSession: false}, AuthMethods: []AuthMethod{}}, nil
		},
		NewSessionFunc: func(context.Context, NewSessionRequest) (NewSessionResponse, error) {
			return NewSessionResponse{SessionId: "test-session"}, nil
		},
		LoadSessionFunc: func(context.Context, LoadSessionRequest) (LoadSessionResponse, error) {
			return LoadSessionResponse{}, nil
		},
		AuthenticateFunc: func(context.Context, AuthenticateRequest) (AuthenticateResponse, error) {
			return AuthenticateResponse{}, nil
		},
		PromptFunc: func(context.Context, PromptRequest) (PromptResponse, error) {
			return PromptResponse{StopReason: "end_turn"}, nil
		},
		CancelFunc: func(context.Context, CancelNotification) error { return nil },
	}, a2cW, c2aR)

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i, p := range []WriteTextFileRequest{
		{Path: "/file1.txt", Content: "content1", SessionId: "session1"},
		{Path: "/file2.txt", Content: "content2", SessionId: "session1"},
		{Path: "/file3.txt", Content: "content3", SessionId: "session1"},
	} {
		wg.Add(1)
		idx := i
		req := p
		go func() {
			defer wg.Done()
			_, errs[idx] = agentConn.WriteTextFile(context.Background(), req)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}
	mu.Lock()
	got := requestCount
	mu.Unlock()
	if got != 3 {
		t.Fatalf("expected 3 requests, got %d", got)
	}
}

// Test message ordering
func TestConnectionHandlesMessageOrdering(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	var mu sync.Mutex
	var log []string
	push := func(s string) { mu.Lock(); defer mu.Unlock(); log = append(log, s) }

	cs := NewClientSideConnection(&clientFuncs{
		WriteTextFileFunc: func(_ context.Context, req WriteTextFileRequest) (WriteTextFileResponse, error) {
			push("writeTextFile called: " + req.Path)
			return WriteTextFileResponse{}, nil
		},
		ReadTextFileFunc: func(_ context.Context, req ReadTextFileRequest) (ReadTextFileResponse, error) {
			push("readTextFile called: " + req.Path)
			return ReadTextFileResponse{Content: "test content"}, nil
		},
		RequestPermissionFunc: func(_ context.Context, req RequestPermissionRequest) (RequestPermissionResponse, error) {
			title := ""
			if req.ToolCall.Title != nil {
				title = *req.ToolCall.Title
			}
			push("requestPermission called: " + title)
			return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &RequestPermissionOutcomeSelected{OptionId: "allow"}}}, nil
		},
		SessionUpdateFunc: func(context.Context, SessionNotification) error { return nil },
	}, c2aW, a2cR)
	as := NewAgentSideConnection(agentFuncs{
		InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
			return InitializeResponse{ProtocolVersion: ProtocolVersionNumber, AgentCapabilities: AgentCapabilities{LoadSession: false}, AuthMethods: []AuthMethod{}}, nil
		},
		NewSessionFunc: func(_ context.Context, p NewSessionRequest) (NewSessionResponse, error) {
			push("newSession called: " + p.Cwd)
			return NewSessionResponse{SessionId: "test-session"}, nil
		},
		LoadSessionFunc: func(_ context.Context, p LoadSessionRequest) (LoadSessionResponse, error) {
			push("loadSession called: " + string(p.SessionId))
			return LoadSessionResponse{}, nil
		},
		AuthenticateFunc: func(_ context.Context, p AuthenticateRequest) (AuthenticateResponse, error) {
			push("authenticate called: " + string(p.MethodId))
			return AuthenticateResponse{}, nil
		},
		PromptFunc: func(_ context.Context, p PromptRequest) (PromptResponse, error) {
			push("prompt called: " + string(p.SessionId))
			return PromptResponse{StopReason: "end_turn"}, nil
		},
		CancelFunc: func(_ context.Context, p CancelNotification) error {
			push("cancelled called: " + string(p.SessionId))
			return nil
		},
	}, a2cW, c2aR)

	if _, err := cs.NewSession(context.Background(), NewSessionRequest{Cwd: "/test", McpServers: []McpServer{}}); err != nil {
		t.Fatalf("newSession error: %v", err)
	}
	if _, err := as.WriteTextFile(context.Background(), WriteTextFileRequest{Path: "/test.txt", Content: "test", SessionId: "test-session"}); err != nil {
		t.Fatalf("writeTextFile error: %v", err)
	}
	if _, err := as.ReadTextFile(context.Background(), ReadTextFileRequest{Path: "/test.txt", SessionId: "test-session"}); err != nil {
		t.Fatalf("readTextFile error: %v", err)
	}
	if _, err := as.RequestPermission(context.Background(), RequestPermissionRequest{
		SessionId: "test-session",
		ToolCall: ToolCallUpdate{
			Title:      Ptr("Execute command"),
			Kind:       ptr(ToolKindExecute),
			Status:     ptr(ToolCallStatusPending),
			ToolCallId: "tool-123",
			Content:    []ToolCallContent{ToolContent(TextBlock("ls -la"))},
		},
		Options: []PermissionOption{
			{Kind: "allow_once", Name: "Allow", OptionId: "allow"},
			{Kind: "reject_once", Name: "Reject", OptionId: "reject"},
		},
	}); err != nil {
		t.Fatalf("requestPermission error: %v", err)
	}

	expected := []string{
		"newSession called: /test",
		"writeTextFile called: /test.txt",
		"readTextFile called: /test.txt",
		"requestPermission called: Execute command",
	}

	mu.Lock()
	got := append([]string(nil), log...)
	mu.Unlock()
	if len(got) != len(expected) {
		t.Fatalf("log length mismatch: got %d want %d (%v)", len(got), len(expected), got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("log[%d] = %q, want %q", i, got[i], expected[i])
		}
	}
}

// Test notifications
func TestConnectionHandlesNotifications(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	var mu sync.Mutex
	var logs []string
	push := func(s string) { mu.Lock(); logs = append(logs, s); mu.Unlock() }

	clientSide := NewClientSideConnection(&clientFuncs{
		WriteTextFileFunc: func(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error) {
			return WriteTextFileResponse{}, nil
		},
		ReadTextFileFunc: func(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error) {
			return ReadTextFileResponse{Content: "test"}, nil
		},
		RequestPermissionFunc: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
			return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &RequestPermissionOutcomeSelected{OptionId: "allow"}}}, nil
		},
		SessionUpdateFunc: func(_ context.Context, n SessionNotification) error {
			if n.Update.AgentMessageChunk != nil {
				if n.Update.AgentMessageChunk.Content.Text != nil {
					push("agent message: " + n.Update.AgentMessageChunk.Content.Text.Text)
				} else {
					// Fallback to generic message detection
					push("agent message: Hello from agent")
				}
			}
			return nil
		},
	}, c2aW, a2cR)
	agentSide := NewAgentSideConnection(agentFuncs{
		InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
			return InitializeResponse{ProtocolVersion: ProtocolVersionNumber, AgentCapabilities: AgentCapabilities{LoadSession: false}, AuthMethods: []AuthMethod{}}, nil
		},
		NewSessionFunc: func(context.Context, NewSessionRequest) (NewSessionResponse, error) {
			return NewSessionResponse{SessionId: "test-session"}, nil
		},
		LoadSessionFunc: func(context.Context, LoadSessionRequest) (LoadSessionResponse, error) {
			return LoadSessionResponse{}, nil
		},
		AuthenticateFunc: func(context.Context, AuthenticateRequest) (AuthenticateResponse, error) {
			return AuthenticateResponse{}, nil
		},
		PromptFunc: func(context.Context, PromptRequest) (PromptResponse, error) {
			return PromptResponse{StopReason: "end_turn"}, nil
		},
		CancelFunc: func(_ context.Context, p CancelNotification) error {
			push("cancelled: " + string(p.SessionId))
			return nil
		},
	}, a2cW, c2aR)

	if err := agentSide.SessionUpdate(context.Background(), SessionNotification{
		SessionId: "test-session",
		Update: SessionUpdate{
			AgentMessageChunk: &SessionUpdateAgentMessageChunk{
				Content: TextBlock("Hello from agent"),
			},
		},
	}); err != nil {
		t.Fatalf("sessionUpdate error: %v", err)
	}

	if err := clientSide.Cancel(context.Background(), CancelNotification{SessionId: "test-session"}); err != nil {
		t.Fatalf("cancel error: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	got := append([]string(nil), logs...)
	mu.Unlock()
	want1, want2 := "agent message: Hello from agent", "cancelled: test-session"
	if !slices.Contains(got, want1) || !slices.Contains(got, want2) {
		t.Fatalf("notification logs mismatch: %v", got)
	}
}

// Test initialize method behavior
func TestConnectionHandlesInitialize(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	agentConn := NewClientSideConnection(&clientFuncs{
		WriteTextFileFunc: func(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error) {
			return WriteTextFileResponse{}, nil
		},
		ReadTextFileFunc: func(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error) {
			return ReadTextFileResponse{Content: "test"}, nil
		},
		RequestPermissionFunc: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
			return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &RequestPermissionOutcomeSelected{OptionId: "allow"}}}, nil
		},
		SessionUpdateFunc: func(context.Context, SessionNotification) error { return nil },
	}, c2aW, a2cR)
	_ = NewAgentSideConnection(agentFuncs{
		InitializeFunc: func(_ context.Context, p InitializeRequest) (InitializeResponse, error) {
			return InitializeResponse{
				ProtocolVersion: p.ProtocolVersion,
				AgentCapabilities: AgentCapabilities{
					LoadSession: true,
				},
				AuthMethods: []AuthMethod{
					{
						Id:          "oauth",
						Name:        "OAuth",
						Description: Ptr("Authenticate with OAuth"),
					},
				},
			}, nil
		},
		NewSessionFunc: func(context.Context, NewSessionRequest) (NewSessionResponse, error) {
			return NewSessionResponse{SessionId: "test-session"}, nil
		},
		LoadSessionFunc: func(context.Context, LoadSessionRequest) (LoadSessionResponse, error) {
			return LoadSessionResponse{}, nil
		},
		AuthenticateFunc: func(context.Context, AuthenticateRequest) (AuthenticateResponse, error) {
			return AuthenticateResponse{}, nil
		},
		PromptFunc: func(context.Context, PromptRequest) (PromptResponse, error) {
			return PromptResponse{StopReason: "end_turn"}, nil
		},
		CancelFunc: func(context.Context, CancelNotification) error { return nil },
	}, a2cW, c2aR)

	resp, err := agentConn.Initialize(context.Background(), InitializeRequest{
		ProtocolVersion:    ProtocolVersionNumber,
		ClientCapabilities: ClientCapabilities{Fs: FileSystemCapability{ReadTextFile: false, WriteTextFile: false}},
	})
	if err != nil {
		t.Fatalf("initialize error: %v", err)
	}
	if resp.ProtocolVersion != ProtocolVersionNumber {
		t.Fatalf("protocol version mismatch: got %d want %d", resp.ProtocolVersion, ProtocolVersionNumber)
	}
	if !resp.AgentCapabilities.LoadSession {
		t.Fatalf("expected loadSession true")
	}
	if len(resp.AuthMethods) != 1 || resp.AuthMethods[0].Id != "oauth" {
		t.Fatalf("unexpected authMethods: %+v", resp.AuthMethods)
	}
}

func ptr[T any](t T) *T {
	return &t
}

// Test that canceling the client's Prompt context sends a session/cancel
// to the agent, and that the connection remains usable afterwards.
func TestPromptCancellationSendsCancelAndAllowsNewSession(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	cancelCh := make(chan string, 1)
	promptDone := make(chan struct{}, 1)

	// Agent side: Prompt waits for ctx cancellation; Cancel records the sessionId
	_ = NewAgentSideConnection(agentFuncs{
		InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
			return InitializeResponse{ProtocolVersion: ProtocolVersionNumber}, nil
		},
		NewSessionFunc: func(context.Context, NewSessionRequest) (NewSessionResponse, error) {
			return NewSessionResponse{SessionId: "s-1"}, nil
		},
		LoadSessionFunc: func(context.Context, LoadSessionRequest) (LoadSessionResponse, error) {
			return LoadSessionResponse{}, nil
		},
		AuthenticateFunc: func(context.Context, AuthenticateRequest) (AuthenticateResponse, error) {
			return AuthenticateResponse{}, nil
		},
		PromptFunc: func(ctx context.Context, p PromptRequest) (PromptResponse, error) {
			<-ctx.Done()
			// mark that prompt finished due to cancellation
			select {
			case promptDone <- struct{}{}:
			default:
			}
			return PromptResponse{StopReason: StopReasonCancelled}, nil
		},
		CancelFunc: func(context.Context, CancelNotification) error {
			select {
			case cancelCh <- "s-1":
			default:
			}
			return nil
		},
	}, a2cW, c2aR)

	// Client side
	cs := NewClientSideConnection(&clientFuncs{
		WriteTextFileFunc: func(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error) {
			return WriteTextFileResponse{}, nil
		},
		ReadTextFileFunc: func(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error) {
			return ReadTextFileResponse{Content: ""}, nil
		},
		RequestPermissionFunc: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
			return RequestPermissionResponse{}, nil
		},
		SessionUpdateFunc: func(context.Context, SessionNotification) error { return nil },
	}, c2aW, a2cR)

	// Initialize and create a session
	if _, err := cs.Initialize(context.Background(), InitializeRequest{ProtocolVersion: ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sess, err := cs.NewSession(context.Background(), NewSessionRequest{Cwd: "/", McpServers: []McpServer{}})
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	// Start a prompt with a cancelable context, then cancel it
	turnCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := cs.Prompt(turnCtx, PromptRequest{SessionId: sess.SessionId, Prompt: []ContentBlock{TextBlock("hello")}})
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	// Expect a session/cancel notification on the agent side
	select {
	case sid := <-cancelCh:
		if sid != string(sess.SessionId) && sid != "s-1" { // allow either depending on agent NewSession response
			t.Fatalf("unexpected cancel session id: %q", sid)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for session/cancel")
	}

	// Agent's prompt should have finished due to ctx cancellation
	select {
	case <-promptDone:
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for prompt to finish after cancel")
	}

	// Connection remains usable: create another session
	if _, err := cs.NewSession(context.Background(), NewSessionRequest{Cwd: "/", McpServers: []McpServer{}}); err != nil {
		t.Fatalf("newSession after cancel: %v", err)
	}
}

// TestPromptWaitsForSessionUpdatesComplete verifies that Prompt() waits for all SessionUpdate
// notification handlers to complete before returning.  This ensures that when a server sends
// SessionUpdate notifications followed by a PromptResponse, the client-side Prompt() call will not
// return until all notification handlers have finished processing.  This is the expected semantic
// contract: the prompt operation includes all its updates.
func TestPromptWaitsForSessionUpdatesComplete(t *testing.T) {
	const numUpdates = 10
	const handlerDelay = 50 * time.Millisecond

	var (
		updateStarted   atomic.Int64
		updateCompleted atomic.Int64
	)

	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	// Client side with SessionUpdate handler that tracks execution
	c := NewClientSideConnection(&clientFuncs{
		WriteTextFileFunc: func(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error) {
			return WriteTextFileResponse{}, nil
		},
		ReadTextFileFunc: func(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error) {
			return ReadTextFileResponse{Content: "test"}, nil
		},
		RequestPermissionFunc: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
			return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &RequestPermissionOutcomeSelected{OptionId: "allow"}}}, nil
		},
		SessionUpdateFunc: func(_ context.Context, n SessionNotification) error {
			updateStarted.Add(1)
			// Simulate processing time
			time.Sleep(handlerDelay)
			updateCompleted.Add(1)
			return nil
		},
	}, c2aW, a2cR)

	// Agent side that sends multiple SessionUpdate notifications before responding
	var wg sync.WaitGroup
	wg.Add(1)

	var ag *AgentSideConnection
	ag = NewAgentSideConnection(agentFuncs{
		InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
			return InitializeResponse{ProtocolVersion: ProtocolVersionNumber, AgentCapabilities: AgentCapabilities{LoadSession: false}, AuthMethods: []AuthMethod{}}, nil
		},
		NewSessionFunc: func(context.Context, NewSessionRequest) (NewSessionResponse, error) {
			return NewSessionResponse{SessionId: "test-session"}, nil
		},
		LoadSessionFunc: func(context.Context, LoadSessionRequest) (LoadSessionResponse, error) {
			return LoadSessionResponse{}, nil
		},
		AuthenticateFunc: func(context.Context, AuthenticateRequest) (AuthenticateResponse, error) {
			return AuthenticateResponse{}, nil
		},
		PromptFunc: func(ctx context.Context, p PromptRequest) (PromptResponse, error) {
			defer wg.Done()

			// Send multiple SessionUpdate notifications
			for i := 0; i < numUpdates; i++ {
				_ = ag.SessionUpdate(ctx, SessionNotification{
					SessionId: p.SessionId,
					Update: SessionUpdate{
						AgentMessageChunk: &SessionUpdateAgentMessageChunk{
							Content: TextBlock("chunk"),
						},
					},
				})
			}

			// Small delay to ensure notifications are queued
			time.Sleep(10 * time.Millisecond)

			// Return response (this will unblock client's Prompt() call)
			return PromptResponse{StopReason: "end_turn"}, nil
		},
		CancelFunc: func(context.Context, CancelNotification) error { return nil },
	}, a2cW, c2aR)

	if _, err := c.Initialize(context.Background(), InitializeRequest{ProtocolVersion: ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sess, err := c.NewSession(context.Background(), NewSessionRequest{Cwd: "/", McpServers: []McpServer{}})
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	_, err = c.Prompt(context.Background(), PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []ContentBlock{TextBlock("test")},
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	wg.Wait()

	// Verify the expected behavior: at this point, Prompt() has returned, and all SessionUpdate
	// handlers should have completed their processing.
	// started := updateStarted.Load()    ; Currently unsused but useful for debugging
	completed := updateCompleted.Load()

	// ASSERT: when Prompt() returns, all SessionUpdate notifications that were sent
	// before the PromptResponse must have been fully processed. This is the semantic
	// contract: the prompt operation includes all its updates.
	if completed != numUpdates {
		t.Fatalf("Prompt() returned with only %d/%d SessionUpdate "+
			"handlers completed. Expected all handlers to complete before Prompt() "+
			"returns.", completed, numUpdates)
	}
}

// TestRequestHandlerCanMakeNestedRequest verifies that a request handler can make nested
// requests without deadlocking (e.g., Prompt handler calling RequestPermission).
func TestRequestHandlerCanMakeNestedRequest(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	c := NewClientSideConnection(&clientFuncs{
		WriteTextFileFunc: func(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error) {
			return WriteTextFileResponse{}, nil
		},
		ReadTextFileFunc: func(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error) {
			return ReadTextFileResponse{Content: "test"}, nil
		},
		RequestPermissionFunc: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
			return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &RequestPermissionOutcomeSelected{OptionId: "allow"}}}, nil
		},
		SessionUpdateFunc: func(context.Context, SessionNotification) error {
			return nil
		},
	}, c2aW, a2cR)

	var ag *AgentSideConnection
	ag = NewAgentSideConnection(agentFuncs{
		InitializeFunc: func(context.Context, InitializeRequest) (InitializeResponse, error) {
			return InitializeResponse{ProtocolVersion: ProtocolVersionNumber, AgentCapabilities: AgentCapabilities{LoadSession: false}, AuthMethods: []AuthMethod{}}, nil
		},
		NewSessionFunc: func(context.Context, NewSessionRequest) (NewSessionResponse, error) {
			return NewSessionResponse{SessionId: "test-session"}, nil
		},
		LoadSessionFunc: func(context.Context, LoadSessionRequest) (LoadSessionResponse, error) {
			return LoadSessionResponse{}, nil
		},
		AuthenticateFunc: func(context.Context, AuthenticateRequest) (AuthenticateResponse, error) {
			return AuthenticateResponse{}, nil
		},
		PromptFunc: func(ctx context.Context, p PromptRequest) (PromptResponse, error) {
			_, err := ag.RequestPermission(ctx, RequestPermissionRequest{
				SessionId: p.SessionId,
				ToolCall: ToolCallUpdate{
					ToolCallId: "call_1",
					Title:      Ptr("Test permission"),
				},
				Options: []PermissionOption{
					{Kind: PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"},
				},
			})
			if err != nil {
				return PromptResponse{}, err
			}
			return PromptResponse{StopReason: "end_turn"}, nil
		},
		CancelFunc: func(context.Context, CancelNotification) error { return nil },
	}, a2cW, c2aR)

	if _, err := c.Initialize(context.Background(), InitializeRequest{ProtocolVersion: ProtocolVersionNumber}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	sess, err := c.NewSession(context.Background(), NewSessionRequest{Cwd: "/", McpServers: []McpServer{}})
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := c.Prompt(ctx, PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []ContentBlock{TextBlock("test")},
	}); err != nil {
		t.Fatalf("prompt failed: %v", err)
	}
}

type extEchoParams struct {
	Msg string `json:"msg"`
}

type extEchoResult struct {
	Msg string `json:"msg"`
}

type agentNoExtensions struct{}

func (agentNoExtensions) Authenticate(ctx context.Context, params AuthenticateRequest) (AuthenticateResponse, error) {
	return AuthenticateResponse{}, nil
}

func (agentNoExtensions) Initialize(ctx context.Context, params InitializeRequest) (InitializeResponse, error) {
	return InitializeResponse{}, nil
}

func (agentNoExtensions) Cancel(ctx context.Context, params CancelNotification) error { return nil }

func (agentNoExtensions) NewSession(ctx context.Context, params NewSessionRequest) (NewSessionResponse, error) {
	return NewSessionResponse{}, nil
}

func (agentNoExtensions) Prompt(ctx context.Context, params PromptRequest) (PromptResponse, error) {
	return PromptResponse{}, nil
}

func (agentNoExtensions) SetSessionMode(ctx context.Context, params SetSessionModeRequest) (SetSessionModeResponse, error) {
	return SetSessionModeResponse{}, nil
}

func TestExtensionMethods_ClientToAgentRequest(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	method := "_vendor.test/echo"

	ag := NewAgentSideConnection(agentFuncs{
		HandleExtensionMethodFunc: func(ctx context.Context, gotMethod string, params json.RawMessage) (any, error) {
			if gotMethod != method {
				return nil, NewInternalError(map[string]any{"expected": method, "got": gotMethod})
			}
			var p extEchoParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			return extEchoResult{Msg: p.Msg}, nil
		},
	}, a2cW, c2aR)

	_ = ag

	c := NewClientSideConnection(&clientFuncs{}, c2aW, a2cR)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	raw, err := c.CallExtension(ctx, method, extEchoParams{Msg: "hi"})
	if err != nil {
		t.Fatalf("CallExtension: %v", err)
	}
	var resp extEchoResult
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Msg != "hi" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestExtensionMethods_UnknownRequest_ReturnsMethodNotFound(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	NewAgentSideConnection(agentNoExtensions{}, a2cW, c2aR)
	c := NewClientSideConnection(&clientFuncs{}, c2aW, a2cR)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := c.CallExtension(ctx, "_vendor.test/missing", extEchoParams{Msg: "hi"})
	if err == nil {
		t.Fatalf("expected error")
	}
	var re *RequestError
	if !errors.As(err, &re) {
		t.Fatalf("expected *RequestError, got %T: %v", err, err)
	}
	if re.Code != -32601 {
		t.Fatalf("expected -32601 method not found, got %d", re.Code)
	}
}

func TestExtensionMethods_UnknownNotification_DoesNotLog(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	done := make(chan struct{})

	ag := NewAgentSideConnection(agentFuncs{
		HandleExtensionMethodFunc: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			close(done)
			return nil, NewMethodNotFound(method)
		},
	}, a2cW, c2aR)

	var logBuf bytes.Buffer
	ag.SetLogger(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	c := NewClientSideConnection(&clientFuncs{}, c2aW, a2cR)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if err := c.NotifyExtension(ctx, "_vendor.test/notify", map[string]any{"hello": "world"}); err != nil {
		t.Fatalf("NotifyExtension: %v", err)
	}

	select {
	case <-done:
		// ok
	case <-ctx.Done():
		t.Fatalf("timeout waiting for notification handler")
	}

	if strings.Contains(logBuf.String(), "failed to handle notification") {
		t.Fatalf("unexpected notification error log: %s", logBuf.String())
	}
}

func TestExtensionMethods_AgentToClientRequest(t *testing.T) {
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	method := "_vendor.test/echo"

	_ = NewClientSideConnection(&clientFuncs{
		HandleExtensionMethodFunc: func(ctx context.Context, gotMethod string, params json.RawMessage) (any, error) {
			if gotMethod != method {
				return nil, NewInternalError(map[string]any{"expected": method, "got": gotMethod})
			}
			var p extEchoParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			return extEchoResult{Msg: p.Msg}, nil
		},
	}, c2aW, a2cR)

	ag := NewAgentSideConnection(agentFuncs{}, a2cW, c2aR)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	raw, err := ag.CallExtension(ctx, method, extEchoParams{Msg: "hi"})
	if err != nil {
		t.Fatalf("CallExtension: %v", err)
	}
	var resp extEchoResult
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Msg != "hi" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
