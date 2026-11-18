package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

// event is one session/update as the scenarios need it.
type event struct {
	Kind   string
	Text   string // text of a message or thought chunk
	Model  string // model id that a usage_update names in _meta
	Status string // status that a tool_call or tool_call_update sets
	// Decoded is a checkedKinds update as the SDK decoded it, encoded again.
	Decoded json.RawMessage
}

// permission is one session/request_permission and the answer it got.
type permission struct {
	Scenario string   `json:"scenario"`
	Conn     int      `json:"conn"`
	Title    string   `json:"title"`
	Options  []string `json:"options"`
	Answer   string   `json:"answer"`
}

// Permission policies: how the client answers session/request_permission.
const (
	answerAllow  = "allow"
	answerReject = "reject"
	answerCancel = "cancel"
)

// harnessClient is the client side of one connection. It answers
// permission requests by the current policy and keeps every session update
// so that a scenario can read what arrived during its own calls.
type harnessClient struct {
	h    *harness
	conn int

	mu     sync.Mutex
	policy string
	events map[acp.SessionId][]event
	costs  map[acp.SessionId]float64 // highest cumulative USD cost per session
	other  map[string]float64        // the same in other currencies, by "CUR session"
	tokens map[acp.SessionId]acp.Usage
	perms  []permission
	misc   []string // client methods the agent called that this client does not offer, with their scenario
	// elicit is the word that the client picks in a form elicitation. When
	// it is empty, the client cancels every elicitation.
	elicit       string
	elicitations []elicitation
	toolNames    []toolName // the name that the SDK decoded for each tool call
}

// elicitation is one elicitation/create or elicitation/complete from the
// agent, and the answer that the client gave.
type elicitation struct {
	Scenario string   `json:"scenario"`
	Conn     int      `json:"conn"`
	Method   string   `json:"method"`
	Mode     string   `json:"mode,omitempty"`
	Message  string   `json:"message,omitempty"`
	Fields   []string `json:"fields,omitempty"`
	Answer   string   `json:"answer,omitempty"`
	// decoded is the request as the SDK decoded it, encoded again. The
	// elicitation scenario compares it with the request on the wire.
	decoded json.RawMessage
}

var (
	_ acp.Client                         = (*harnessClient)(nil)
	_ acp.ClientUnstableAuthStatusUpdate = (*harnessClient)(nil)
)

func newHarnessClient(h *harness, conn int) *harnessClient {
	return &harnessClient{h: h, conn: conn, policy: answerCancel, events: map[acp.SessionId][]event{},
		costs: map[acp.SessionId]float64{}, other: map[string]float64{}, tokens: map[acp.SessionId]acp.Usage{}}
}

func (c *harnessClient) setElicit(word string) {
	c.mu.Lock()
	c.elicit = word
	c.mu.Unlock()
}

func (c *harnessClient) elicitationsSince(mark int) []elicitation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]elicitation(nil), c.elicitations[mark:]...)
}

// CreateElicitation answers a form with the field values that fillForm
// picks, or cancels when no word is set. It cancels every url elicitation,
// because the driver opens no browser.
func (c *harnessClient) CreateElicitation(_ context.Context, p acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error) {
	c.mu.Lock()
	word := c.elicit
	c.mu.Unlock()
	rec := elicitation{Scenario: c.h.rec.current(), Conn: c.conn, Method: "elicitation/create", Answer: "cancel"}
	rec.decoded, _ = json.Marshal(p)
	resp := acp.CreateElicitationResponse{Cancel: &acp.CreateElicitationCancel{Action: "cancel"}}
	switch {
	case p.Form != nil:
		rec.Mode, rec.Message = "form", p.Form.Message
		for name := range p.Form.RequestedSchema.Properties {
			rec.Fields = append(rec.Fields, name)
		}
		sort.Strings(rec.Fields)
		if content := fillForm(p.Form.RequestedSchema, word); word != "" && len(content) > 0 {
			resp = acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{Action: "accept", Content: content}}
			b, _ := json.Marshal(content)
			rec.Answer = "accept " + string(b)
		}
	case p.Url != nil:
		rec.Mode, rec.Message = "url", p.Url.Message
	case p.Other != nil:
		rec.Mode, rec.Message = p.Other.Mode, p.Other.Message
	}
	c.mu.Lock()
	c.elicitations = append(c.elicitations, rec)
	c.mu.Unlock()
	return resp, nil
}

// UnstableAuthStatusUpdate takes the agent's sign-in status, which Claude
// Code sends on every session/new, session/load and session/resume. The
// client shows no sign-in, so it keeps nothing.
func (c *harnessClient) UnstableAuthStatusUpdate(context.Context, acp.UnstableAuthStatusUpdateNotification) error {
	c.h.rec.handledOne(c.conn)
	return nil
}

// CompleteElicitation records the notification.
func (c *harnessClient) CompleteElicitation(_ context.Context, p acp.CompleteElicitationNotification) error {
	defer c.h.rec.handledOne(c.conn)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.elicitations = append(c.elicitations, elicitation{Scenario: c.h.rec.current(), Conn: c.conn,
		Method: "elicitation/complete", Answer: string(p.ElicitationId)})
	return nil
}

// fillForm answers each field of a form that offers choices with the choice
// whose value or title contains word, else with its first choice. It gives
// word itself to a required string field without choices, and leaves every
// other field out.
func fillForm(schema acp.ElicitationSchema, word string) map[string]acp.ElicitationContentValue {
	content := map[string]acp.ElicitationContentValue{}
	for name, field := range schema.Properties {
		if field.String == nil {
			continue
		}
		var choices []string
		for _, o := range field.String.OneOf {
			if strings.Contains(strings.ToLower(o.Const+" "+o.Title), strings.ToLower(word)) {
				choices = append([]string{o.Const}, choices...)
			} else {
				choices = append(choices, o.Const)
			}
		}
		for _, e := range field.String.Enum {
			if strings.Contains(strings.ToLower(e), strings.ToLower(word)) {
				choices = append([]string{e}, choices...)
			} else {
				choices = append(choices, e)
			}
		}
		switch {
		case len(choices) > 0:
			content[name] = acp.NewElicitationContentValueString(choices[0])
		case contains(schema.Required, name):
			content[name] = acp.NewElicitationContentValueString(word)
		}
	}
	return content
}

func (c *harnessClient) setPolicy(p string) {
	c.mu.Lock()
	c.policy = p
	c.mu.Unlock()
}

// mark returns a position in the session's updates for since.
func (c *harnessClient) mark(s acp.SessionId) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events[s])
}

func (c *harnessClient) since(s acp.SessionId, mark int) []event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]event(nil), c.events[s][mark:]...)
}

func (c *harnessClient) permissionsSince(mark int) []permission {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]permission(nil), c.perms[mark:]...)
}

func (c *harnessClient) cost() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0.0
	for _, v := range c.costs {
		total += v
	}
	return total
}

func (c *harnessClient) RequestPermission(_ context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.mu.Lock()
	policy := c.policy
	c.mu.Unlock()

	want := map[string][]acp.PermissionOptionKind{
		answerAllow:  {acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways},
		answerReject: {acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways},
	}[policy]
	rec := permission{Scenario: c.h.rec.current(), Conn: c.conn, Answer: "cancelled"}
	c.addToolName("session/request_permission", p.SessionId, p.ToolCall.ToolCallId, p.ToolCall.Name.Ptr())
	rec.Title, _ = p.ToolCall.Title.Value()
	for _, o := range p.Options {
		rec.Options = append(rec.Options, fmt.Sprintf("%s:%s", o.Kind, o.OptionId))
	}
	resp := acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}
pick:
	for _, kind := range want {
		for _, o := range p.Options {
			if o.Kind == kind {
				resp = acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: o.OptionId}}}
				rec.Answer = fmt.Sprintf("%s:%s", o.Kind, o.OptionId)
				break pick
			}
		}
	}
	c.mu.Lock()
	c.perms = append(c.perms, rec)
	c.mu.Unlock()
	return resp, nil
}

func (c *harnessClient) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	u := n.Update
	ev := event{Kind: updateKind(u)}
	if checkedKinds[ev.Kind] {
		ev.Decoded, _ = json.Marshal(u)
	}
	switch {
	case u.ToolCall != nil:
		c.addToolName("tool_call", n.SessionId, u.ToolCall.ToolCallId, u.ToolCall.Name)
	case u.ToolCallUpdate != nil:
		c.addToolName("tool_call_update", n.SessionId, u.ToolCallUpdate.ToolCallId, u.ToolCallUpdate.Name.Ptr())
	}
	switch {
	case u.AgentMessageChunk != nil:
		ev.Text = blockText(u.AgentMessageChunk.Content)
	case u.UserMessageChunk != nil:
		ev.Text = blockText(u.UserMessageChunk.Content)
	case u.AgentThoughtChunk != nil:
		ev.Text = blockText(u.AgentThoughtChunk.Content)
	case u.ToolCall != nil:
		ev.Status = string(u.ToolCall.Status)
	case u.ToolCallUpdate != nil && !u.ToolCallUpdate.Status.IsUnset():
		status, _ := u.ToolCallUpdate.Status.Value()
		ev.Status = string(status)
	case u.UsageUpdate != nil:
		if models := metaModels(u.UsageUpdate.Meta); len(models) > 0 {
			ev.Model = models[0]
		}
	}
	// Count the notification as routed only once its state is stored, so
	// that close never returns before the last update is in the summary.
	defer c.h.rec.handledOne(c.conn)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events[n.SessionId] = append(c.events[n.SessionId], ev)
	if u.UsageUpdate != nil && u.UsageUpdate.Cost != nil {
		cost := u.UsageUpdate.Cost
		if strings.EqualFold(cost.Currency, "USD") {
			c.costs[n.SessionId] = max(c.costs[n.SessionId], cost.Amount)
		} else {
			// The budget is in USD, so another currency is kept apart and
			// reported, never added to the USD total.
			key := strings.ToUpper(cost.Currency) + " " + string(n.SessionId)
			c.other[key] = max(c.other[key], cost.Amount)
		}
	}
	return nil
}

func (c *harnessClient) addToolName(where string, sid acp.SessionId, id acp.ToolCallId, name *string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolNames = append(c.toolNames, toolName{c.conn, where, string(sid), string(id), decodedName(name)})
}

func (c *harnessClient) decodedToolNames() []toolName {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]toolName(nil), c.toolNames...)
}

// usage records a prompt response's usage. The schema defines its fields
// as totals across the session's turns, so the highest value counts.
func (c *harnessClient) usage(sid acp.SessionId, u *acp.Usage) {
	if u == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.tokens[sid]
	t.InputTokens = max(t.InputTokens, u.InputTokens)
	t.OutputTokens = max(t.OutputTokens, u.OutputTokens)
	t.TotalTokens = max(t.TotalTokens, u.TotalTokens)
	c.tokens[sid] = t
}

// totals returns the usage of this connection's sessions and its costs in
// currencies other than USD.
func (c *harnessClient) totals() (map[acp.SessionId]acp.Usage, map[string]float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	usage := make(map[acp.SessionId]acp.Usage, len(c.tokens))
	for sid, t := range c.tokens {
		usage[sid] = t
	}
	other := map[string]float64{}
	for key, v := range c.other {
		other[strings.Fields(key)[0]] += v
	}
	return usage, other
}

// The client offers no file system and no terminal, so a well-behaved agent
// never calls these. A call is recorded, with its scenario, and refused.
// The no-refused-calls row fails on it.
func (c *harnessClient) refuse(method string) error {
	c.mu.Lock()
	c.misc = append(c.misc, method+" in "+c.h.rec.current())
	c.mu.Unlock()
	return fmt.Errorf("acpharness offers no %s", method)
}

func (c *harnessClient) refusedCalls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.misc...)
}

func (c *harnessClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, c.refuse("fs/read_text_file")
}

func (c *harnessClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, c.refuse("fs/write_text_file")
}

func (c *harnessClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, c.refuse("terminal/create")
}

func (c *harnessClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, c.refuse("terminal/kill")
}

func (c *harnessClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, c.refuse("terminal/output")
}

func (c *harnessClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, c.refuse("terminal/release")
}

func (c *harnessClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, c.refuse("terminal/wait_for_exit")
}

func updateKind(u acp.SessionUpdate) string {
	switch {
	case u.UserMessageChunk != nil:
		return "user_message_chunk"
	case u.AgentMessageChunk != nil:
		return "agent_message_chunk"
	case u.AgentThoughtChunk != nil:
		return "agent_thought_chunk"
	case u.ToolCall != nil:
		return "tool_call"
	case u.ToolCallUpdate != nil:
		return "tool_call_update"
	case u.Plan != nil:
		return "plan"
	case u.PlanUpdate != nil:
		return "plan_update"
	case u.PlanRemoved != nil:
		return "plan_removed"
	case u.AvailableCommandsUpdate != nil:
		return "available_commands_update"
	case u.CurrentModeUpdate != nil:
		return "current_mode_update"
	case u.ConfigOptionUpdate != nil:
		return "config_option_update"
	case u.SessionInfoUpdate != nil:
		return "session_info_update"
	case u.UsageUpdate != nil:
		return "usage_update"
	case u.Notice != nil:
		return "notice"
	case u.CompactionUpdate != nil:
		return "compaction_update"
	case u.CompactionSummaryChunk != nil:
		return "compaction_summary_chunk"
	}
	return "unknown"
}

func blockText(b acp.ContentBlock) string {
	if b.Text != nil {
		return b.Text.Text
	}
	return ""
}

// metaModels returns every model id in a _meta object: string values under a
// key named "model" or ending in "/model", at any depth. Claude names its
// model in usage_update as "_claude/model"; both adapters list
// quota.model_usage[].model in the prompt response.
func metaModels(meta acp.Meta) []string {
	seen := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				if s, ok := x.(string); ok && (k == "model" || strings.HasSuffix(k, "/model")) && s != "" {
					seen[s] = true
				}
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	for k, raw := range meta {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			walk(map[string]any{k: v})
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
