package main

import (
	"context"
	"time"

	"github.com/coder/acp-go-sdk"
)

// spawn registers a child session of the turn's session and announces it,
// before any traffic names it. stop cancels the child's work.
func (t *turn) spawn(ctx context.Context, title string, cancellable bool, stop context.CancelFunc) (acp.SessionId, error) {
	cid := acp.SessionId(randomID("child_"))
	t.a.mu.Lock()
	t.a.children[cid] = stop
	t.a.mu.Unlock()
	u := &acp.SessionSubagentUpdate{
		SessionId: cid,
		Title:     acp.PatchSet(title),
		Cwd:       acp.PatchSet(t.cwd),
		State:     acp.PatchSet(acp.StateUpdate{Running: &acp.StateUpdateRunning{}}),
	}
	if cancellable {
		u.Capabilities = acp.PatchSet(acp.SubagentSessionCapabilities{Cancel: &acp.SessionCancelCapabilities{}})
	}
	return cid, t.tell(ctx, t.sid, acp.SessionUpdate{SubagentUpdate: u})
}

// idle reports that child cid stopped, for the reason given.
func (t *turn) idle(ctx context.Context, cid acp.SessionId, idle acp.StateUpdateIdle) error {
	state := acp.StateUpdate{Idle: &idle}
	return t.tell(ctx, t.sid, acp.SessionUpdate{SubagentUpdate: &acp.SessionSubagentUpdate{SessionId: cid, State: acp.PatchSet(state)}})
}

// delegate hands a task to a subagent that finishes it, for a client that
// accepts subagents. The parent sends the task, the child receives it,
// streams it, reports a select config option and its output, sends its
// result back, and stops; the parent's tool call holds the child's session
// reference.
func delegate(ctx context.Context, t *turn) (acp.StopReason, error) {
	if t.caps.Subagents == nil {
		return echo(ctx, t)
	}
	cid, err := t.spawn(ctx, "Worker", false, nil)
	if err != nil {
		return "", err
	}
	tool := acp.ToolCallId(t.id("delegate"))
	task := acp.TextBlock("Summarize the project.")
	result := acp.TextBlock("The project is a Go SDK for ACP.")
	sent, received := acp.MessageId(t.id("msg")), acp.MessageId(t.id("msg"))
	replied, reply := acp.MessageId(t.id("msg")), acp.MessageId(t.id("msg"))
	childModel := acp.SessionConfigSelectOptionsUngrouped{{Value: modelFast, Name: "Fast"}}
	steps := []struct {
		sid acp.SessionId
		u   acp.SessionUpdate
	}{
		{t.sid, acp.StartToolCall(tool, "Delegate to a subagent",
			acp.WithStartKind(acp.ToolKindOther),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
			acp.WithStartContent([]acp.ToolCallContent{{Session: &acp.ToolCallContentSession{SessionId: cid}}}))},
		{t.sid, acp.SessionUpdate{SessionMessage: &acp.SessionUpdateSessionMessage{
			MessageId: sent, SenderSessionId: acp.PatchSet(t.sid), RecipientSessionId: acp.PatchSet(cid),
			RecipientMessageId: acp.PatchSet(received), Content: acp.PatchSet([]acp.ContentBlock{task}),
		}}},
		{cid, acp.SessionUpdate{SessionMessage: &acp.SessionUpdateSessionMessage{
			MessageId: received, SenderSessionId: acp.PatchSet(t.sid), RecipientSessionId: acp.PatchSet(cid),
			SenderMessageId: acp.PatchSet(sent), Content: acp.PatchSet([]acp.ContentBlock{task}),
		}}},
		{cid, acp.SessionUpdate{SessionMessageChunk: &acp.SessionUpdateSessionMessageChunk{MessageId: received, Content: acp.TextBlock(" Keep it short.")}}},
		{cid, acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{ConfigOptions: []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
			Id: configModel, Name: "Model", Type: "select", CurrentValue: modelFast,
			Options: acp.SessionConfigSelectOptions{Ungrouped: &childModel},
		}}}}}},
		{cid, acp.UpdateAgentMessage(result)},
		{cid, acp.SessionUpdate{SessionMessage: &acp.SessionUpdateSessionMessage{
			MessageId: reply, SenderSessionId: acp.PatchSet(cid), RecipientSessionId: acp.PatchSet(t.sid),
			RecipientMessageId: acp.PatchSet(replied), Content: acp.PatchSet([]acp.ContentBlock{result}),
		}}},
		{t.sid, acp.SessionUpdate{SessionMessage: &acp.SessionUpdateSessionMessage{
			MessageId: replied, SenderSessionId: acp.PatchSet(cid), RecipientSessionId: acp.PatchSet(t.sid),
			SenderMessageId: acp.PatchSet(reply), Content: acp.PatchSet([]acp.ContentBlock{result}),
		}}},
	}
	for _, s := range steps {
		if err := t.tell(ctx, s.sid, s.u); err != nil {
			return "", err
		}
	}
	if err := t.idle(ctx, cid, acp.StateUpdateIdle{EndTurn: &acp.StateUpdateIdleEndTurn{}}); err != nil {
		return "", err
	}
	if err := t.say(ctx, acp.UpdateToolCall(tool, acp.WithUpdateStatus(acp.ToolCallStatusCompleted))); err != nil {
		return "", err
	}
	return acp.StopReasonEndTurn, t.say(ctx, acp.UpdateAgentMessageText("The subagent says: "+result.Text.Text))
}

// delegateCancellable hands a task to a cancellable subagent that runs
// until the client cancels it (session/cancel with the child's id) or the
// turn ends. The parent's turn runs on until it is cancelled itself, so a
// client can see that cancelling the child left the parent running.
func delegateCancellable(ctx context.Context, t *turn) (acp.StopReason, error) {
	if t.caps.Subagents == nil {
		return stream(ctx, t)
	}
	childCtx, stopChild := context.WithCancel(ctx)
	defer stopChild()
	cid, err := t.spawn(ctx, "Long worker", true, stopChild)
	if err != nil {
		return "", err
	}
	if err := t.say(ctx, acp.UpdateAgentMessageText("Waiting for the subagent.")); err != nil {
		return "", err
	}
	cancelled := acp.StateUpdateIdle{Cancelled: &acp.StateUpdateIdleCancelled{}}
	deadline := time.NewTimer(streamLimit)
	defer deadline.Stop()
	select {
	case <-childCtx.Done(): // the client cancelled the child, or the whole turn
	case <-deadline.C:
		stopChild()
		return acp.StopReasonEndTurn, t.idle(ctx, cid, cancelled)
	}
	// A cancelled turn stops its child too, and reports that before the
	// prompt's response.
	if err := t.idle(context.WithoutCancel(ctx), cid, cancelled); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return acp.StopReasonCancelled, nil
	case <-deadline.C:
		return acp.StopReasonEndTurn, nil
	}
}
