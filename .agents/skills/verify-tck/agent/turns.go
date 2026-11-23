package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"
)

// The prompt texts that acp-tck 0.5.0 sends for the rows these behaviors
// serve (src/tck/v1/conformance/test_permissions.py, test_plan.py,
// test_prompt.py and test_client_capabilities.py). A TCK that changes one
// sends the agent an echo instead, and the rows it served drift from the
// baseline.
const (
	permissionPrompt = "Create a file named tck-permission-probe.txt in the current working directory containing the word hello."
	planPrompt       = "Make a step-by-step plan for refactoring this project. Do not start on it yet."
	readmePrompt     = "Read the file README.md in the current directory and summarize it."
)

// prompts are the texts run.sh gives the TCK's --cancel-prompt,
// --subagent-prompt and --subagent-cancel-prompt.
type prompts struct {
	cancel, subagent, subagentCancel string
}

// behavior runs one turn. It returns how the turn stopped; Prompt reports a
// turn whose context was cancelled as cancelled whatever it returns.
type behavior func(ctx context.Context, t *turn) (acp.StopReason, error)

// behaviorsFor maps each prompt text the agent knows to its behavior. Any
// other prompt gets echo.
func behaviorsFor(p prompts) map[string]behavior {
	b := map[string]behavior{
		permissionPrompt: writeWithPermission,
		planPrompt:       makePlan,
		readmePrompt:     readREADME,
	}
	for text, run := range map[string]behavior{p.cancel: stream, p.subagent: delegate, p.subagentCancel: delegateCancellable} {
		if text != "" {
			b[text] = run
		}
	}
	return b
}

// The pace of a slow turn: a word every streamInterval, until the turn is
// cancelled or streamLimit passes. streamLimit stays below the TCK's turn
// deadline (run.sh's --timeout), so an uncancelled slow turn still ends.
const (
	streamInterval = 200 * time.Millisecond
	streamLimit    = 20 * time.Second
)

// turn is one prompt turn of a session.
type turn struct {
	a      *agent
	sid    acp.SessionId
	cwd    string
	caps   acp.ClientCapabilities
	prompt []acp.ContentBlock
}

// say sends an update of the turn's session and adds it to the session's
// history, so session/load replays it. It sends nothing once the turn is
// cancelled.
func (t *turn) say(ctx context.Context, u acp.SessionUpdate) error {
	if err := t.tell(ctx, t.sid, u); err != nil {
		return err
	}
	t.a.mu.Lock()
	if s := t.a.sessions[t.sid]; s != nil {
		s.history = append(s.history, u)
	}
	t.a.mu.Unlock()
	return nil
}

// tell sends an update of session sid that is not history: state, prompt
// suggestions, a subagent's traffic. It sends nothing once ctx is done.
func (t *turn) tell(ctx context.Context, sid acp.SessionId, u acp.SessionUpdate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return t.a.client().SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: u})
}

func (t *turn) id(prefix string) string {
	t.a.mu.Lock()
	defer t.a.mu.Unlock()
	return t.a.nextID(prefix)
}

func suggestions(texts ...string) acp.SessionUpdate {
	list := []acp.PromptSuggestion{}
	for _, s := range texts {
		list = append(list, acp.PromptSuggestion{Prompt: []acp.ContentBlock{acp.TextBlock(s)}})
	}
	return acp.SessionUpdate{PromptSuggestionsUpdate: &acp.SessionPromptSuggestionsUpdate{Suggestions: list}}
}

func (a *agent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	a.mu.Lock()
	s, err := a.lookup(p.SessionId, true)
	if err != nil {
		a.mu.Unlock()
		return acp.PromptResponse{}, err
	}
	if s.stopTurn != nil {
		s.stopTurn()
	}
	s.turn++
	n := s.turn
	s.stopTurn = stop
	s.updatedAt = time.Now()
	for _, b := range p.Prompt {
		s.history = append(s.history, acp.UpdateUserMessage(b))
	}
	t := &turn{a: a, sid: s.id, cwd: s.cwd, caps: a.caps, prompt: p.Prompt}
	suggest := a.caps.Session != nil && a.caps.Session.PromptSuggestions != nil
	clear := suggest && s.suggesting
	s.suggesting = false
	run := a.behaviors[promptText(p.Prompt)]
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if s.turn == n {
			s.stopTurn = nil
		}
		a.mu.Unlock()
	}()

	// A new prompt replaces the suggestions of the last turn.
	if clear {
		if err := t.tell(ctx, t.sid, suggestions()); err != nil && ctx.Err() == nil {
			return acp.PromptResponse{}, err
		}
	}
	if run == nil {
		run = echo
	}
	reason, err := run(ctx, t)
	if ctx.Err() != nil {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if suggest {
		if err := t.tell(ctx, t.sid, suggestions("What else can you do?")); err != nil {
			return acp.PromptResponse{}, err
		}
		a.mu.Lock()
		s.suggesting = true
		a.mu.Unlock()
	}
	return acp.PromptResponse{StopReason: reason}, nil
}

// Cancel stops a subagent the client cancels by its session id. The SDK
// cancels the context of a client session's turn itself.
func (a *agent) Cancel(ctx context.Context, p acp.CancelNotification) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if stop := a.children[p.SessionId]; stop != nil {
		stop()
	}
	return nil
}

// echo answers with what it received.
func echo(ctx context.Context, t *turn) (acp.StopReason, error) {
	return acp.StopReasonEndTurn, t.say(ctx, acp.UpdateAgentMessageText("You sent: "+describe(t.prompt)))
}

// stream echoes the prompt a word at a time until the turn is cancelled.
func stream(ctx context.Context, t *turn) (acp.StopReason, error) {
	words := strings.Fields(describe(t.prompt))
	if len(words) == 0 {
		words = []string{"..."}
	}
	limit := time.After(streamLimit)
	for i := 0; ; i++ {
		if err := t.say(ctx, acp.UpdateAgentMessageText(words[i%len(words)]+" ")); err != nil {
			return "", err
		}
		select {
		case <-ctx.Done():
			return acp.StopReasonCancelled, nil
		case <-limit:
			return acp.StopReasonEndTurn, nil
		case <-time.After(streamInterval):
		}
	}
}

// writeWithPermission asks before it writes the probe file, and writes it
// only when the client picks the allow option.
func writeWithPermission(ctx context.Context, t *turn) (acp.StopReason, error) {
	path := filepath.Join(t.cwd, "tck-permission-probe.txt")
	id := acp.ToolCallId(t.id("write"))
	const content = "hello\n"
	if err := t.say(ctx, acp.StartEditToolCall(id, "Write tck-permission-probe.txt", path, content)); err != nil {
		return "", err
	}
	const allow acp.PermissionOptionId = "allow"
	resp, err := t.a.client().RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: t.sid,
		ToolCall:  acp.ToolCallUpdate{ToolCallId: id, Title: acp.PatchSet("Write tck-permission-probe.txt")},
		Options: []acp.PermissionOption{
			{OptionId: allow, Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
		},
	})
	if err != nil {
		return "", err
	}
	// Any answer but the allow option, an unknown option id included, is
	// not approval.
	if sel := resp.Outcome.Selected; sel == nil || sel.OptionId != allow {
		if err := t.say(ctx, acp.UpdateToolCall(id, acp.WithUpdateStatus(acp.ToolCallStatusFailed))); err != nil {
			return "", err
		}
		return acp.StopReasonEndTurn, t.say(ctx, acp.UpdateAgentMessageText("I did not write the file."))
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	if err := t.say(ctx, acp.UpdateToolCall(id, acp.WithUpdateStatus(acp.ToolCallStatusCompleted))); err != nil {
		return "", err
	}
	return acp.StopReasonEndTurn, t.say(ctx, acp.UpdateAgentMessageText("I wrote "+path+"."))
}

// makePlan reports a plan: a markdown plan_update with an absolute path for
// a client that accepts plan updates, else the baseline plan entries.
func makePlan(ctx context.Context, t *turn) (acp.StopReason, error) {
	steps := []string{"Map the packages", "Extract the shared helpers", "Add tests for the helpers"}
	if t.caps.Plan != nil {
		path := filepath.Join(t.cwd, "PLAN.md")
		md := "# Refactoring plan\n\n1. " + strings.Join(steps, "\n1. ") + "\n"
		if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
			return "", err
		}
		plan := acp.NewPlanUpdateContentMarkdown(acp.PlanId(t.id("plan")), md)
		plan.Markdown.Path = acp.Ptr(path)
		if err := t.say(ctx, acp.SessionUpdate{PlanUpdate: &acp.SessionPlanUpdate{Plan: plan}}); err != nil {
			return "", err
		}
	} else {
		var entries []acp.PlanEntry
		for _, s := range steps {
			entries = append(entries, acp.PlanEntry{Content: s, Priority: acp.PlanEntryPriorityMedium, Status: acp.PlanEntryStatusPending})
		}
		if err := t.say(ctx, acp.UpdatePlan(entries...)); err != nil {
			return "", err
		}
	}
	return acp.StopReasonEndTurn, t.say(ctx, acp.UpdateAgentMessageText("Here is the plan. I have not started on it."))
}

// readREADME reads README.md from the session's directory itself, never
// through the client's fs methods, and reports the read as a tool call.
func readREADME(ctx context.Context, t *turn) (acp.StopReason, error) {
	path := filepath.Join(t.cwd, "README.md")
	id := acp.ToolCallId(t.id("read"))
	if err := t.say(ctx, acp.StartReadToolCall(id, "Read README.md", path)); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if err := t.say(ctx, acp.UpdateToolCall(id, acp.WithUpdateStatus(acp.ToolCallStatusFailed))); err != nil {
			return "", err
		}
		return acp.StopReasonEndTurn, t.say(ctx, acp.UpdateAgentMessageText("I could not read README.md."))
	}
	text := string(data)
	if err := t.say(ctx, acp.UpdateToolCall(id,
		acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
		acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(text))}))); err != nil {
		return "", err
	}
	return acp.StopReasonEndTurn, t.say(ctx, acp.UpdateAgentMessageText(fmt.Sprintf("README.md has %d lines.", strings.Count(text, "\n"))))
}
