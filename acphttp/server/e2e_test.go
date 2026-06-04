package httpserver_test

// End-to-end: drive the server via the client transport through a real
// SDK ClientSideConnection. Kept in a separate _test package to avoid
// entangling the two sub-packages' import graphs.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/coder/acp-go-sdk/acphttp/client"
	"github.com/coder/acp-go-sdk/acphttp/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// endToEndAgent is a pared-down stub for the cross-package test.
type endToEndAgent struct {
	conn           *acp.AgentSideConnection
	sessionCounter atomic.Uint64
}

func (a *endToEndAgent) Initialize(ctx context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "e2e-stub", Version: "0.0.1"},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			PromptCapabilities: acp.PromptCapabilities{
				Image:           false,
				Audio:           false,
				EmbeddedContext: true,
			},
		},
	}, nil
}

func (a *endToEndAgent) Authenticate(ctx context.Context, req acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *endToEndAgent) Logout(ctx context.Context, req acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

func (a *endToEndAgent) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	n := a.sessionCounter.Add(1)
	return acp.NewSessionResponse{SessionId: acp.SessionId(fmt.Sprintf("sess-%d", n))}, nil
}

func (a *endToEndAgent) LoadSession(ctx context.Context, req acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	return acp.LoadSessionResponse{}, nil
}

func (a *endToEndAgent) ResumeSession(ctx context.Context, req acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}

func (a *endToEndAgent) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}

func (a *endToEndAgent) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	// One streamed update + one final response.
	if a.conn != nil {
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: req.SessionId,
			Update: acp.SessionUpdate{
				AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "hi"}},
				},
			},
		})
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *endToEndAgent) Cancel(ctx context.Context, params acp.CancelNotification) error {
	return nil
}

func (a *endToEndAgent) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}

func (a *endToEndAgent) ListSessions(ctx context.Context, params acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}

func (a *endToEndAgent) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

// endToEndClient satisfies the minimal acp.Client surface needed for
// the client-side SDK connection: we don't care about fs callbacks here.
type endToEndClient struct {
	updates atomic.Int64
}

func (c *endToEndClient) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{}, nil
}

func (c *endToEndClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	c.updates.Add(1)
	return nil
}

func (c *endToEndClient) ReadTextFile(ctx context.Context, req acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, nil
}

func (c *endToEndClient) WriteTextFile(ctx context.Context, req acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, nil
}

func (c *endToEndClient) CreateTerminal(ctx context.Context, req acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, nil
}
func (c *endToEndClient) TerminalOutput(ctx context.Context, req acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, nil
}
func (c *endToEndClient) ReleaseTerminal(ctx context.Context, req acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, nil
}
func (c *endToEndClient) WaitForTerminalExit(ctx context.Context, req acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, nil
}
func (c *endToEndClient) KillTerminal(ctx context.Context, req acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, nil
}

// TestEndToEnd_ClientTalksToServer wires the SDK on both sides of a real
// HTTP loopback: the client Transport drives the server over Streamable
// HTTP, and a full prompt turn round-trips.
func TestEndToEnd_ClientTalksToServer(t *testing.T) {
	var agent *endToEndAgent
	srv, err := httpserver.New(httpserver.Config{
		Factory: func(ctx context.Context) (acp.Agent, func(*acp.AgentSideConnection), func(), error) {
			agent = &endToEndAgent{}
			return agent, func(c *acp.AgentSideConnection) { agent.conn = c }, nil, nil
		},
	})
	require.NoError(t, err)
	defer srv.Close()

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	// --- client side ---
	tr, err := httpclient.Dial(context.Background(), httpclient.Config{BaseURL: httpSrv.URL + "/acp"})
	require.NoError(t, err)
	defer tr.Close()

	clientImpl := &endToEndClient{}
	conn := acp.NewClientSideConnection(clientImpl, tr, tr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	initResp, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientInfo:      &acp.Implementation{Name: "e2e", Version: "0"},
	})
	require.NoError(t, err)
	require.NotNil(t, initResp.AgentInfo)
	assert.Equal(t, "e2e-stub", initResp.AgentInfo.Name)

	newResp, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        "/tmp",
		McpServers: []acp.McpServer{},
	})
	require.NoError(t, err)
	assert.Equal(t, acp.SessionId("sess-1"), newResp.SessionId)

	promptResp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: newResp.SessionId,
		Prompt: []acp.ContentBlock{
			{Text: &acp.ContentBlockText{Text: "hello"}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, acp.StopReasonEndTurn, promptResp.StopReason)

	// Wait for the session/update to propagate before asserting; the SDK
	// delivers notifications asynchronously relative to the prompt
	// response.
	require.Eventually(t, func() bool {
		return clientImpl.updates.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond, "expected session/update to reach the client")
}
