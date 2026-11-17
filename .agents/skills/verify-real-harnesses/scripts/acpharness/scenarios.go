package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// Prompts. Each one asks for a result that the client can observe.
const (
	pongPrompt   = "Reply with exactly the single word pong and nothing else. Do not use tools."
	recallPrompt = "Do not use tools. Earlier in this conversation you replied with a single word. Reply with only that word."
	essayPrompt  = "Do not use tools. Write a detailed essay of at least 3000 words on the history of computing, from the abacus to today."
	smokePrompt  = "Hello, agent!"
)

func writePrompt(file, marker string) string {
	return fmt.Sprintf("Create a new file named %s in the current working directory that contains exactly one line: %s. "+
		"Use your file editing tool, not a shell command. If the edit is rejected, do not retry and do not try "+
		"another way; reply with the single word rejected.", file, marker)
}

const rpcTimeout = 2 * time.Minute

// profiles lists the scenarios of each profile in the order they run.
// runLive and runSmoke must match it; -only accepts only these names.
var profiles = map[string][]string{
	"live": {"initialize", "authenticate", "session/new", "prompt", "config-option", "permission-allow",
		"permission-reject", "cancel", "session/list", "session/close", "session/delete", "session/load", "session/resume"},
	"smoke": {"initialize", "authenticate", "session/new", "prompt"},
}

// runLive runs every scenario. Connection 1 creates session A (prompt,
// config option, list, close) and session B (permissions, cancel, delete).
// Connections 2 and 3 are new agent processes that reopen session A, the
// way an editor does after a restart.
func (h *harness) runLive() {
	a, ok := h.connect()
	if !ok {
		return
	}
	h.step("session/new", func() (status, string) { return h.newSession(a, "A") })
	h.step("prompt", func() (status, string) { return h.plainPrompt(a) })
	h.step("config-option", func() (status, string) { return h.switchModel(a) })
	h.step("permission-allow", func() (status, string) { return h.permissionTurn(a, answerAllow) })
	h.step("permission-reject", func() (status, string) { return h.permissionTurn(a, answerReject) })
	h.step("cancel", func() (status, string) { return h.cancelTurn(a) })
	h.step("session/list", func() (status, string) { return h.listSessions(a) })
	h.step("session/close", func() (status, string) { return h.closeSession(a) })
	h.step("session/delete", func() (status, string) { return h.deleteSession(a) })
	a.close(10 * time.Second)
	h.step("session/load", h.loadSession)
	h.step("session/resume", h.resumeSession)
}

// runSmoke runs one prompt turn against an agent without a model.
func (h *harness) runSmoke() {
	a, ok := h.connect()
	if !ok {
		return
	}
	h.step("session/new", func() (status, string) { return h.newSession(a, "A") })
	h.step("prompt", func() (status, string) {
		sid, ok := h.sessions["A"]
		if !ok {
			return skip, "blocked: session/new failed"
		}
		a.client.setPolicy(answerAllow)
		defer a.client.setPolicy(answerCancel)
		t := h.prompt(a, sid, smokePrompt)
		switch {
		case t.err != nil:
			return fail, "session/prompt: " + errText(t.err)
		case t.resp.StopReason != acp.StopReasonEndTurn:
			return fail, "stopReason " + string(t.resp.StopReason) + "; " + t.describe()
		case t.kinds["agent_message_chunk"] == 0:
			return fail, "no agent_message_chunk; " + t.describe()
		case len(t.perms) == 0 || allowed(t.perms) < len(t.perms):
			// Both example agents ask before their edit, so the turn must carry
			// permission requests, and the client must allow each one.
			return fail, fmt.Sprintf("%d session/request_permission, %d allowed; %s", len(t.perms), allowed(t.perms), t.describe())
		}
		return pass, t.describe()
	})
}

func initRequest() acp.InitializeRequest {
	return acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{},
		ClientInfo:         &acp.Implementation{Name: "acpharness", Version: "0"},
	}
}

// connect starts connection 1 and runs initialize and authenticate as
// scenarios.
func (h *harness) connect() (*agentConn, bool) {
	a, err := h.startAgent()
	if err != nil {
		h.step("initialize", func() (status, string) { return fail, err.Error() })
		return nil, false
	}
	st := h.step("initialize", func() (status, string) {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		resp, err := a.conn.Initialize(ctx, initRequest())
		if err != nil {
			return fail, "initialize: " + errText(err)
		}
		h.init = resp
		if resp.ProtocolVersion != acp.ProtocolVersionNumber {
			return fail, fmt.Sprintf("protocolVersion %d, want %d", resp.ProtocolVersion, acp.ProtocolVersionNumber)
		}
		return pass, describeInit(resp)
	})
	if st != pass {
		return a, false
	}
	h.step("authenticate", func() (status, string) {
		if h.opts.auth == "" {
			return skip, "agent needs no authentication here (no -auth method for this adapter)"
		}
		ids := authIDs(h.init.AuthMethods)
		if !contains(ids, h.opts.auth) {
			return fail, fmt.Sprintf("agent does not advertise auth method %q; it advertises %v", h.opts.auth, ids)
		}
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		if _, err := a.conn.Authenticate(ctx, acp.AuthenticateRequest{MethodId: h.opts.auth}); err != nil {
			return fail, "authenticate: " + errText(err)
		}
		return pass, "methodId " + h.opts.auth
	})
	return a, true
}

// reconnect starts a new agent process for a later scenario and runs
// initialize and authenticate on it. The caller closes the connection.
func (h *harness) reconnect() (*agentConn, error) {
	a, err := h.startAgent()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	if _, err := a.conn.Initialize(ctx, initRequest()); err != nil {
		return a, fmt.Errorf("initialize on connection %d: %s", a.n, errText(err))
	}
	if h.opts.auth != "" {
		if _, err := a.conn.Authenticate(ctx, acp.AuthenticateRequest{MethodId: h.opts.auth}); err != nil {
			return a, fmt.Errorf("authenticate on connection %d: %s", a.n, errText(err))
		}
	}
	return a, nil
}

func (h *harness) newSession(a *agentConn, name string) (status, string) {
	cwd := filepath.Join(h.opts.cwd, strings.ToLower(name))
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return fail, err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	resp, err := a.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	if err != nil {
		return fail, "session/new: " + errText(err)
	}
	if resp.SessionId == "" {
		return fail, "session/new returned an empty sessionId"
	}
	h.sessions[name] = resp.SessionId
	h.cwds[name] = cwd
	h.configs[name] = resp.ConfigOptions
	h.modes[name] = resp.Modes
	return pass, fmt.Sprintf("session %s %s; config options: %s", name, resp.SessionId, describeOptions(resp.ConfigOptions))
}

// turn is what one session/prompt produced.
type turn struct {
	resp   acp.PromptResponse
	err    error
	ms     int64
	text   string // agent_message_chunk text
	kinds  map[string]int
	tools  map[string]int // tool call statuses
	models []string
	perms  []permission
}

func (t turn) describe() string {
	var kinds []string
	for k, n := range t.kinds {
		kinds = append(kinds, fmt.Sprintf("%s=%d", k, n))
	}
	sort.Strings(kinds)
	var tools []string
	for k, n := range t.tools {
		tools = append(tools, fmt.Sprintf("%s=%d", k, n))
	}
	sort.Strings(tools)
	return fmt.Sprintf("stopReason %s, reply %q, models %v, updates [%s], tool statuses [%s], %.1fs",
		t.resp.StopReason, clip(t.text, 60), t.models, strings.Join(kinds, " "), strings.Join(tools, " "), float64(t.ms)/1000)
}

func (h *harness) prompt(a *agentConn, sid acp.SessionId, text string) turn {
	mark, pmark := a.client.mark(sid), len(a.client.permissionsSince(0))
	ctx, cancel := context.WithTimeout(context.Background(), h.opts.turnTimeout)
	defer cancel()
	start := time.Now()
	resp, err := a.conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
	t := turn{resp: resp, err: err, ms: time.Since(start).Milliseconds()}
	h.collect(&t, a, sid, mark, pmark)
	return t
}

// collect fills a turn from the updates that arrived since mark, and adds
// its models and tokens to the run's totals.
func (h *harness) collect(t *turn, a *agentConn, sid acp.SessionId, mark, pmark int) {
	t.kinds, t.tools = map[string]int{}, map[string]int{}
	models := map[string]bool{}
	for _, ev := range a.client.since(sid, mark) {
		t.kinds[ev.Kind]++
		if ev.Status != "" {
			t.tools[ev.Status]++
		}
		if ev.Kind == "agent_message_chunk" {
			t.text += ev.Text
		}
		if ev.Model != "" {
			models[ev.Model] = true
		}
	}
	for _, m := range metaModels(t.resp.Meta) {
		models[m] = true
	}
	for m := range models {
		t.models = append(t.models, m)
		h.models[m] = true
	}
	sort.Strings(t.models)
	if u := t.resp.Usage; u != nil {
		h.tokens["input"] += u.InputTokens
		h.tokens["output"] += u.OutputTokens
		h.tokens["total"] += u.TotalTokens
	}
	t.perms = a.client.permissionsSince(pmark)
}

func (h *harness) plainPrompt(a *agentConn) (status, string) {
	sid, ok := h.sessions["A"]
	if !ok {
		return skip, "blocked: session/new failed"
	}
	t := h.prompt(a, sid, pongPrompt)
	switch {
	case t.err != nil:
		return fail, "session/prompt: " + errText(t.err)
	case t.resp.StopReason != acp.StopReasonEndTurn:
		return fail, t.describe()
	case !saysPong(t):
		return fail, "the reply does not say pong; " + t.describe()
	}
	h.defaultModels = t.models
	return pass, t.describe()
}

// switchModel sets the model config option to another model that the agent
// offers, proves the next turn runs on it, and sets the option back.
func (h *harness) switchModel(a *agentConn) (status, string) {
	sid, ok := h.sessions["A"]
	if !ok {
		return skip, "blocked: session/new failed"
	}
	sel := findSelect(h.configs["A"], acp.SessionConfigOptionCategoryModel, "model")
	if sel == nil {
		return skip, "session/new offered no model config option"
	}
	orig := sel.CurrentValue
	target, why := pickSwitchTarget(sel)
	if target == "" {
		return skip, "no other model to switch to: " + why
	}
	if err := h.setOption(a, sid, sel.Id, target); err != nil {
		return fail, fmt.Sprintf("set %s to %s: %v", sel.Id, target, err)
	}
	on := h.prompt(a, sid, pongPrompt)
	var off turn
	back := h.setOption(a, sid, sel.Id, orig)
	if back == nil {
		off = h.prompt(a, sid, pongPrompt)
	}
	detail := fmt.Sprintf("%s %s -> %s (%s) -> %s; wire models before %v, after the switch %v, after the switch back %v",
		sel.Id, orig, target, why, orig, h.defaultModels, on.models, off.models)
	switch {
	case on.err != nil:
		return fail, "session/prompt after the switch: " + errText(on.err) + "; " + detail
	case on.resp.StopReason != acp.StopReasonEndTurn || !saysPong(on):
		return fail, "turn after the switch: " + on.describe() + "; " + detail
	case back != nil:
		return fail, fmt.Sprintf("set %s back to %s: %v; %s", sel.Id, orig, back, detail)
	case off.err != nil:
		return fail, "session/prompt after the switch back: " + errText(off.err) + "; " + detail
	case off.resp.StopReason != acp.StopReasonEndTurn || !saysPong(off):
		return fail, "turn after the switch back: " + off.describe() + "; " + detail
	}
	if problem := switchProblem(h.defaultModels, on.models, off.models); problem != "" {
		return fail, problem + "; " + detail
	}
	return pass, detail
}

// allowed counts the requests that got an allow_* option.
func allowed(perms []permission) int {
	n := 0
	for _, p := range perms {
		if strings.HasPrefix(p.Answer, answerAllow+"_") {
			n++
		}
	}
	return n
}

func saysPong(t turn) bool { return strings.Contains(strings.ToLower(t.text), "pong") }

// switchProblem says why the model ids on the wire do not prove a model
// switch and the switch back, or returns "" when they do. before, on and off
// are the models of the turns before the switch, after it, and after the
// switch back.
func switchProblem(before, on, off []string) string {
	added := minus(on, before)
	switch {
	case len(before) == 0 || len(on) == 0 || len(off) == 0:
		return "a turn named no model id on the wire, so the switch is not proven"
	case len(added) == 0:
		return "the turn after the switch ran on no new model"
	case len(minus(added, off)) < len(added):
		return "the turn after the switch back still ran on the switched model"
	}
	return ""
}

// setOption sets a select config option and checks that the response
// reports the new value.
func (h *harness) setOption(a *agentConn, sid acp.SessionId, id acp.SessionConfigId, value acp.SessionConfigValueId) error {
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	resp, err := a.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: sid, ConfigId: id, Value: value,
	}})
	if err != nil {
		return errors.New(errText(err))
	}
	for _, o := range resp.ConfigOptions {
		if o.Select != nil && o.Select.Id == id {
			if o.Select.CurrentValue != value {
				return fmt.Errorf("response reports currentValue %s", o.Select.CurrentValue)
			}
			return nil
		}
	}
	return fmt.Errorf("response has no config option %s", id)
}

// ensureB creates session B, the one that asks before edits, on first use.
func (h *harness) ensureB(a *agentConn) (acp.SessionId, error) {
	if sid, ok := h.sessions["B"]; ok {
		return sid, nil
	}
	if st, detail := h.newSession(a, "B"); st != pass {
		return "", errors.New(detail)
	}
	sid := h.sessions["B"]
	if h.opts.askMode == "" {
		return sid, nil
	}
	if sel := findSelect(h.configs["B"], acp.SessionConfigOptionCategoryMode, "mode"); sel != nil {
		if err := h.setOption(a, sid, sel.Id, acp.SessionConfigValueId(h.opts.askMode)); err != nil {
			return "", fmt.Errorf("set mode %s: %v", h.opts.askMode, err)
		}
		return sid, nil
	}
	if h.modes["B"] == nil {
		return "", fmt.Errorf("agent offers no mode, so -ask-mode %s cannot apply", h.opts.askMode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	if _, err := a.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: sid, ModeId: acp.SessionModeId(h.opts.askMode)}); err != nil {
		return "", fmt.Errorf("session/set_mode %s: %s", h.opts.askMode, errText(err))
	}
	return sid, nil
}

// permissionTurn asks the agent to write a file in session B's cwd and
// answers every permission request with policy. The file decides.
func (h *harness) permissionTurn(a *agentConn, policy string) (status, string) {
	sid, err := h.ensureB(a)
	if err != nil {
		return fail, "session B: " + err.Error()
	}
	file, marker := policy+"-check.txt", "harness-"+policy+"-ok"
	path := filepath.Join(h.cwds["B"], file)
	_ = os.Remove(path)
	a.client.setPolicy(policy)
	t := h.prompt(a, sid, writePrompt(file, marker))
	a.client.setPolicy(answerCancel)

	body, readErr := os.ReadFile(path)
	exists := readErr == nil
	var answers []string
	answered := 0
	for _, p := range t.perms {
		answers = append(answers, p.Answer)
		if strings.HasPrefix(p.Answer, policy+"_") {
			answered++
		}
	}
	detail := fmt.Sprintf("mode %q; %d permission request(s), answers %v; %s exists=%v; %s",
		h.opts.askMode, len(t.perms), answers, file, exists, t.describe())
	switch {
	case t.err != nil:
		return fail, "session/prompt: " + errText(t.err) + "; " + detail
	case len(t.perms) == 0:
		return fail, "no session/request_permission arrived; " + detail
	case answered < len(t.perms):
		return fail, "a request offered no " + policy + " option, so the client cancelled it; " + detail
	case policy == answerAllow && (!exists || !strings.Contains(string(body), marker)):
		return fail, "the file is missing or lacks the marker after allow; " + detail
	case policy == answerReject && exists:
		return fail, "the file exists after reject; " + detail
	}
	return pass, detail
}

// cancelTurn starts a long turn in session B, sends session/cancel once
// the agent streams, and expects stopReason cancelled.
func (h *harness) cancelTurn(a *agentConn) (status, string) {
	sid, err := h.ensureB(a)
	if err != nil {
		return fail, "session B: " + err.Error()
	}
	mark, pmark := a.client.mark(sid), len(a.client.permissionsSince(0))
	ctx, cancel := context.WithTimeout(context.Background(), h.opts.turnTimeout)
	defer cancel()
	type outcome struct {
		resp acp.PromptResponse
		err  error
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		resp, err := a.conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock(essayPrompt)}})
		done <- outcome{resp, err}
	}()

	first := ""
	deadline := time.Now().Add(90 * time.Second)
wait:
	for time.Now().Before(deadline) {
		select {
		case o := <-done:
			t := turn{resp: o.resp, err: o.err, ms: time.Since(start).Milliseconds()}
			h.collect(&t, a, sid, mark, pmark)
			return fail, "the turn ended before any streamed update, so there was nothing to cancel; " + t.describe()
		case <-time.After(100 * time.Millisecond):
		}
		for _, ev := range a.client.since(sid, mark) {
			switch ev.Kind {
			case "agent_message_chunk", "agent_thought_chunk", "tool_call", "plan":
				first = ev.Kind
				break wait
			}
		}
	}
	if first == "" {
		first = "none within 90s"
	}
	sentAt := time.Since(start)
	if err := a.conn.Cancel(context.Background(), acp.CancelNotification{SessionId: sid}); err != nil {
		return fail, "session/cancel: " + err.Error()
	}
	var o outcome
	select {
	case o = <-done:
	case <-time.After(60 * time.Second):
		cancel()
		o = <-done
		return fail, fmt.Sprintf("no session/prompt response within 60s of session/cancel (sent at %.1fs after the first %s)", sentAt.Seconds(), first)
	}
	t := turn{resp: o.resp, err: o.err, ms: time.Since(start).Milliseconds()}
	h.collect(&t, a, sid, mark, pmark)
	detail := fmt.Sprintf("session/cancel sent %.1fs into the turn after the first %s, response %.1fs later; %s",
		sentAt.Seconds(), first, (time.Duration(t.ms)*time.Millisecond - sentAt).Seconds(), t.describe())
	switch {
	case t.err != nil:
		return fail, "session/prompt: " + errText(t.err) + "; " + detail
	case t.resp.StopReason != acp.StopReasonCancelled:
		return fail, detail
	}
	return pass, detail
}

func (h *harness) listSessions(a *agentConn) (status, string) {
	if h.init.AgentCapabilities.SessionCapabilities.List == nil {
		return skip, "agent does not advertise sessionCapabilities.list"
	}
	sid, ok := h.sessions["A"]
	if !ok {
		return skip, "blocked: session/new failed"
	}
	found, n, err := listContains(a, h.cwds["A"], sid)
	if err != nil {
		return fail, err.Error()
	}
	if !found {
		return fail, fmt.Sprintf("session A is not among the %d sessions listed for its cwd", n)
	}
	return pass, fmt.Sprintf("session A is among the %d sessions listed for its cwd", n)
}

// listContains pages through session/list for cwd and looks for sid.
func listContains(a *agentConn, cwd string, sid acp.SessionId) (bool, int, error) {
	n := 0
	var cursor *string
	for page := 0; page < 50; page++ {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		resp, err := a.conn.ListSessions(ctx, acp.ListSessionsRequest{Cwd: &cwd, Cursor: cursor})
		cancel()
		if err != nil {
			return false, n, errors.New("session/list: " + errText(err))
		}
		for _, s := range resp.Sessions {
			n++
			if s.SessionId == sid {
				return true, n, nil
			}
		}
		if resp.NextCursor == nil || *resp.NextCursor == "" {
			return false, n, nil
		}
		cursor = resp.NextCursor
	}
	return false, n, errors.New("session/list: more than 50 pages")
}

func (h *harness) closeSession(a *agentConn) (status, string) {
	if h.init.AgentCapabilities.SessionCapabilities.Close == nil {
		return skip, "agent does not advertise sessionCapabilities.close"
	}
	sid, ok := h.sessions["A"]
	if !ok {
		return skip, "blocked: session/new failed"
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	if _, err := a.conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: sid}); err != nil {
		return fail, "session/close: " + errText(err)
	}
	return pass, "closed session A"
}

func (h *harness) deleteSession(a *agentConn) (status, string) {
	caps := h.init.AgentCapabilities.SessionCapabilities
	if caps.Delete == nil {
		return skip, "agent does not advertise sessionCapabilities.delete"
	}
	sid, ok := h.sessions["B"]
	if !ok {
		return skip, "blocked: session B was never created"
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	note := ""
	if caps.Close != nil {
		if _, err := a.conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: sid}); err != nil {
			note = "; session/close of B before the delete failed: " + errText(err)
		}
	}
	if _, err := a.conn.UnstableDeleteSession(ctx, acp.UnstableDeleteSessionRequest{SessionId: sid}); err != nil {
		return fail, "session/delete: " + errText(err) + note
	}
	if caps.List == nil {
		return pass, "deleted session B; no session/list to confirm" + note
	}
	found, n, err := listContains(a, h.cwds["B"], sid)
	switch {
	case err != nil:
		return fail, err.Error() + note
	case found:
		return fail, fmt.Sprintf("session B is still listed after session/delete (%d sessions)%s", n, note)
	}
	return pass, "deleted session B; session/list for its cwd no longer has it" + note
}

// loadSession reopens session A in a new agent process and expects the
// agent to replay the conversation before it answers.
func (h *harness) loadSession() (status, string) {
	if !h.init.AgentCapabilities.LoadSession {
		return skip, "agent does not advertise loadSession"
	}
	sid, ok := h.sessions["A"]
	if !ok {
		return skip, "blocked: session/new failed"
	}
	b, err := h.reconnect()
	if b != nil {
		defer b.close(10 * time.Second)
	}
	if err != nil {
		return fail, err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	if _, err := b.conn.LoadSession(ctx, acp.LoadSessionRequest{SessionId: sid, Cwd: h.cwds["A"], McpServers: []acp.McpServer{}}); err != nil {
		return fail, "session/load: " + errText(err)
	}
	evs := b.client.since(sid, 0)
	detail := fmt.Sprintf("connection %d replayed %d updates before the response", b.n, len(evs))
	if problem := replayProblem(evs); problem != "" {
		return fail, problem + "; " + detail
	}
	return pass, detail
}

// replayProblem says why a session/load replay does not bring back the
// earlier pong turn, or returns "" when it does. The prompt itself says
// pong, so only the agent's replayed reply counts.
func replayProblem(evs []event) string {
	users, agents, reply := 0, 0, ""
	for _, ev := range evs {
		switch ev.Kind {
		case "user_message_chunk":
			users++
		case "agent_message_chunk":
			agents++
			reply += ev.Text
		}
	}
	if users == 0 || !strings.Contains(strings.ToLower(reply), "pong") {
		return fmt.Sprintf("the replay lacks the earlier pong turn (%d user chunks, %d agent chunks, agent text %q)", users, agents, clip(reply, 60))
	}
	return ""
}

// resumeSession reopens session A in a new agent process without a replay
// and asks the agent what it said before.
func (h *harness) resumeSession() (status, string) {
	caps := h.init.AgentCapabilities.SessionCapabilities
	if caps.Resume == nil {
		return skip, "agent does not advertise sessionCapabilities.resume"
	}
	sid, ok := h.sessions["A"]
	if !ok {
		return skip, "blocked: session/new failed"
	}
	c, err := h.reconnect()
	if c != nil {
		defer c.close(10 * time.Second)
	}
	if err != nil {
		return fail, err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
	defer cancel()
	if _, err := c.conn.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: sid, Cwd: h.cwds["A"]}); err != nil {
		return fail, "session/resume: " + errText(err)
	}
	// session/resume restores a session without its history, so no message
	// chunk may arrive before the first prompt.
	replayed, messages := 0, 0
	for _, ev := range c.client.since(sid, 0) {
		replayed++
		if ev.Kind == "user_message_chunk" || ev.Kind == "agent_message_chunk" {
			messages++
		}
	}
	t := h.prompt(c, sid, recallPrompt)
	if caps.Close != nil {
		_, _ = c.conn.CloseSession(ctx, acp.CloseSessionRequest{SessionId: sid})
	}
	detail := fmt.Sprintf("connection %d, %d updates and %d message chunks before the first prompt; recall turn: %s", c.n, replayed, messages, t.describe())
	switch {
	case messages > 0:
		return fail, "session/resume replayed the conversation, which only session/load may do; " + detail
	case t.err != nil:
		return fail, "session/prompt: " + errText(t.err) + "; " + detail
	case t.resp.StopReason != acp.StopReasonEndTurn:
		return fail, "the recall turn did not end with end_turn; " + detail
	case !strings.Contains(strings.ToLower(t.text), "pong"):
		return fail, "the resumed session does not remember pong; " + detail
	}
	return pass, detail
}

func errText(err error) string {
	var re *acp.RequestError
	if errors.As(err, &re) {
		return clip(re.Error(), 300)
	}
	return clip(err.Error(), 300)
}

func describeInit(r acp.InitializeResponse) string {
	name := "agent without agentInfo"
	if r.AgentInfo != nil {
		name = r.AgentInfo.Name + " " + r.AgentInfo.Version
	}
	c := r.AgentCapabilities
	var caps []string
	if c.LoadSession {
		caps = append(caps, "loadSession")
	}
	for _, sc := range []struct {
		name string
		on   bool
	}{
		{"list", c.SessionCapabilities.List != nil},
		{"resume", c.SessionCapabilities.Resume != nil},
		{"close", c.SessionCapabilities.Close != nil},
		{"delete", c.SessionCapabilities.Delete != nil},
		{"fork", c.SessionCapabilities.Fork != nil},
	} {
		if sc.on {
			caps = append(caps, "session."+sc.name)
		}
	}
	return fmt.Sprintf("%s, protocol %d, auth methods %v, capabilities %v", name, r.ProtocolVersion, authIDs(r.AuthMethods), caps)
}

func authIDs(ms []acp.AuthMethod) []string {
	ids := []string{}
	for _, m := range ms {
		switch {
		case m.Agent != nil:
			ids = append(ids, m.Agent.Id)
		case m.EnvVar != nil:
			ids = append(ids, m.EnvVar.Id)
		case m.Terminal != nil:
			ids = append(ids, m.Terminal.Id)
		}
	}
	return ids
}

func describeOptions(cfg []acp.SessionConfigOption) string {
	var out []string
	for _, o := range cfg {
		switch {
		case o.Select != nil:
			out = append(out, fmt.Sprintf("%s=%s", o.Select.Id, o.Select.CurrentValue))
		case o.Boolean != nil:
			out = append(out, fmt.Sprintf("%s=%v", o.Boolean.Id, o.Boolean.CurrentValue))
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, " ")
}

func findSelect(cfg []acp.SessionConfigOption, cat acp.SessionConfigOptionCategory, id string) *acp.SessionConfigOptionSelect {
	for _, o := range cfg {
		if s := o.Select; s != nil && ((s.Category != nil && *s.Category == cat) || string(s.Id) == id) {
			return s
		}
	}
	return nil
}

func selectOptions(s *acp.SessionConfigOptionSelect) []acp.SessionConfigSelectOption {
	switch {
	case s.Options.Ungrouped != nil:
		return *s.Options.Ungrouped
	case s.Options.Grouped != nil:
		var out []acp.SessionConfigSelectOption
		for _, g := range *s.Options.Grouped {
			out = append(out, g.Options...)
		}
		return out
	}
	return nil
}

// lesserMarkers are words in a model option's name or description that
// mark a cheaper, faster or older model. The switch never picks one.
var lesserMarkers = []string{"legacy", "older", "previous", "fast", "affordable", "efficient", "routine", "quick", "cheap", "mini", "lite", "small"}

// pickSwitchTarget returns the first model, in the agent's order, that is
// not the current one, not an alias of it, and not a lesser model. It
// returns "" and the reason when there is none.
func pickSwitchTarget(s *acp.SessionConfigOptionSelect) (acp.SessionConfigValueId, string) {
	opts := selectOptions(s)
	curText := string(s.CurrentValue)
	for _, o := range opts {
		if o.Value == s.CurrentValue {
			curText = strings.ToLower(o.Name + " " + deref(o.Description))
		}
	}
	var skipped []string
	for _, o := range opts {
		v := strings.ToLower(string(o.Value))
		switch {
		case o.Value == s.CurrentValue:
			continue
		case v == "default":
			skipped = append(skipped, string(o.Value)+" (the default alias)")
		case strings.Contains(curText, v):
			skipped = append(skipped, string(o.Value)+" (alias of the current model)")
		case isLesser(o.Name + " " + deref(o.Description)):
			skipped = append(skipped, string(o.Value)+" (a lesser model)")
		default:
			return o.Value, fmt.Sprintf("first of %d offered that is not current, an alias or a lesser model", len(opts))
		}
	}
	return "", fmt.Sprintf("current %s; skipped %v", s.CurrentValue, skipped)
}

func isLesser(text string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		for _, m := range lesserMarkers {
			if strings.HasPrefix(w, m) {
				return true
			}
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// minus returns the elements of a that are not in b.
func minus(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}
