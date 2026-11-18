package main

// The checks in this file compare what an agent sent on the wire with what
// the SDK decoded: the tool call name (stable in schema 1.22.0), and the
// notice and compaction session updates (stable in 1.25.0).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// checkedKinds are the session updates whose raw JSON the recorder keeps
// and whose decoded form the client encodes again, so that a scenario can
// compare the two.
var checkedKinds = map[string]bool{"notice": true, "compaction_update": true, "compaction_summary_chunk": true}

// toolName is the name of one tool call as the wire or the SDK has it.
type toolName struct {
	Conn    int
	Where   string // tool_call, tool_call_update or session/request_permission
	Session string
	ID      string // toolCallId
	Name    string // the name as JSON text, "" when absent or null
}

// Where a tool call name can be, in the order that the detail lists them.
var toolNamePlaces = []string{"tool_call", "tool_call_update", "session/request_permission"}

// wireName returns a raw name as the SDK encodes a string, or "" when it is
// absent or null. A name that is not a string stays as it is, so no decoded
// name can equal it.
func wireName(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return string(raw)
	}
	return string(mustMarshal(s))
}

func decodedName(n *string) string {
	if n == nil {
		return ""
	}
	return string(mustMarshal(*n))
}

// checkToolNames compares the name of every tool call on the wire with the
// name that the SDK decoded for it. It passes when each one matches and at
// least one carries a name. With -only, a run without any tool call skips,
// and with -sends-none tool-call-name, so does a run without a name.
func checkToolNames(wire, decoded []toolName, only, sendsNone bool) (status, string) {
	type names struct{ wire, decoded []string }
	byCall := map[string]*names{}
	at := func(t toolName) *names {
		k := fmt.Sprintf("connection %d %s %s in session %s", t.Conn, t.Where, t.ID, t.Session)
		if byCall[k] == nil {
			byCall[k] = &names{}
		}
		return byCall[k]
	}
	named, total, distinct := map[string]int{}, map[string]int{}, map[string]bool{}
	for _, t := range wire {
		n := at(t)
		n.wire = append(n.wire, t.Name)
		total[t.Where]++
		if t.Name != "" {
			named[t.Where]++
			distinct[t.Name] = true
		}
	}
	for _, t := range decoded {
		n := at(t)
		n.decoded = append(n.decoded, t.Name)
	}
	keys := make([]string, 0, len(byCall))
	for k := range byCall {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n := byCall[k]
		sort.Strings(n.wire)
		sort.Strings(n.decoded)
		if !reflect.DeepEqual(n.wire, n.decoded) {
			return fail, fmt.Sprintf("decode mismatch: %s has the name(s) [%s] on the wire, but the SDK decoded [%s]", k, showNames(n.wire), showNames(n.decoded))
		}
	}
	var counts []string
	for _, w := range toolNamePlaces {
		counts = append(counts, fmt.Sprintf("%d of %d %s", named[w], total[w], w))
	}
	detail := "names on " + strings.Join(counts, ", ")
	if len(wire) == 0 && only {
		return skip, skipNotSelected + ": no selected scenario made a tool call"
	}
	switch {
	case len(distinct) == 0 && sendsNone:
		return skip, skipSendsNone + ": -sends-none tool-call-name, and no tool call on the wire carried a name; " + detail
	case len(distinct) == 0:
		return fail, "no tool call on the wire carried a name; " + detail
	}
	list := make([]string, 0, len(distinct))
	for n := range distinct {
		list = append(list, n)
	}
	sort.Strings(list)
	detail = fmt.Sprintf("%s, each decoded as on the wire; %d distinct: %s", detail, len(list), clip(strings.Join(list, " "), 200))
	if sendsNone {
		detail += "; the agent sent a name despite -sends-none tool-call-name, so drop that flag from the adapter table"
	}
	return pass, detail
}

func showNames(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n
		if n == "" {
			out[i] = "absent"
		}
	}
	return strings.Join(out, ", ")
}

// kindOf returns the sessionUpdate discriminator of a raw update.
func kindOf(raw json.RawMessage) string {
	var u struct {
		Kind string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(raw, &u)
	return u.Kind
}

// matchDecoded checks that the updates the SDK decoded, encoded again, are
// the updates on the wire. The client and the recorder can see them in
// another order, so they match as a multiset.
func matchDecoded(decoded, wire []json.RawMessage) error {
	if len(decoded) != len(wire) {
		return fmt.Errorf("decode mismatch: %d update(s) on the wire, the SDK delivered %d", len(wire), len(decoded))
	}
	left := append([]json.RawMessage(nil), wire...)
next:
	for _, d := range decoded {
		for i, w := range left {
			if sameJSON(d, w) == nil {
				left = append(left[:i], left[i+1:]...)
				continue next
			}
		}
		return fmt.Errorf("decode mismatch: the SDK decoded %s, and no update on the wire matches it; the wire has %s",
			clip(string(d), 300), clip(string(left[0]), 300))
	}
	return nil
}

// sent waits until the SDK has routed what the agent on a sent, and returns
// the updates of the given kinds that session sid got on the wire since the
// marks. It fails with a decode mismatch when the SDK decoded them otherwise.
func (h *harness) sent(a *agentConn, sid acp.SessionId, emark, wmark int, kinds ...string) ([]json.RawMessage, error) {
	for deadline := time.Now().Add(5 * time.Second); h.rec.pending(a.n) > 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	var decoded, wire []json.RawMessage
	for _, ev := range a.client.since(sid, emark) {
		if contains(kinds, ev.Kind) {
			decoded = append(decoded, ev.Decoded)
		}
	}
	for _, w := range h.rec.updatesOnWire(a.n, sid)[wmark:] {
		if contains(kinds, kindOf(w)) {
			wire = append(wire, w)
		}
	}
	return wire, matchDecoded(decoded, wire)
}

// describeNotices lists each raw notice as severity "title", with a mark
// when it has a description.
func describeNotices(wire []json.RawMessage) string {
	var out []string
	for _, w := range wire {
		var n struct {
			Severity    string  `json:"severity"`
			Title       string  `json:"title"`
			Description *string `json:"description"`
		}
		_ = json.Unmarshal(w, &n)
		s := fmt.Sprintf("%s %q", n.Severity, clip(n.Title, 120))
		if n.Description != nil {
			s += fmt.Sprintf(" with description %q", clip(*n.Description, 120))
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

// compactions reads the compaction updates on the wire, in wire order. It
// returns how many compaction_update arrived, one line per compaction id,
// and a problem when the last update of an id is not completed.
func compactions(wire []json.RawMessage) (int, string, string) {
	type entry struct {
		statuses              []string
		blocks, chunks, chars int
		errText               string
		announced             bool
	}
	var order []string
	byID := map[string]*entry{}
	updates := 0
	for _, w := range wire {
		var u struct {
			Kind    string `json:"sessionUpdate"`
			ID      string `json:"compactionId"`
			Status  string `json:"status"`
			Error   string `json:"error"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(w, &u) != nil {
			continue
		}
		e := byID[u.ID]
		if e == nil {
			e = &entry{}
			byID[u.ID] = e
			order = append(order, u.ID)
		}
		switch u.Kind {
		case "compaction_update":
			updates++
			e.announced = true
			e.statuses = append(e.statuses, u.Status)
			e.blocks += len(u.Summary)
			for _, b := range u.Summary {
				e.chars += len(b.Text)
			}
			if u.Error != "" {
				e.errText = u.Error
			}
		case "compaction_summary_chunk":
			e.chunks++
			e.chars += len(u.Content.Text)
		}
	}
	var lines []string
	problem := ""
	for _, id := range order {
		e := byID[id]
		line := fmt.Sprintf("%s: %s, summary %d block(s) and %d chunk(s), %d chars", id, strings.Join(e.statuses, " -> "), e.blocks, e.chunks, e.chars)
		if e.errText != "" {
			line += fmt.Sprintf(", error %q", clip(e.errText, 120))
		}
		lines = append(lines, line)
		switch {
		case problem != "":
		case !e.announced:
			problem = fmt.Sprintf("summary chunks for compaction %s, which no compaction_update announced", id)
		case e.statuses[len(e.statuses)-1] != string(acp.CompactionStatusCompleted):
			problem = fmt.Sprintf("compaction %s ended with status %s, not completed", id, e.statuses[len(e.statuses)-1])
		}
	}
	return updates, strings.Join(lines, "; "), problem
}
