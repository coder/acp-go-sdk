package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
)

// The prompt texts the tests start the agent with, as run.sh would.
var testPrompts = prompts{cancel: "slow", subagent: "delegate", subagentCancel: "delegate, cancellable"}

// client records the agent's session updates and answers permission
// requests with permit.
type client struct {
	mu      sync.Mutex
	updates []acp.SessionNotification
	changed chan struct{}
	permit  acp.RequestPermissionOutcome
}

func (c *client) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, n)
	c.mu.Unlock()
	select {
	case c.changed <- struct{}{}:
	default:
	}
	return nil
}

func (c *client) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return acp.RequestPermissionResponse{Outcome: c.permit}, nil
}

// waitFor returns the first update that ok accepts, waiting for it.
func (c *client) waitFor(t *testing.T, ok func(acp.SessionNotification) bool) acp.SessionNotification {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		c.mu.Lock()
		for _, u := range c.updates {
			if ok(u) {
				c.mu.Unlock()
				return u
			}
		}
		c.mu.Unlock()
		select {
		case <-c.changed:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatal("timed out waiting for a session update")
		}
	}
}

func (c *client) CompleteElicitation(context.Context, acp.CompleteElicitationNotification) error {
	return nil
}

func (c *client) CreateElicitation(context.Context, acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error) {
	return acp.CreateElicitationResponse{}, acp.NewMethodNotFound("elicitation/create")
}

func (c *client) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound("fs/read_text_file")
}

func (c *client) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound("fs/write_text_file")
}

func (c *client) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound("terminal/create")
}

func (c *client) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound("terminal/kill")
}

func (c *client) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound("terminal/output")
}

func (c *client) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound("terminal/release")
}

func (c *client) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound("terminal/wait_for_exit")
}

func (c *client) UnstableAuthStatusUpdate(context.Context, acp.UnstableAuthStatusUpdateNotification) error {
	return nil
}

// connect starts the agent on pipes and initializes it with caps.
func connect(t *testing.T, caps acp.ClientCapabilities) (*acp.ClientSideConnection, *client) {
	t.Helper()
	toAgent, fromClient := io.Pipe()
	toClient, fromAgent := io.Pipe()
	a := newAgent(testPrompts)
	a.connect(acp.NewAgentSideConnection(a, fromAgent, toAgent))
	c := &client{changed: make(chan struct{}, 1), permit: acp.NewRequestPermissionOutcomeCancelled()}
	conn := acp.NewClientSideConnection(c, fromClient, toClient)
	t.Cleanup(func() {
		_ = fromClient.Close()
		_ = fromAgent.Close()
		a.shutdown()
	})
	if _, err := conn.Initialize(context.Background(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber, ClientCapabilities: caps}); err != nil {
		t.Fatal(err)
	}
	return conn, c
}

func newSession(t *testing.T, conn *acp.ClientSideConnection, cwd string) acp.SessionId {
	t.Helper()
	resp, err := conn.NewSession(context.Background(), acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.SessionId
}

// wantCode checks that err is a JSON-RPC error with code, and with the
// session id in its data when sid is set.
func wantCode(t *testing.T, what string, err error, code int, sid acp.SessionId) {
	t.Helper()
	var re *acp.RequestError
	if !errors.As(err, &re) || re.Code != code {
		t.Errorf("%s: got %v, want code %d", what, err, code)
		return
	}
	if sid == "" {
		return
	}
	var data struct {
		SessionID acp.SessionId `json:"sessionId"`
	}
	if json.Unmarshal(re.Data, &data) != nil || data.SessionID != sid {
		t.Errorf("%s: data %s, want sessionId %q", what, re.Data, sid)
	}
}

func text(s string) []acp.ContentBlock { return []acp.ContentBlock{acp.TextBlock(s)} }

func TestAgent_UnknownAndClosedSessions(t *testing.T) {
	ctx := context.Background()
	conn, _ := connect(t, acp.ClientCapabilities{})
	const unknown acp.SessionId = "never-created"

	_, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: unknown, Prompt: text("hi")})
	wantCode(t, "prompt", err, -32002, unknown)
	_, err = conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: unknown, ModeId: modeCode})
	wantCode(t, "set_mode", err, -32002, unknown)
	_, err = conn.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: unknown, Cwd: t.TempDir()})
	wantCode(t, "resume", err, -32002, unknown)
	_, err = conn.UnstableArchiveSession(ctx, acp.UnstableArchiveSessionRequest{SessionId: unknown})
	wantCode(t, "archive", err, -32002, unknown)
	if _, err := conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: unknown}); err != nil {
		t.Errorf("close of an unknown session: %v, want success", err)
	}
	if _, err := conn.DeleteSession(ctx, acp.DeleteSessionRequest{SessionId: unknown}); err != nil {
		t.Errorf("delete of an unknown session: %v, want success", err)
	}

	sid := newSession(t, conn, t.TempDir())
	if _, err := conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: sid}); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: text("hi")})
	wantCode(t, "prompt after close", err, -32002, sid)
	if _, err := conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: sid}); err != nil {
		t.Errorf("second close: %v, want success", err)
	}
}

func TestAgent_InvalidParams(t *testing.T) {
	ctx := context.Background()
	conn, _ := connect(t, acp.ClientCapabilities{})
	sid := newSession(t, conn, t.TempDir())

	_, err := conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: sid, ModeId: "no-such-mode"})
	wantCode(t, "unknown mode", err, -32602, "")
	_, err = conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: sid, ConfigId: configModel, Value: "no-such-model"}})
	wantCode(t, "unknown value", err, -32602, "")
	_, err = conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{Boolean: &acp.SetSessionConfigOptionBoolean{SessionId: sid, ConfigId: configWebSearch, Type: "boolean", Value: true}})
	wantCode(t, "boolean option the client did not accept", err, -32602, "")
	_, err = conn.ListSessions(ctx, acp.ListSessionsRequest{Cursor: acp.Ptr("never-issued")})
	wantCode(t, "list with a cursor", err, -32602, "")
	_, err = conn.ListSessions(ctx, acp.ListSessionsRequest{Cwd: acp.Ptr("relative/dir")})
	wantCode(t, "list with a relative cwd", err, -32602, "")
	_, err = conn.UnstableSessionOptions(ctx, acp.UnstableSessionOptionsRequest{Cwd: "relative/dir"})
	wantCode(t, "options for a relative cwd", err, -32602, "")
	_, err = conn.UnstableSessionOptions(ctx, acp.UnstableSessionOptionsRequest{Cwd: t.TempDir(), AdditionalDirectories: []string{"relative/dir"}})
	wantCode(t, "options for a relative additional directory", err, -32602, "")
	_, err = conn.LoadSession(ctx, acp.LoadSessionRequest{SessionId: sid, Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	wantCode(t, "load with another cwd", err, -32602, "")
	_, err = conn.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: sid, Cwd: t.TempDir()})
	wantCode(t, "resume with another cwd", err, -32602, "")
	_, err = conn.UnstableSessionOptions(ctx, acp.UnstableSessionOptionsRequest{SessionId: &sid, Cwd: t.TempDir()})
	wantCode(t, "options for a session with another cwd", err, -32602, "")
	_, err = conn.UnstableForkSession(ctx, acp.UnstableForkSessionRequest{SessionId: sid, Cwd: t.TempDir(), McpServers: []acp.McpServer{}, CursorId: acp.Ptr(acp.CursorId("never-issued"))})
	wantCode(t, "fork at a cursor", err, -32602, "")
	_, err = conn.ListSessions(ctx, acp.ListSessionsRequest{Archived: acp.Ptr(acp.SessionArchiveFilter("sometimes"))})
	wantCode(t, "unknown archived filter", err, -32602, "")
	_, err = conn.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}, AdditionalDirectories: []string{"relative/dir"}})
	wantCode(t, "relative additional directory", err, -32602, "")
}

// TestAgent_ReopenReplacesAdditionalDirectories checks that load and resume
// take the request's additionalDirectories as the whole list, so omitting it
// clears the list.
func TestAgent_ReopenReplacesAdditionalDirectories(t *testing.T) {
	ctx := context.Background()
	conn, _ := connect(t, acp.ClientCapabilities{})
	cwd, extra := t.TempDir(), t.TempDir()
	resp, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}, AdditionalDirectories: []string{extra}})
	if err != nil {
		t.Fatal(err)
	}
	dirs := func() []string {
		t.Helper()
		list, err := conn.ListSessions(ctx, acp.ListSessionsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range list.Sessions {
			if s.SessionId == resp.SessionId {
				return s.AdditionalDirectories
			}
		}
		t.Fatalf("session %s is not listed", resp.SessionId)
		return nil
	}
	if got := dirs(); len(got) != 1 || got[0] != extra {
		t.Fatalf("SETUP: additionalDirectories %v, want [%s]", got, extra)
	}
	if _, err := conn.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: resp.SessionId, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if got := dirs(); len(got) != 0 {
		t.Errorf("after a resume without additionalDirectories the session lists %v, want none", got)
	}
}

// TestAgent_LogoutRequiresAuthentication checks the logout contract: after a
// logout every new session needs authentication again.
func TestAgent_LogoutRequiresAuthentication(t *testing.T) {
	ctx := context.Background()
	conn, _ := connect(t, acp.ClientCapabilities{})
	sid := newSession(t, conn, t.TempDir())
	if _, err := conn.Logout(ctx, acp.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	wantCode(t, "new session after logout", err, -32000, "")
	_, err = conn.UnstableForkSession(ctx, acp.UnstableForkSessionRequest{SessionId: sid, Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	wantCode(t, "fork after logout", err, -32000, "")
	if _, err := conn.Authenticate(ctx, acp.AuthenticateRequest{MethodId: authMethod}); err != nil {
		t.Fatal(err)
	}
	newSession(t, conn, t.TempDir())
}

// TestMCPServer_StopKillsAServerThatIgnoresEOF stops a stdio server that does
// not read its stdin: the agent must kill it after mcpStopGrace.
func TestMCPServer_StopKillsAServerThatIgnoresEOF(t *testing.T) {
	m, err := startStdio(acp.McpServerStdio{Name: "hung", Command: "sleep", Args: []string{"60"}, Env: []acp.EnvVariable{}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.stop():
	case <-time.After(mcpStopGrace + 3*time.Second):
		_ = m.cmd.Process.Kill()
		t.Fatal("the server still runs after mcpStopGrace")
	}
}

func TestAgent_ListShowsArchiveViews(t *testing.T) {
	ctx := context.Background()
	conn, _ := connect(t, acp.ClientCapabilities{})
	cwd := t.TempDir()
	kept, archived := newSession(t, conn, cwd), newSession(t, conn, cwd)
	newSession(t, conn, t.TempDir()) // another directory: never listed below
	if _, err := conn.UnstableArchiveSession(ctx, acp.UnstableArchiveSessionRequest{SessionId: archived}); err != nil {
		t.Fatal(err)
	}
	for filter, want := range map[acp.SessionArchiveFilter][]acp.SessionId{
		"":                              {kept},
		acp.SessionArchiveFilterExclude: {kept},
		acp.SessionArchiveFilterInclude: {kept, archived},
		acp.SessionArchiveFilterOnly:    {archived},
	} {
		req := acp.ListSessionsRequest{Cwd: &cwd}
		if filter != "" {
			req.Archived = &filter
		}
		resp, err := conn.ListSessions(ctx, req)
		if err != nil {
			t.Fatalf("%q: %v", filter, err)
		}
		var got []acp.SessionId
		for _, s := range resp.Sessions {
			got = append(got, s.SessionId)
			if s.Archived == nil || *s.Archived != (s.SessionId == archived) {
				t.Errorf("%q: %s has archived %v", filter, s.SessionId, s.Archived)
			}
		}
		if len(got) != len(want) || len(got) > 0 && (got[0] != want[0] || got[len(got)-1] != want[len(want)-1]) {
			t.Errorf("%q: listed %v, want %v", filter, got, want)
		}
	}
}

func TestAgent_InitialConfig(t *testing.T) {
	ctx := context.Background()
	conn, _ := connect(t, acp.ClientCapabilities{})
	value := func(id acp.SessionConfigId, v acp.SessionConfigValueId) acp.InitialSessionConfigOption {
		return acp.InitialSessionConfigOption{ValueId: &acp.InitialSessionConfigOptionValueId{ConfigId: id, Value: v}}
	}
	_, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}, ConfigOptions: []acp.InitialSessionConfigOption{
		value(configModel, modelDeep), value(configModel, modelFast),
	}})
	wantCode(t, "an option named twice", err, -32602, "")

	resp, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}, ConfigOptions: []acp.InitialSessionConfigOption{
		value("no-such-option", "x"), value(configModel, modelDeep),
	}})
	if err != nil {
		t.Fatalf("an unknown option must be skipped, not rejected: %v", err)
	}
	if got := resp.ConfigOptions[0].Select.CurrentValue; got != modelDeep {
		t.Errorf("model is %q, want the initial value %q", got, modelDeep)
	}
	for _, o := range resp.ConfigOptions {
		if o.Boolean != nil {
			t.Errorf("a client that accepts no booleans got boolean option %q", o.Boolean.Id)
		}
	}
}

// A slow turn runs until the client cancels it, and then ends cancelled.
func TestAgent_SlowTurnEndsCancelled(t *testing.T) {
	ctx := context.Background()
	conn, c := connect(t, acp.ClientCapabilities{})
	sid := newSession(t, conn, t.TempDir())
	done := make(chan acp.StopReason, 1)
	go func() {
		resp, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: text("slow")})
		if err != nil {
			t.Error(err)
		}
		done <- resp.StopReason
	}()
	c.waitFor(t, func(n acp.SessionNotification) bool { return n.Update.AgentMessageChunk != nil })
	select {
	case r := <-done:
		t.Fatalf("the slow turn ended on its own (%s)", r)
	case <-time.After(3 * streamInterval):
	}
	if err := conn.Cancel(ctx, acp.CancelNotification{SessionId: sid}); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r != acp.StopReasonCancelled {
		t.Errorf("stop reason %q, want cancelled", r)
	}
}

// Cancelling the cancellable subagent stops it and leaves the parent's turn
// running until the parent is cancelled. The client cannot address the
// child.
func TestAgent_CancelSubagentThenParent(t *testing.T) {
	ctx := context.Background()
	conn, c := connect(t, acp.ClientCapabilities{Subagents: &acp.SubagentCapabilities{}})
	sid := newSession(t, conn, t.TempDir())
	done := make(chan acp.StopReason, 1)
	go func() {
		resp, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: text("delegate, cancellable")})
		if err != nil {
			t.Error(err)
		}
		done <- resp.StopReason
	}()
	announced := c.waitFor(t, func(n acp.SessionNotification) bool {
		u := n.Update.SubagentUpdate
		if u == nil || n.SessionId != sid {
			return false
		}
		caps, _ := u.Capabilities.Value()
		return caps.Cancel != nil
	})
	cid := announced.Update.SubagentUpdate.SessionId

	_, err := conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: cid})
	wantCode(t, "close of a child", err, -32602, "")
	if err := conn.Cancel(ctx, acp.CancelNotification{SessionId: cid}); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, func(n acp.SessionNotification) bool {
		u := n.Update.SubagentUpdate
		if u == nil || u.SessionId != cid {
			return false
		}
		state, _ := u.State.Value()
		return state.Idle != nil && state.Idle.Cancelled != nil
	})
	select {
	case r := <-done:
		t.Fatalf("cancelling the child ended the parent's turn (%s)", r)
	case <-time.After(3 * streamInterval):
	}
	if err := conn.Cancel(ctx, acp.CancelNotification{SessionId: sid}); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r != acp.StopReasonCancelled {
		t.Errorf("parent stop reason %q, want cancelled", r)
	}
}

// The agent writes the probe file only when the client picks the allow
// option: a rejection or an option it never offered is not approval.
func TestAgent_WritesOnlyWithPermission(t *testing.T) {
	for name, tc := range map[string]struct {
		permit acp.RequestPermissionOutcome
		write  bool
	}{
		"allow":          {acp.NewRequestPermissionOutcomeSelected("allow"), true},
		"reject":         {acp.NewRequestPermissionOutcomeSelected("reject"), false},
		"unknown option": {acp.NewRequestPermissionOutcomeSelected("_never_offered"), false},
	} {
		t.Run(name, func(t *testing.T) {
			conn, c := connect(t, acp.ClientCapabilities{})
			c.mu.Lock()
			c.permit = tc.permit
			c.mu.Unlock()
			cwd := t.TempDir()
			sid := newSession(t, conn, cwd)
			if _, err := conn.Prompt(context.Background(), acp.PromptRequest{SessionId: sid, Prompt: text(permissionPrompt)}); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(filepath.Join(cwd, "tck-permission-probe.txt"))
			if wrote := err == nil; wrote != tc.write {
				t.Errorf("wrote the file: %v, want %v", wrote, tc.write)
			}
		})
	}
}
