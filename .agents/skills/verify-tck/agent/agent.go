package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
)

// capabilities is everything the agent advertises. It does not depend on
// the client's capabilities, because the TCK reads it from an initialize of
// its own with none. It leaves out what no v1 requirement of the pinned TCK
// checks: MCP over http, sse and acp, providers, session/status and
// session/set_title, which the SDK does support.
var capabilities = acp.AgentCapabilities{
	LoadSession: true,
	Auth: acp.AgentAuthCapabilities{
		Logout: &acp.LogoutCapabilities{},
		Status: &acp.AuthStatusCapabilities{},
	},
	PromptCapabilities: acp.PromptCapabilities{Image: true, Audio: true, EmbeddedContext: true},
	SessionCapabilities: acp.SessionCapabilities{
		AdditionalDirectories: &acp.SessionAdditionalDirectoriesCapabilities{},
		Archive:               &acp.SessionArchiveCapabilities{},
		Close:                 &acp.SessionCloseCapabilities{},
		Delete:                &acp.SessionDeleteCapabilities{},
		Fork:                  &acp.SessionForkCapabilities{},
		List:                  &acp.SessionListCapabilities{},
		Options:               &acp.SessionOptionsCapabilities{},
		Resume:                &acp.SessionResumeCapabilities{},
		// A system prompt in append mode, the one mode every agent with the
		// capability supports.
		SystemPrompt: &acp.SessionSystemPromptCapabilities{},
	},
}

// authMethod is the one way to log in. The login lives in memory, so the
// TCK's --auth-method tck --allow-logout revokes nothing real.
const authMethod acp.AuthMethodId = "tck"

// Each optional interface the agent implements, asserted so that a schema
// release that renames one breaks the build instead of turning the method
// into -32601 (method not found).
var (
	_ acp.Agent                         = (*agent)(nil)
	_ acp.AgentLoader                   = (*agent)(nil)
	_ acp.AgentUnstableAuthStatus       = (*agent)(nil)
	_ acp.AgentUnstableArchiveSession   = (*agent)(nil)
	_ acp.AgentUnstableUnarchiveSession = (*agent)(nil)
	_ acp.AgentUnstableForkSession      = (*agent)(nil)
	_ acp.AgentUnstableSessionOptions   = (*agent)(nil)
)

type agent struct {
	behaviors map[string]behavior

	// ready closes once conn is set; client waits for it.
	ready chan struct{}
	conn  *acp.AgentSideConnection

	// mu guards every field below and every field of each session. No
	// method holds it while it calls the client.
	mu sync.Mutex
	// caps are the client's capabilities from initialize.
	caps          acp.ClientCapabilities
	authenticated bool
	// loggedOut is set by a logout and cleared by authenticate. Between them
	// a new session gets -32000, as the logout contract asks.
	loggedOut bool
	sessions  map[acp.SessionId]*session
	// children are the subagent sessions the agent announced, each with
	// the func that cancels its work (nil for a child that cannot be
	// cancelled). The client did not create them and cannot address them.
	children map[acp.SessionId]context.CancelFunc
	// seq counts the sessions created, for session/list order, and ids
	// counts the tool call and message ids handed out.
	seq, ids int
}

type session struct {
	id        acp.SessionId
	seq       int
	cwd       string
	dirs      []string
	updatedAt time.Time
	archived  bool
	// open says the session is live on this connection: it takes prompts
	// and mode and config changes. A new, loaded, resumed or forked session
	// is open; close and archive end it.
	open      bool
	mode      acp.SessionModeId
	model     acp.SessionConfigValueId
	webSearch bool
	// history is what session/load replays: the user's prompts and the
	// agent's messages, tool calls and plans, in order.
	history []acp.SessionUpdate
	// stopTurn cancels the running turn; turn numbers it, so that a turn
	// clears stopTurn only when it is still its own.
	stopTurn context.CancelFunc
	turn     int
	// suggesting says a non-empty list of prompt suggestions is current.
	suggesting bool
	// mcp holds the stdin of each stdio MCP server the session started.
	mcp []*mcpServer
}

func newAgent(p prompts) *agent {
	return &agent{
		behaviors: behaviorsFor(p),
		ready:     make(chan struct{}),
		sessions:  make(map[acp.SessionId]*session),
		children:  make(map[acp.SessionId]context.CancelFunc),
	}
}

func (a *agent) connect(conn *acp.AgentSideConnection) {
	a.conn = conn
	close(a.ready)
}

func (a *agent) client() *acp.AgentSideConnection {
	<-a.ready
	return a.conn
}

// shutdown stops every MCP server the sessions started and waits until each
// has exited, at most a little longer than mcpStopGrace.
func (a *agent) shutdown() {
	a.mu.Lock()
	var exited []<-chan struct{}
	for _, s := range a.sessions {
		exited = append(exited, s.stopMCP()...)
	}
	a.mu.Unlock()
	deadline := time.After(mcpStopGrace + time.Second)
	for _, ch := range exited {
		select {
		case <-ch:
		case <-deadline:
			return
		}
	}
}

func notFound(id acp.SessionId) error {
	return acp.NewResourceNotFound(map[string]any{"sessionId": id})
}

func invalid(msg string, kv ...any) error {
	data := map[string]any{"error": msg}
	for i := 0; i+1 < len(kv); i += 2 {
		data[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return acp.NewInvalidParams(data)
}

// lookup returns the session a request names. A subagent's session is not
// the client's to address (-32602), and a session this process does not
// have gets -32002 (resource not found), as does a closed one when open is
// set. The caller holds a.mu.
func (a *agent) lookup(id acp.SessionId, open bool) (*session, error) {
	if _, ok := a.children[id]; ok {
		return nil, invalid("the session belongs to a subagent; the client cannot address it", "sessionId", id)
	}
	s := a.sessions[id]
	if s == nil || open && !s.open {
		return nil, notFound(id)
	}
	return s, nil
}

// lookupOrNone is lookup for close and delete, which succeed for a session
// that does not exist. s is nil then.
func (a *agent) lookupOrNone(id acp.SessionId) (*session, error) {
	if _, ok := a.children[id]; ok {
		return nil, invalid("the session belongs to a subagent; the client cannot address it", "sessionId", id)
	}
	return a.sessions[id], nil
}

func randomID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// nextID returns a new id for a tool call or a message. The caller holds
// a.mu.
func (a *agent) nextID(prefix string) string {
	a.ids++
	return fmt.Sprintf("%s-%d", prefix, a.ids)
}

// booleans says the client accepts boolean config options. The caller holds
// a.mu.
func (a *agent) booleans() bool {
	s := a.caps.Session
	return s != nil && s.ConfigOptions != nil && s.ConfigOptions.Boolean != nil
}

func (a *agent) Initialize(ctx context.Context, p acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.mu.Lock()
	a.caps = p.ClientCapabilities
	a.mu.Unlock()
	return acp.InitializeResponse{
		// The SDK speaks version 1 only, which is also the answer to a
		// client that asks for a version the agent does not know.
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentInfo:         &acp.Implementation{Name: "acp-go-sdk-tck-agent", Title: acp.Ptr("acp-go-sdk TCK conformance agent"), Version: "0.0.0"},
		AgentCapabilities: capabilities,
		AuthMethods: []acp.AuthMethod{{Agent: &acp.AuthMethodAgent{
			Id:          authMethod,
			Name:        "TCK",
			Description: acp.Ptr("Logs in in memory, for the conformance kit"),
		}}},
	}, nil
}

func (a *agent) Authenticate(ctx context.Context, p acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	if p.MethodId != authMethod {
		return acp.AuthenticateResponse{}, invalid("unknown auth method", "methodId", p.MethodId)
	}
	a.mu.Lock()
	a.authenticated = true
	a.loggedOut = false
	a.mu.Unlock()
	return acp.AuthenticateResponse{}, nil
}

func (a *agent) Logout(ctx context.Context, p acp.LogoutRequest) (acp.LogoutResponse, error) {
	a.mu.Lock()
	a.authenticated = false
	a.loggedOut = true
	a.mu.Unlock()
	return acp.LogoutResponse{}, nil
}

func (a *agent) UnstableAuthStatus(ctx context.Context, p acp.UnstableAuthStatusRequest) (acp.UnstableAuthStatusResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.authenticated {
		return acp.UnstableAuthStatusResponse{State: acp.UnstableAuthStateAuthenticated}, nil
	}
	return acp.UnstableAuthStatusResponse{State: acp.UnstableAuthStateUnauthenticated}, nil
}

// setup is what every request that opens a session (new, load, resume,
// fork) brings: the directories, the system prompt, the MCP servers and
// the initial config values.
type setup struct {
	cwd     string
	dirs    []string
	system  *acp.SystemPrompt
	mcp     []acp.McpServer
	initial []acp.InitialSessionConfigOption
}

// check rejects a setup the agent cannot apply.
func (st setup) check() error {
	if !filepath.IsAbs(st.cwd) {
		return invalid("cwd must be an absolute path", "cwd", st.cwd)
	}
	for _, d := range st.dirs {
		if !filepath.IsAbs(d) {
			return invalid("additionalDirectories entries must be absolute paths", "path", d)
		}
	}
	if err := checkSystemPrompt(st.system); err != nil {
		return err
	}
	return checkInitialConfig(st.initial)
}

// checkSystemPrompt rejects a system prompt the agent cannot apply: a mode
// other than append, no content, or a block that is not text.
func checkSystemPrompt(p *acp.SystemPrompt) error {
	if p == nil {
		return nil
	}
	if p.Mode != acp.SystemPromptModeAppend {
		return invalid(fmt.Sprintf("systemPrompt mode %q is not supported", p.Mode))
	}
	if len(p.Content) == 0 {
		return invalid("systemPrompt content is empty")
	}
	for _, b := range p.Content {
		if b.Text == nil {
			return invalid("systemPrompt content must be text blocks")
		}
	}
	return nil
}

// reopenable reports why session/load or session/resume cannot reopen s with
// st: the request's cwd must be the session's. The caller holds a.mu.
func reopenable(s *session, st setup) error {
	if st.cwd != s.cwd {
		return invalid("cwd does not match the session's cwd", "cwd", st.cwd)
	}
	return nil
}

// open applies st to s and opens it. The caller checked st and holds a.mu.
// additionalDirectories is the session's whole list, so an omitted one clears
// it. It returns the MCP servers to start once a.mu is released.
func (a *agent) open(s *session, st setup) []acp.McpServer {
	s.cwd = st.cwd
	s.dirs = st.dirs
	applyInitialConfig(s, st.initial, a.booleans())
	s.open = true
	s.updatedAt = time.Now()
	return st.mcp
}

// add creates an open session from st, with the config of from when set.
// The caller checked st and holds a.mu.
func (a *agent) add(st setup, from *session) (*session, []acp.McpServer) {
	a.seq++
	s := &session{id: acp.SessionId(randomID("sess_")), seq: a.seq, mode: modeAsk, model: modelFast}
	if from != nil {
		s.mode, s.model, s.webSearch = from.mode, from.model, from.webSearch
		s.history = slices.Clone(from.history)
	}
	servers := a.open(s, st)
	a.sessions[s.id] = s
	return s, servers
}

func (a *agent) NewSession(ctx context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	st := setup{p.Cwd, p.AdditionalDirectories, p.SystemPrompt, p.McpServers, p.ConfigOptions}
	if err := st.check(); err != nil {
		return acp.NewSessionResponse{}, err
	}
	a.mu.Lock()
	if a.loggedOut {
		a.mu.Unlock()
		return acp.NewSessionResponse{}, acp.NewAuthRequired(nil)
	}
	s, servers := a.add(st, nil)
	resp := acp.NewSessionResponse{SessionId: s.id, Modes: s.modes(), ConfigOptions: s.configOptions(a.booleans())}
	a.mu.Unlock()
	a.startMCP(s, servers)
	return resp, nil
}

func (a *agent) UnstableForkSession(ctx context.Context, p acp.UnstableForkSessionRequest) (acp.UnstableForkSessionResponse, error) {
	st := setup{p.Cwd, p.AdditionalDirectories, p.SystemPrompt, p.McpServers, p.ConfigOptions}
	if err := st.check(); err != nil {
		return acp.UnstableForkSessionResponse{}, err
	}
	// The agent issues no cursor, so the spec's -32602 for a cursorId it
	// never announced applies to every one.
	if p.CursorId != nil {
		return acp.UnstableForkSessionResponse{}, invalid("unknown cursorId: the agent never issues a cursor", "cursorId", *p.CursorId)
	}
	a.mu.Lock()
	if a.loggedOut {
		a.mu.Unlock()
		return acp.UnstableForkSessionResponse{}, acp.NewAuthRequired(nil)
	}
	from, err := a.lookup(p.SessionId, false)
	if err != nil {
		a.mu.Unlock()
		return acp.UnstableForkSessionResponse{}, err
	}
	s, servers := a.add(st, from)
	resp := acp.UnstableForkSessionResponse{SessionId: s.id, Modes: s.modes(), ConfigOptions: s.configOptions(a.booleans())}
	a.mu.Unlock()
	a.startMCP(s, servers)
	return resp, nil
}

// LoadSession replays the session's history before it answers, as the
// protocol asks. The agent keeps sessions in memory, so it can load only a
// session of this process.
func (a *agent) LoadSession(ctx context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	st := setup{p.Cwd, p.AdditionalDirectories, p.SystemPrompt, p.McpServers, p.ConfigOptions}
	if err := st.check(); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	a.mu.Lock()
	s, err := a.lookup(p.SessionId, false)
	if err == nil {
		err = reopenable(s, st)
	}
	if err != nil {
		a.mu.Unlock()
		return acp.LoadSessionResponse{}, err
	}
	history := slices.Clone(s.history)
	a.mu.Unlock()
	for _, u := range history {
		if err := a.client().SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: u}); err != nil {
			return acp.LoadSessionResponse{}, err
		}
	}
	a.mu.Lock()
	// A session/delete during the replay removed the session: answer as for
	// a session that does not exist, and start nothing for it.
	if cur, err := a.lookup(p.SessionId, false); err != nil || cur != s {
		a.mu.Unlock()
		if err == nil {
			err = notFound(p.SessionId)
		}
		return acp.LoadSessionResponse{}, err
	}
	servers := a.open(s, st)
	resp := acp.LoadSessionResponse{Modes: s.modes(), ConfigOptions: s.configOptions(a.booleans())}
	a.mu.Unlock()
	a.startMCP(s, servers)
	return resp, nil
}

func (a *agent) ResumeSession(ctx context.Context, p acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	st := setup{p.Cwd, p.AdditionalDirectories, p.SystemPrompt, p.McpServers, p.ConfigOptions}
	if err := st.check(); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	a.mu.Lock()
	s, err := a.lookup(p.SessionId, false)
	if err == nil {
		err = reopenable(s, st)
	}
	if err != nil {
		a.mu.Unlock()
		return acp.ResumeSessionResponse{}, err
	}
	servers := a.open(s, st)
	resp := acp.ResumeSessionResponse{Modes: s.modes(), ConfigOptions: s.configOptions(a.booleans())}
	a.mu.Unlock()
	a.startMCP(s, servers)
	return resp, nil
}

// UnstableSessionOptions returns the options a session would start with, or
// those of the session it names, and changes nothing.
func (a *agent) UnstableSessionOptions(ctx context.Context, p acp.UnstableSessionOptionsRequest) (acp.UnstableSessionOptionsResponse, error) {
	if err := (setup{cwd: p.Cwd, dirs: p.AdditionalDirectories}).check(); err != nil {
		return acp.UnstableSessionOptionsResponse{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := &session{mode: modeAsk, model: modelFast}
	if p.SessionId != nil {
		var err error
		if s, err = a.lookup(*p.SessionId, false); err != nil {
			return acp.UnstableSessionOptionsResponse{}, err
		}
		if err := reopenable(s, setup{cwd: p.Cwd}); err != nil {
			return acp.UnstableSessionOptionsResponse{}, err
		}
	}
	return acp.UnstableSessionOptionsResponse{Modes: s.modes(), ConfigOptions: s.configOptions(a.booleans())}, nil
}

// close ends the session's turn, stops its MCP servers and leaves it
// stored, so session/list still shows it and session/load reopens it. The
// caller holds a.mu.
func (s *session) close() {
	if s.stopTurn != nil {
		s.stopTurn()
		s.stopTurn = nil
	}
	s.stopMCP()
	s.open = false
}

func (a *agent) CloseSession(ctx context.Context, p acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, err := a.lookupOrNone(p.SessionId)
	if err != nil {
		return acp.CloseSessionResponse{}, err
	}
	if s != nil {
		s.close()
	}
	return acp.CloseSessionResponse{}, nil
}

func (a *agent) DeleteSession(ctx context.Context, p acp.DeleteSessionRequest) (acp.DeleteSessionResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, err := a.lookupOrNone(p.SessionId)
	if err != nil {
		return acp.DeleteSessionResponse{}, err
	}
	if s != nil {
		s.close()
		delete(a.sessions, s.id)
	}
	return acp.DeleteSessionResponse{}, nil
}

func (a *agent) UnstableArchiveSession(ctx context.Context, p acp.UnstableArchiveSessionRequest) (acp.UnstableArchiveSessionResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, err := a.lookup(p.SessionId, false)
	if err != nil {
		return acp.UnstableArchiveSessionResponse{}, err
	}
	s.close()
	s.archived = true
	return acp.UnstableArchiveSessionResponse{}, nil
}

func (a *agent) UnstableUnarchiveSession(ctx context.Context, p acp.UnstableUnarchiveSessionRequest) (acp.UnstableUnarchiveSessionResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, err := a.lookup(p.SessionId, false)
	if err != nil {
		return acp.UnstableUnarchiveSessionResponse{}, err
	}
	s.archived = false
	return acp.UnstableUnarchiveSessionResponse{}, nil
}

// archiveViews says which sessions each session/list archived filter shows.
var archiveViews = map[acp.SessionArchiveFilter]func(archived bool) bool{
	acp.SessionArchiveFilterExclude: func(archived bool) bool { return !archived },
	acp.SessionArchiveFilterInclude: func(bool) bool { return true },
	acp.SessionArchiveFilterOnly:    func(archived bool) bool { return archived },
}

// ListSessions returns the sessions in creation order on one page. It never
// issues a cursor, so every cursor is invalid.
func (a *agent) ListSessions(ctx context.Context, p acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	if p.Cursor != nil {
		return acp.ListSessionsResponse{}, invalid("unknown cursor: the agent never issues one", "cursor", *p.Cursor)
	}
	if p.Cwd != nil && !filepath.IsAbs(*p.Cwd) {
		return acp.ListSessionsResponse{}, invalid("cwd must be an absolute path", "cwd", *p.Cwd)
	}
	filter := acp.SessionArchiveFilterExclude
	if p.Archived != nil {
		filter = *p.Archived
	}
	shows, ok := archiveViews[filter]
	if !ok {
		return acp.ListSessionsResponse{}, invalid("unknown archived filter", "archived", filter)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var shown []*session
	for _, s := range a.sessions {
		if shows(s.archived) && (p.Cwd == nil || *p.Cwd == s.cwd) {
			shown = append(shown, s)
		}
	}
	slices.SortFunc(shown, func(x, y *session) int { return x.seq - y.seq })
	infos := []acp.SessionInfo{}
	for _, s := range shown {
		infos = append(infos, acp.SessionInfo{
			SessionId:             s.id,
			Cwd:                   s.cwd,
			AdditionalDirectories: s.dirs,
			UpdatedAt:             acp.Ptr(s.updatedAt.UTC().Format(time.RFC3339)),
			Archived:              acp.Ptr(s.archived),
		})
	}
	return acp.ListSessionsResponse{Sessions: infos}, nil
}

func (a *agent) SetSessionMode(ctx context.Context, p acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	a.mu.Lock()
	s, err := a.lookup(p.SessionId, true)
	if err == nil && !slices.ContainsFunc(modes, func(m acp.SessionMode) bool { return m.Id == p.ModeId }) {
		err = invalid("unknown mode", "modeId", p.ModeId)
	}
	if err != nil {
		a.mu.Unlock()
		return acp.SetSessionModeResponse{}, err
	}
	s.mode = p.ModeId
	a.mu.Unlock()
	update := acp.SessionUpdate{CurrentModeUpdate: &acp.SessionCurrentModeUpdate{CurrentModeId: p.ModeId}}
	if err := a.client().SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: update}); err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	return acp.SetSessionModeResponse{}, nil
}

func (a *agent) SetSessionConfigOption(ctx context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var id acp.SessionId
	switch {
	case p.ValueId != nil:
		id = p.ValueId.SessionId
	case p.Boolean != nil:
		id = p.Boolean.SessionId
	default:
		return acp.SetSessionConfigOptionResponse{}, invalid("no value")
	}
	s, err := a.lookup(id, true)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if p.ValueId != nil {
		err = s.setValue(p.ValueId.ConfigId, p.ValueId.Value)
	} else {
		err = s.setBoolean(p.Boolean.ConfigId, p.Boolean.Value, a.booleans())
	}
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: s.configOptions(a.booleans())}, nil
}

// promptText is the text the agent keys a prompt's behavior on: its first
// block's, when that is text.
func promptText(blocks []acp.ContentBlock) string {
	if len(blocks) > 0 && blocks[0].Text != nil {
		return blocks[0].Text.Text
	}
	return ""
}

// describe returns what the agent says it received, one item per block.
func describe(blocks []acp.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			parts = append(parts, b.Text.Text)
		case b.Image != nil:
			parts = append(parts, fmt.Sprintf("[an image, %s]", b.Image.MimeType))
		case b.Audio != nil:
			parts = append(parts, fmt.Sprintf("[audio, %s]", b.Audio.MimeType))
		case b.ResourceLink != nil:
			parts = append(parts, fmt.Sprintf("[a link to %s]", b.ResourceLink.Uri))
		case b.Resource != nil:
			parts = append(parts, "[an embedded resource]")
		}
	}
	return strings.Join(parts, " ")
}
