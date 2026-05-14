// example/agent-http serves the ACP demo agent over HTTP using the
// Streamable HTTP transport in github.com/coder/acp-go-sdk/acphttp/server.
//
// Run it standalone:
//
//	go run ./example/agent-http -listen 127.0.0.1:7777
//
// Then connect with ./example/client-http (in another terminal) or any
// other Streamable HTTP ACP client:
//
//	go run ./example/client-http -url http://127.0.0.1:7777/acp
//
// The agent itself behaves like example/agent: it streams a short demo
// turn with one tool call and one permission request. The interesting
// piece here is the wiring — a single Server hosts many independent
// connections, each with its own per-connection Agent produced by the
// Factory callback.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
	httpserver "github.com/coder/acp-go-sdk/acphttp/server"
)

// demoAgent is a minimal acp.Agent that simulates a short prompt turn
// with a streamed message, one tool call, and one permission request.
// It mirrors example/agent but trimmed down to focus on the HTTP wiring.
type demoAgent struct {
	conn *acp.AgentSideConnection

	mu       sync.Mutex
	sessions map[string]context.CancelFunc
}

var _ acp.Agent = (*demoAgent)(nil)

func newDemoAgent() *demoAgent {
	return &demoAgent{sessions: make(map[string]context.CancelFunc)}
}

func (a *demoAgent) Initialize(ctx context.Context, _ acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "acp-go-sdk/example/agent-http", Version: "0.1.0"},
		AgentCapabilities: acp.AgentCapabilities{
			PromptCapabilities: acp.PromptCapabilities{EmbeddedContext: true},
		},
	}, nil
}

func (a *demoAgent) Authenticate(ctx context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *demoAgent) NewSession(ctx context.Context, _ acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	sid := "sess_" + randomID()
	a.mu.Lock()
	a.sessions[sid] = nil
	a.mu.Unlock()
	return acp.NewSessionResponse{SessionId: acp.SessionId(sid)}, nil
}

func (a *demoAgent) LoadSession(ctx context.Context, _ acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	return acp.LoadSessionResponse{}, nil
}

func (a *demoAgent) ResumeSession(ctx context.Context, _ acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

func (a *demoAgent) ListSessions(ctx context.Context, _ acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

func (a *demoAgent) CloseSession(ctx context.Context, _ acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionClose)
}

func (a *demoAgent) SetSessionMode(ctx context.Context, _ acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func (a *demoAgent) SetSessionConfigOption(ctx context.Context, _ acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

func (a *demoAgent) Cancel(ctx context.Context, params acp.CancelNotification) error {
	a.mu.Lock()
	cancel, ok := a.sessions[string(params.SessionId)]
	a.mu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
	return nil
}

func (a *demoAgent) Prompt(_ context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	sid := string(params.SessionId)

	a.mu.Lock()
	if _, ok := a.sessions[sid]; !ok {
		a.mu.Unlock()
		return acp.PromptResponse{}, fmt.Errorf("session %s not found", sid)
	}
	// Cancel any previous turn for this session before starting a new one.
	if prev := a.sessions[sid]; prev != nil {
		prev()
	}
	turnCtx, cancel := context.WithCancel(context.Background())
	a.sessions[sid] = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		// Only clear if it's still the current cancel; a concurrent Cancel may
		// already have rotated it.
		if a.sessions[sid] != nil {
			a.sessions[sid] = nil
		}
		a.mu.Unlock()
	}()

	if err := a.simulateTurn(turnCtx, params.SessionId); err != nil {
		if turnCtx.Err() != nil {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

// simulateTurn streams a short scripted interaction. It demonstrates the
// three kinds of server→client traffic that flow over a session-scoped
// SSE stream: notifications (session/update), a server-initiated
// request (session/request_permission) that needs a reply from the
// client, and the eventual response to session/prompt itself.
func (a *demoAgent) simulateTurn(ctx context.Context, sid acp.SessionId) error {
	steps := []acp.SessionUpdate{
		acp.UpdateAgentMessageText("ACP Go Example Agent (HTTP transport) — demo only."),
		acp.UpdateAgentMessageText("Let me read the project README to get started…"),
		acp.StartToolCall(
			acp.ToolCallId("call_1"),
			"Reading project files",
			acp.WithStartKind(acp.ToolKindRead),
			acp.WithStartStatus(acp.ToolCallStatusPending),
			acp.WithStartLocations([]acp.ToolCallLocation{{Path: "/project/README.md"}}),
		),
		acp.UpdateToolCall(
			acp.ToolCallId("call_1"),
			acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
			acp.WithUpdateContent([]acp.ToolCallContent{
				acp.ToolContent(acp.TextBlock("# My Project\n\nThis is a sample project.")),
			}),
		),
		acp.UpdateAgentMessageText("Now I'd like to update the configuration."),
	}
	for _, u := range steps {
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: u}); err != nil {
			return err
		}
		if err := pause(ctx, 400*time.Millisecond); err != nil {
			return err
		}
	}

	// Request permission for a sensitive operation — the response
	// travels back over the session-scoped stream.
	permResp, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: sid,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("call_2"),
			Title:      acp.Ptr("Modifying critical configuration file"),
			Kind:       acp.Ptr(acp.ToolKindEdit),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
			Locations:  []acp.ToolCallLocation{{Path: "/project/config.json"}},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow this change", OptionId: "allow"},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Skip this change", OptionId: "reject"},
		},
	})
	if err != nil {
		return err
	}

	var reply string
	switch {
	case permResp.Outcome.Cancelled != nil:
		reply = "Permission request cancelled — leaving the configuration alone."
	case permResp.Outcome.Selected != nil && permResp.Outcome.Selected.OptionId == "allow":
		reply = "Configuration updated. Have a nice day!"
	default:
		reply = "Skipping the configuration change as requested."
	}
	return a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: sid,
		Update:    acp.UpdateAgentMessageText(reply),
	})
}

func main() {
	listen := flag.String("listen", "127.0.0.1:7777", "host:port to listen on")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Each new ACP connection (one per remote client) gets a fresh
	// demoAgent. The bindConn callback wires the SDK-level
	// AgentSideConnection back into the agent so it can issue
	// server→client calls (SessionUpdate, RequestPermission).
	factory := func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
		ag := newDemoAgent()
		return ag, func(c *acp.AgentSideConnection) { ag.conn = c }, nil, nil
	}

	srv, err := httpserver.New(httpserver.Config{
		Factory: factory,
		Logger:  logger,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "create server: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logger.Info("listening", "addr", *listen, "endpoint", "/acp")
	if err := srv.ListenAndServe(ctx, *listen); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}

func randomID() string {
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
