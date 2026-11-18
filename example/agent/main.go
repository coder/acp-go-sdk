package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
)

type agentSession struct {
	cwd    string
	seq    uint64
	cancel context.CancelFunc
	turn   uint64 // counts the prompts; the newest one owns cancel
	// The session's config options, see configOptions.
	model     acp.SessionConfigValueId
	webSearch bool
}

type exampleAgent struct {
	conn     *acp.AgentSideConnection
	sessions map[string]*agentSession
	nextSeq  uint64
	// booleanConfig is set when the client accepts boolean config options.
	booleanConfig bool
	// formElicitation and urlElicitation are set when the client accepts
	// elicitation/create in form and url mode.
	formElicitation, urlElicitation bool
	// notices and compaction are set when the client accepts notice and
	// compaction session updates.
	notices, compaction bool
	// mu guards sessions, nextSeq, the client's capability flags and every
	// field of each agentSession. A handler never calls the client while
	// it holds mu.
	mu sync.Mutex
}

var (
	_ acp.Agent             = (*exampleAgent)(nil)
	_ acp.AgentLoader       = (*exampleAgent)(nil)
	_ acp.AgentExperimental = (*exampleAgent)(nil)
)

func newExampleAgent() *exampleAgent {
	return &exampleAgent{sessions: make(map[string]*agentSession)}
}

// SetSessionMode implements acp.Agent.
func (a *exampleAgent) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

// UnstableForkSession implements acp.AgentExperimental.
func (a *exampleAgent) UnstableForkSession(ctx context.Context, params acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	return acp.UnstableForkSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionFork)
}

// ListSessions implements acp.Agent. It returns every session, or those in
// params.Cwd when it is set, in creation order on one page.
func (a *exampleAgent) ListSessions(ctx context.Context, params acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sessions := []acp.SessionInfo{}
	for id, s := range a.sessions {
		if params.Cwd != nil && *params.Cwd != s.cwd {
			continue
		}
		sessions = append(sessions, acp.SessionInfo{SessionId: acp.SessionId(id), Cwd: s.cwd})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return a.sessions[string(sessions[i].SessionId)].seq < a.sessions[string(sessions[j].SessionId)].seq
	})
	return acp.ListSessionsResponse{Sessions: sessions}, nil
}

// DeleteSession implements acp.Agent. It cancels the session's running turn
// and drops the session, so session/list no longer returns it. An unknown
// session ID succeeds, as the protocol asks.
func (a *exampleAgent) DeleteSession(ctx context.Context, params acp.DeleteSessionRequest) (acp.DeleteSessionResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sessions[string(params.SessionId)]; ok && s.cancel != nil {
		s.cancel()
	}
	delete(a.sessions, string(params.SessionId))
	return acp.DeleteSessionResponse{}, nil
}

// ResumeSession implements acp.Agent.
func (a *exampleAgent) ResumeSession(ctx context.Context, params acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

// The config option ids and the model values that the example agent offers.
const (
	configModel     acp.SessionConfigId      = "model"
	configWebSearch acp.SessionConfigId      = "web_search"
	modelFast       acp.SessionConfigValueId = "demo-fast"
	modelDeep       acp.SessionConfigValueId = "demo-deep"
)

// configOptions returns the session's config options: a model select, and a
// boolean in the model_config category when the client accepts booleans.
// The caller holds a.mu.
func (a *exampleAgent) configOptions(s *agentSession) []acp.SessionConfigOption {
	models := acp.SessionConfigSelectOptionsUngrouped{
		{Value: modelFast, Name: "Demo Fast"},
		{Value: modelDeep, Name: "Demo Deep"},
	}
	opts := []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id:           configModel,
		Name:         "Model",
		Category:     acp.Ptr(acp.SessionConfigOptionCategoryModel),
		Type:         "select",
		CurrentValue: s.model,
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &models},
	}}}
	if a.booleanConfig {
		opts = append(opts, acp.SessionConfigOption{Boolean: &acp.SessionConfigOptionBoolean{
			Id:           configWebSearch,
			Name:         "Web search",
			Category:     acp.Ptr(acp.SessionConfigOptionCategoryModelConfig),
			Type:         "boolean",
			CurrentValue: s.webSearch,
		}})
	}
	return opts
}

// SetSessionConfigOption implements acp.Agent. It sets the model to one of
// its values, or web search on or off, and returns every option.
func (a *exampleAgent) SetSessionConfigOption(ctx context.Context, params acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	invalid := func(configID acp.SessionConfigId, msg string) error {
		return acp.NewInvalidParams(map[string]any{"configId": configID, "error": msg})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var s *agentSession
	switch {
	case params.ValueId != nil:
		p := params.ValueId
		s = a.sessions[string(p.SessionId)]
		if s == nil {
			return acp.SetSessionConfigOptionResponse{}, invalid(p.ConfigId, "unknown session")
		}
		if p.ConfigId != configModel {
			return acp.SetSessionConfigOptionResponse{}, invalid(p.ConfigId, "no select option with this id")
		}
		if p.Value != modelFast && p.Value != modelDeep {
			return acp.SetSessionConfigOptionResponse{}, invalid(p.ConfigId, "unknown model "+string(p.Value))
		}
		s.model = p.Value
	case params.Boolean != nil:
		p := params.Boolean
		s = a.sessions[string(p.SessionId)]
		if s == nil {
			return acp.SetSessionConfigOptionResponse{}, invalid(p.ConfigId, "unknown session")
		}
		if p.ConfigId != configWebSearch || !a.booleanConfig {
			return acp.SetSessionConfigOptionResponse{}, invalid(p.ConfigId, "no boolean option with this id")
		}
		s.webSearch = p.Value
	default:
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "no value"})
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: a.configOptions(s)}, nil
}

// UnstableMessageMcp implements acp.AgentExperimental. The example agent
// sends no mcp/message request, so it has no active request for a client's
// MCP notification to belong to, and drops it.
func (a *exampleAgent) UnstableMessageMcp(ctx context.Context, params acp.UnstableMessageMcpNotification) error {
	return nil
}

// UnstableDidChangeDocument implements acp.AgentExperimental.
func (a *exampleAgent) UnstableDidChangeDocument(ctx context.Context, params acp.UnstableDidChangeDocumentNotification) error {
	return nil
}

// UnstableDidCloseDocument implements acp.AgentExperimental.
func (a *exampleAgent) UnstableDidCloseDocument(ctx context.Context, params acp.UnstableDidCloseDocumentNotification) error {
	return nil
}

// UnstableDidFocusDocument implements acp.AgentExperimental.
func (a *exampleAgent) UnstableDidFocusDocument(ctx context.Context, params acp.UnstableDidFocusDocumentNotification) error {
	return nil
}

// UnstableDidOpenDocument implements acp.AgentExperimental.
func (a *exampleAgent) UnstableDidOpenDocument(ctx context.Context, params acp.UnstableDidOpenDocumentNotification) error {
	return nil
}

// UnstableDidSaveDocument implements acp.AgentExperimental.
func (a *exampleAgent) UnstableDidSaveDocument(ctx context.Context, params acp.UnstableDidSaveDocumentNotification) error {
	return nil
}

// Logout implements acp.Agent.
func (a *exampleAgent) Logout(ctx context.Context, params acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

// UnstableAcceptNes implements acp.AgentExperimental.
func (a *exampleAgent) UnstableAcceptNes(ctx context.Context, params acp.UnstableAcceptNesNotification) error {
	return nil
}

// UnstableCloseNes implements acp.AgentExperimental.
func (a *exampleAgent) UnstableCloseNes(ctx context.Context, params acp.UnstableCloseNesRequest) (acp.UnstableCloseNesResponse, error) {
	return acp.UnstableCloseNesResponse{}, acp.NewMethodNotFound(acp.AgentMethodNesClose)
}

// UnstableRejectNes implements acp.AgentExperimental.
func (a *exampleAgent) UnstableRejectNes(ctx context.Context, params acp.UnstableRejectNesNotification) error {
	return nil
}

// UnstableStartNes implements acp.AgentExperimental.
func (a *exampleAgent) UnstableStartNes(ctx context.Context, params acp.UnstableStartNesRequest) (acp.UnstableStartNesResponse, error) {
	return acp.UnstableStartNesResponse{}, acp.NewMethodNotFound(acp.AgentMethodNesStart)
}

// UnstableSuggestNes implements acp.AgentExperimental.
func (a *exampleAgent) UnstableSuggestNes(ctx context.Context, params acp.UnstableSuggestNesRequest) (acp.UnstableSuggestNesResponse, error) {
	return acp.UnstableSuggestNesResponse{}, acp.NewMethodNotFound(acp.AgentMethodNesSuggest)
}

// UnstableDisableProvider implements acp.AgentExperimental.
func (a *exampleAgent) UnstableDisableProvider(ctx context.Context, params acp.UnstableDisableProviderRequest) (acp.UnstableDisableProviderResponse, error) {
	return acp.UnstableDisableProviderResponse{}, acp.NewMethodNotFound(acp.AgentMethodProvidersDisable)
}

// UnstableListProviders implements acp.AgentExperimental.
func (a *exampleAgent) UnstableListProviders(ctx context.Context, params acp.UnstableListProvidersRequest) (acp.UnstableListProvidersResponse, error) {
	return acp.UnstableListProvidersResponse{}, acp.NewMethodNotFound(acp.AgentMethodProvidersList)
}

// UnstableSetProvider implements acp.AgentExperimental.
func (a *exampleAgent) UnstableSetProvider(ctx context.Context, params acp.UnstableSetProviderRequest) (acp.UnstableSetProviderResponse, error) {
	return acp.UnstableSetProviderResponse{}, acp.NewMethodNotFound(acp.AgentMethodProvidersSet)
}

// CloseSession implements acp.Agent.
func (a *exampleAgent) CloseSession(ctx context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionClose)
}

// Implement acp.AgentConnAware to receive the connection after construction.
func (a *exampleAgent) SetAgentConnection(conn *acp.AgentSideConnection) { a.conn = conn }

func (a *exampleAgent) Initialize(ctx context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	caps := params.ClientCapabilities.Session
	elicitation := params.ClientCapabilities.Elicitation
	a.mu.Lock()
	a.booleanConfig = caps != nil && caps.ConfigOptions != nil && caps.ConfigOptions.Boolean != nil
	a.formElicitation = elicitation != nil && elicitation.Form != nil
	a.urlElicitation = elicitation != nil && elicitation.Url != nil
	a.notices = caps != nil && caps.Notices != nil
	a.compaction = caps != nil && caps.Compaction != nil
	a.mu.Unlock()
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: false,
			SessionCapabilities: acp.SessionCapabilities{
				List:   &acp.SessionListCapabilities{},
				Delete: &acp.SessionDeleteCapabilities{},
			},
		},
	}, nil
}

func (a *exampleAgent) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	sid := randomID("sess_")
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextSeq++
	s := &agentSession{cwd: params.Cwd, seq: a.nextSeq, model: modelFast}
	a.sessions[sid] = s
	return acp.NewSessionResponse{SessionId: acp.SessionId(sid), ConfigOptions: a.configOptions(s)}, nil
}

func (a *exampleAgent) Authenticate(ctx context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *exampleAgent) LoadSession(ctx context.Context, _ acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	return acp.LoadSessionResponse{}, nil
}

// Cancel cancels the session's running turn. It copies the cancel func under
// a.mu, which guards every agentSession field, and calls it after the unlock.
func (a *exampleAgent) Cancel(ctx context.Context, params acp.CancelNotification) error {
	var cancel context.CancelFunc
	a.mu.Lock()
	if s, ok := a.sessions[string(params.SessionId)]; ok {
		cancel = s.cancel
	}
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (a *exampleAgent) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	sid := string(params.SessionId)
	// Look up the session and install this turn's cancel func in one critical
	// section, so that a concurrent DeleteSession either runs first or
	// cancels this turn.
	a.mu.Lock()
	s, ok := a.sessions[sid]
	if !ok {
		a.mu.Unlock()
		return acp.PromptResponse{}, fmt.Errorf("session %s not found", sid)
	}
	ctx, cancel := context.WithCancel(ctx)
	prev := s.cancel
	s.cancel = cancel
	s.turn++
	turn := s.turn
	a.mu.Unlock()
	// cancel any previous turn
	if prev != nil {
		prev()
	}

	// /compact compacts the session; any other prompt runs the demo turn with
	// streaming updates and a permission request
	run := a.simulateTurn
	if isCommand(params.Prompt, "/compact") {
		run = a.compact
	}
	if err := run(ctx, sid); err != nil {
		if ctx.Err() != nil {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.turn == turn {
		s.cancel = nil
	}
	// A delete that ran after the turn's last step still cancels it.
	if ctx.Err() != nil {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

// isCommand reports whether the prompt is the one text block command.
func isCommand(prompt []acp.ContentBlock, command string) bool {
	return len(prompt) == 1 && prompt[0].Text != nil && prompt[0].Text.Text == command
}

// disclaimer tells the user that this is the example agent: as a notice when
// the client presents notices, else as an agent message.
func (a *exampleAgent) disclaimer() acp.SessionUpdate {
	a.mu.Lock()
	notices := a.notices
	a.mu.Unlock()
	if !notices {
		return acp.UpdateAgentMessageText("ACP Go Example Agent — demo only (no AI model).")
	}
	return acp.SessionUpdate{Notice: &acp.SessionUpdateNotice{
		Severity:    acp.NoticeSeverityInfo,
		Title:       "ACP Go Example Agent — demo only",
		Description: acp.Ptr("No AI model runs. Every turn is scripted."),
	}}
}

// compact runs the /compact command. The demo keeps no conversation, so the
// summary only counts the session's earlier prompts. A client that accepts
// compaction updates sees the compaction start, its summary stream in and the
// compaction complete. Any other client gets the summary as an agent message.
func (a *exampleAgent) compact(ctx context.Context, sid string) error {
	a.mu.Lock()
	supported := a.compaction
	var earlier uint64
	if s, ok := a.sessions[sid]; ok {
		earlier = s.turn - 1
	}
	a.mu.Unlock()
	summary := fmt.Sprintf("Compacted %d earlier prompt(s) of this demo session.", earlier)
	if !supported {
		return a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: acp.SessionId(sid),
			Update:    acp.UpdateAgentMessageText(summary),
		})
	}
	id := acp.CompactionId(randomID("cmp_"))
	updates := []acp.SessionUpdate{
		{CompactionUpdate: &acp.SessionCompactionUpdate{CompactionId: id, Status: acp.CompactionStatusInProgress}},
		{CompactionSummaryChunk: &acp.SessionUpdateCompactionSummaryChunk{CompactionId: id, Content: acp.TextBlock(summary)}},
		{CompactionUpdate: &acp.SessionCompactionUpdate{CompactionId: id, Status: acp.CompactionStatusCompleted}},
	}
	for i, u := range updates {
		if i > 0 {
			if err := pause(ctx, 250*time.Millisecond); err != nil {
				return err
			}
		}
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: acp.SessionId(sid), Update: u}); err != nil {
			return err
		}
	}
	return nil
}

func (a *exampleAgent) simulateTurn(ctx context.Context, sid string) error {
	// disclaimer, so clients see it's the example agent
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update:    a.disclaimer(),
	}); err != nil {
		return err
	}
	if err := pause(ctx, 250*time.Millisecond); err != nil {
		return err
	}
	// initial message chunk
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update:    acp.UpdateAgentMessageText("I'll help you with that. Let me start by reading some files to understand the current situation."),
	}); err != nil {
		return err
	}
	if err := pause(ctx, time.Second); err != nil {
		return err
	}

	// tool call without permission
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update: acp.StartToolCall(
			acp.ToolCallId("call_1"),
			"Reading project files",
			acp.WithStartName("read_file"),
			acp.WithStartKind(acp.ToolKindRead),
			acp.WithStartStatus(acp.ToolCallStatusPending),
			acp.WithStartLocations([]acp.ToolCallLocation{{Path: "/project/README.md"}}),
			acp.WithStartRawInput(map[string]any{"path": "/project/README.md"}),
		),
	}); err != nil {
		return err
	}
	if err := pause(ctx, time.Second); err != nil {
		return err
	}

	// update tool call completed
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update: acp.UpdateToolCall(
			acp.ToolCallId("call_1"),
			acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
			acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock("# My Project\n\nThis is a sample project..."))}),
			acp.WithUpdateRawOutput(map[string]any{"content": "# My Project\n\nThis is a sample project..."}),
		),
	}); err != nil {
		return err
	}
	if err := pause(ctx, time.Second); err != nil {
		return err
	}

	// more text
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update:    acp.UpdateAgentMessageText(" Now I understand the project structure. I need to make some changes to improve it."),
	}); err != nil {
		return err
	}
	if err := pause(ctx, time.Second); err != nil {
		return err
	}

	// tool call requiring permission
	if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update: acp.StartToolCall(
			acp.ToolCallId("call_2"),
			"Modifying critical configuration file",
			acp.WithStartName("write_file"),
			acp.WithStartKind(acp.ToolKindEdit),
			acp.WithStartStatus(acp.ToolCallStatusPending),
			acp.WithStartLocations([]acp.ToolCallLocation{{Path: "/project/config.json"}}),
			acp.WithStartRawInput(map[string]any{"path": "/project/config.json", "content": "{\"database\": {\"host\": \"new-host\"}}"}),
		),
	}); err != nil {
		return err
	}

	// ask the user for the value that the edit writes
	host, err := a.askHost(ctx, sid)
	if err != nil {
		return err
	}
	hostJSON, err := json.Marshal(host)
	if err != nil {
		return err
	}
	edit := map[string]any{"path": "/project/config.json", "content": fmt.Sprintf(`{"database": {"host": %s}}`, hostJSON)}

	// request permission for sensitive operation
	permResp, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(sid),
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("call_2"),
			Title:      acp.Ptr("Modifying critical configuration file"),
			Name:       acp.Ptr("write_file"),
			Kind:       acp.Ptr(acp.ToolKindEdit),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
			Locations:  []acp.ToolCallLocation{{Path: "/project/config.json"}},
			RawInput:   edit,
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow this change", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Skip this change", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		return err
	}

	// handle permission outcome
	if permResp.Outcome.Cancelled != nil {
		return nil
	}
	if permResp.Outcome.Selected == nil {
		return fmt.Errorf("unexpected permission outcome")
	}
	switch string(permResp.Outcome.Selected.OptionId) {
	case "allow":
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: acp.SessionId(sid),
			Update: acp.UpdateToolCall(
				acp.ToolCallId("call_2"),
				acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
				acp.WithUpdateRawOutput(map[string]any{"success": true, "message": "Configuration updated"}),
				acp.WithUpdateTitle("Modifying critical configuration file"),
			),
		}); err != nil {
			return err
		}
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: acp.SessionId(sid),
			Update:    acp.UpdateAgentMessageText(" Perfect! I've successfully updated the configuration. The changes have been applied."),
		}); err != nil {
			return err
		}
		if err := a.askReview(ctx, sid); err != nil {
			return err
		}
	case "reject":
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{
			SessionId: acp.SessionId(sid),
			Update:    acp.UpdateAgentMessageText(" I understand you prefer not to make that change. I'll skip the configuration update."),
		}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unexpected permission option: %s", permResp.Outcome.Selected.OptionId)
	}
	return nil
}

// defaultHost is the database host that the edit writes unless the user
// gives another one.
const defaultHost = "new-host"

// askHost asks the user, through a form elicitation tied to the edit's tool
// call, which database host the edit writes. It returns defaultHost when the
// client does not accept form elicitation, or when the user declines,
// cancels, or accepts without a host.
func (a *exampleAgent) askHost(ctx context.Context, sid string) (string, error) {
	a.mu.Lock()
	form := a.formElicitation
	a.mu.Unlock()
	if !form {
		return defaultHost, nil
	}
	resp, err := a.conn.CreateElicitation(ctx, acp.CreateElicitationRequest{Form: &acp.CreateElicitationForm{
		Mode:    "form",
		Message: "Which database host should the configuration use?",
		RequestedSchema: acp.ElicitationSchema{
			Type: acp.ElicitationSchemaTypeObject,
			Properties: map[string]any{"host": acp.ElicitationPropertySchema{String: &acp.ElicitationPropertySchemaString{
				Type:      "string",
				Title:     acp.Ptr("Database host"),
				Default:   acp.Ptr(defaultHost),
				MinLength: acp.Ptr(1),
			}}},
			Required: []string{"host"},
		},
		Session: &acp.ElicitationSessionScope{SessionId: acp.SessionId(sid), ToolCallId: acp.Ptr(acp.ToolCallId("call_2"))},
	}})
	if err != nil {
		return "", err
	}
	if resp.Accept != nil {
		if host, ok := resp.Accept.Content["host"].(string); ok && host != "" {
			return host, nil
		}
	}
	return defaultHost, nil
}

// askReview asks the user, through a url elicitation, to review the change in
// a browser, when the client accepts url elicitation. The demo has no review
// page, so once the user accepts, the agent reports the review as done with
// elicitation/complete at once.
func (a *exampleAgent) askReview(ctx context.Context, sid string) error {
	a.mu.Lock()
	url := a.urlElicitation
	a.mu.Unlock()
	if !url {
		return nil
	}
	id := acp.ElicitationId(randomID("elic_"))
	resp, err := a.conn.CreateElicitation(ctx, acp.CreateElicitationRequest{Url: &acp.CreateElicitationUrl{
		Mode:          "url",
		ElicitationId: id,
		Message:       "Review the configuration change in your browser.",
		Url:           "https://example.com/acp-demo/review",
		Session:       &acp.ElicitationSessionScope{SessionId: acp.SessionId(sid)},
	}})
	if err != nil || resp.Accept == nil {
		return err
	}
	return a.conn.CompleteElicitation(ctx, acp.CompleteElicitationNotification{ElicitationId: id})
}

func randomID(prefix string) string {
	var b [12]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		// fallback to time-based
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b[:])
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

func main() {
	// If args provided, treat them as client program + args to spawn and connect via stdio.
	// Otherwise, default to stdio (allowing manual wiring or use by another process).
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	var (
		out io.Writer = os.Stdout
		in  io.Reader = os.Stdin
		cmd *exec.Cmd
	)
	if len(os.Args) > 1 {
		cmd = exec.CommandContext(ctx, os.Args[1], os.Args[2:]...)
		cmd.Stderr = os.Stderr
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to start client: %v\n", err)
			os.Exit(1)
		}
		out = stdin
		in = stdout
	}

	ag := newExampleAgent()
	asc := acp.NewAgentSideConnection(ag, out, in)
	asc.SetLogger(slog.Default())
	ag.SetAgentConnection(asc)

	// Block until the peer disconnects.
	<-asc.Done()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
