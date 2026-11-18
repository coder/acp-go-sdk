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
	"strings"
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
	// systemPrompt is the text of the client's system prompt, if any.
	systemPrompt string
	// history is every session/update of the session in order: the user's
	// prompts and each update that the agent sent. LoadSession replays it.
	history []acp.SessionUpdate
	// toolCalls counts the tool call ids that the session's turns used, so
	// that each id is unique within the session, as the protocol asks.
	toolCalls int
	// sendMu orders the session's updates with a replay. sessionUpdate holds
	// it while it sends an update and adds it to history, and LoadSession
	// while it replays history, so a replay never misses or repeats an
	// update. Lock order: sendMu, then exampleAgent.mu.
	sendMu sync.Mutex
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
	// field of each agentSession but sendMu. A handler never calls the
	// client while it holds mu.
	mu sync.Mutex
}

// The example agent implements acp.Agent and two optional interfaces. An
// agent asserts each optional interface it implements, so that a schema
// release that changes the method breaks the build instead of turning the
// method into -32601 (method not found).
var (
	_ acp.Agent                   = (*exampleAgent)(nil)
	_ acp.AgentLoader             = (*exampleAgent)(nil)
	_ acp.AgentUnstableMessageMcp = (*exampleAgent)(nil)
)

func newExampleAgent() *exampleAgent {
	return &exampleAgent{sessions: make(map[string]*agentSession)}
}

// SetSessionMode implements acp.Agent.
func (a *exampleAgent) SetSessionMode(ctx context.Context, params acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
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
// and drops the session, so session/list no longer returns it. It holds the
// session's sendMu, so it waits for a running replay to finish, and no update
// of the session follows it. An unknown session ID succeeds, as the protocol
// asks.
func (a *exampleAgent) DeleteSession(ctx context.Context, params acp.DeleteSessionRequest) (acp.DeleteSessionResponse, error) {
	sid := string(params.SessionId)
	a.mu.Lock()
	s := a.sessions[sid]
	a.mu.Unlock()
	if s == nil {
		return acp.DeleteSessionResponse{}, nil
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	if a.sessions[sid] == s {
		delete(a.sessions, sid)
	}
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

// UnstableMessageMcp implements acp.AgentUnstableMessageMcp. The example agent
// sends no mcp/message request, so it has no active request for a client's
// MCP notification to belong to, and drops it.
func (a *exampleAgent) UnstableMessageMcp(ctx context.Context, params acp.UnstableMessageMcpNotification) error {
	return nil
}

// Logout implements acp.Agent.
func (a *exampleAgent) Logout(ctx context.Context, params acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
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
			LoadSession: true,
			SessionCapabilities: acp.SessionCapabilities{
				List:   &acp.SessionListCapabilities{},
				Delete: &acp.SessionDeleteCapabilities{},
				// A system prompt in append mode, the one mode that every
				// agent with the capability supports.
				SystemPrompt: &acp.SessionSystemPromptCapabilities{},
			},
		},
	}, nil
}

func (a *exampleAgent) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	prompt, err := systemPromptText(params.SystemPrompt)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	sid := randomID("sess_")
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextSeq++
	s := &agentSession{cwd: params.Cwd, seq: a.nextSeq, model: modelFast, systemPrompt: prompt}
	a.sessions[sid] = s
	return acp.NewSessionResponse{SessionId: acp.SessionId(sid), ConfigOptions: a.configOptions(s)}, nil
}

// systemPromptText returns the text of a client's system prompt. The agent
// advertises append mode only, and it must reject a prompt that it cannot
// apply instead of creating the session: another mode, an empty prompt, or a
// block that is not text.
func systemPromptText(p *acp.SystemPrompt) (string, error) {
	if p == nil {
		return "", nil
	}
	invalid := func(msg string) error { return acp.NewInvalidParams(map[string]any{"error": msg}) }
	if p.Mode != acp.SystemPromptModeAppend {
		return "", invalid(fmt.Sprintf("systemPrompt mode %q is not supported", p.Mode))
	}
	if len(p.Content) == 0 {
		return "", invalid("systemPrompt content is empty")
	}
	parts := make([]string, 0, len(p.Content))
	for _, b := range p.Content {
		if b.Text == nil {
			return "", invalid("systemPrompt content must be text blocks")
		}
		parts = append(parts, b.Text.Text)
	}
	return strings.Join(parts, "\n\n"), nil
}

func (a *exampleAgent) Authenticate(ctx context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

// LoadSession implements acp.AgentLoader. It replays the session's history as
// session/update notifications before it answers, as the protocol asks, and
// takes a new system prompt the way NewSession does. Without one, the session
// keeps the prompt that it has. The agent keeps sessions in memory, so it can
// load only a session of this process.
func (a *exampleAgent) LoadSession(ctx context.Context, params acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	prompt, err := systemPromptText(params.SystemPrompt)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	// An unknown session gets -32002 (resource not found) with the requested
	// sessionId in data, as the protocol asks of the session methods.
	notFound := &acp.RequestError{Code: -32002, Message: "Resource not found", Data: map[string]any{"sessionId": params.SessionId}}
	sid := string(params.SessionId)
	a.mu.Lock()
	s := a.sessions[sid]
	a.mu.Unlock()
	if s == nil {
		return acp.LoadSessionResponse{}, notFound
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	a.mu.Lock()
	switch {
	case a.sessions[sid] != s: // deleted while this call waited for sendMu
		a.mu.Unlock()
		return acp.LoadSessionResponse{}, notFound
	case params.Cwd != s.cwd:
		a.mu.Unlock()
		return acp.LoadSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "cwd does not match the session"})
	}
	history := append([]acp.SessionUpdate(nil), s.history...)
	a.mu.Unlock()
	for _, u := range history {
		if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: params.SessionId, Update: u}); err != nil {
			return acp.LoadSessionResponse{}, err
		}
	}
	// A failed load leaves the session's prompt as it was. sendMu keeps the
	// session from being deleted until this returns.
	a.mu.Lock()
	defer a.mu.Unlock()
	if params.SystemPrompt != nil {
		s.systemPrompt = prompt
	}
	return acp.LoadSessionResponse{ConfigOptions: a.configOptions(s)}, nil
}

// sessionUpdate sends an update of a session and adds it to the session's
// history, except a notice: notices are live events, not session history.
// See agentSession.sendMu.
func (a *exampleAgent) sessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	a.mu.Lock()
	s := a.sessions[string(n.SessionId)]
	a.mu.Unlock()
	if s == nil {
		return a.conn.SessionUpdate(ctx, n)
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if err := a.conn.SessionUpdate(ctx, n); err != nil {
		return err
	}
	if n.Update.Notice == nil {
		a.mu.Lock()
		s.history = append(s.history, n.Update)
		a.mu.Unlock()
	}
	return nil
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
	notFound := fmt.Errorf("session %s not found", sid)
	a.mu.Lock()
	s := a.sessions[sid]
	a.mu.Unlock()
	if s == nil {
		return acp.PromptResponse{}, notFound
	}
	// Start the turn in one critical section under sendMu and a.mu: check
	// that the session still exists and that no newer prompt or cancel has
	// ended this one, install the turn's cancel func, and add the prompt to
	// the history. A concurrent DeleteSession then either runs first or
	// cancels this turn. The SDK cancels the context of a prompt that
	// arrived before the session's newest one, so a late prompt neither
	// cancels the newer turn nor lands after it in the history. Under sendMu,
	// an update of an earlier turn lands before or after the whole prompt.
	s.sendMu.Lock()
	a.mu.Lock()
	if a.sessions[sid] != s {
		a.mu.Unlock()
		s.sendMu.Unlock()
		return acp.PromptResponse{}, notFound
	}
	if ctx.Err() != nil {
		a.mu.Unlock()
		s.sendMu.Unlock()
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	prev := s.cancel
	s.cancel = cancel
	s.turn++
	turn := s.turn
	for _, block := range params.Prompt {
		s.history = append(s.history, acp.UpdateUserMessage(block))
	}
	a.mu.Unlock()
	s.sendMu.Unlock()
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
		return a.sessionUpdate(ctx, acp.SessionNotification{
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
		if err := a.sessionUpdate(ctx, acp.SessionNotification{SessionId: acp.SessionId(sid), Update: u}); err != nil {
			return err
		}
	}
	return nil
}

// toolCallIDs returns the ids of a turn's read and edit tool calls: call_1
// and call_2 in the session's first turn, call_3 and call_4 in the next.
func (a *exampleAgent) toolCallIDs(sid string) (read, edit acp.ToolCallId) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	if s, ok := a.sessions[sid]; ok {
		n = s.toolCalls
		s.toolCalls += 2
	}
	return acp.ToolCallId(fmt.Sprintf("call_%d", n+1)), acp.ToolCallId(fmt.Sprintf("call_%d", n+2))
}

func (a *exampleAgent) simulateTurn(ctx context.Context, sid string) error {
	readID, editID := a.toolCallIDs(sid)
	// disclaimer, so clients see it's the example agent
	if err := a.sessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update:    a.disclaimer(),
	}); err != nil {
		return err
	}
	if err := pause(ctx, 250*time.Millisecond); err != nil {
		return err
	}
	// A scripted agent cannot follow a system prompt, so it shows the one
	// that it would follow.
	a.mu.Lock()
	var prompt string
	if s, ok := a.sessions[sid]; ok {
		prompt = s.systemPrompt
	}
	a.mu.Unlock()
	if prompt != "" {
		if err := a.sessionUpdate(ctx, acp.SessionNotification{
			SessionId: acp.SessionId(sid),
			Update:    acp.UpdateAgentMessageText("Following your system prompt: " + prompt),
		}); err != nil {
			return err
		}
	}
	// initial message chunk
	if err := a.sessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update:    acp.UpdateAgentMessageText("I'll help you with that. Let me start by reading some files to understand the current situation."),
	}); err != nil {
		return err
	}
	if err := pause(ctx, time.Second); err != nil {
		return err
	}

	// tool call without permission
	if err := a.sessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update: acp.StartToolCall(
			readID,
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
	if err := a.sessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update: acp.UpdateToolCall(
			readID,
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
	if err := a.sessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update:    acp.UpdateAgentMessageText(" Now I understand the project structure. I need to make some changes to improve it."),
	}); err != nil {
		return err
	}
	if err := pause(ctx, time.Second); err != nil {
		return err
	}

	// tool call requiring permission
	if err := a.sessionUpdate(ctx, acp.SessionNotification{
		SessionId: acp.SessionId(sid),
		Update: acp.StartToolCall(
			editID,
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
	host, err := a.askHost(ctx, sid, editID)
	if err != nil {
		return err
	}
	hostJSON, err := json.Marshal(host)
	if err != nil {
		return err
	}
	edit, err := json.Marshal(map[string]any{"path": "/project/config.json", "content": fmt.Sprintf(`{"database": {"host": %s}}`, hostJSON)})
	if err != nil {
		return err
	}

	// request permission for sensitive operation
	permResp, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(sid),
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: editID,
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
		if err := a.sessionUpdate(ctx, acp.SessionNotification{
			SessionId: acp.SessionId(sid),
			Update: acp.UpdateToolCall(
				editID,
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
		if err := a.sessionUpdate(ctx, acp.SessionNotification{
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
		if err := a.sessionUpdate(ctx, acp.SessionNotification{
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
func (a *exampleAgent) askHost(ctx context.Context, sid string, editID acp.ToolCallId) (string, error) {
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
		Session: &acp.ElicitationSessionScope{SessionId: acp.SessionId(sid), ToolCallId: acp.Ptr(editID)},
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
