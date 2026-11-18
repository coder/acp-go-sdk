package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// deliver records an agent line on the wire and hands its params, decoded
// by the SDK's types, to the client, as the SDK would.
func deliver(t *testing.T, rec *recorder, c *harnessClient, line string) {
	t.Helper()
	rec.line(1, agentToClient, []byte(line))
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatal(err)
	}
	switch msg.Method {
	case "session/update":
		var n acp.SessionNotification
		if err := json.Unmarshal(msg.Params, &n); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}
		_ = c.SessionUpdate(context.Background(), n)
	case "session/request_permission":
		var p acp.RequestPermissionRequest
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}
		_, _ = c.RequestPermission(context.Background(), p)
	default:
		t.Fatalf("no route for %s", msg.Method)
	}
}

func update(u string) string {
	return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":` + u + `}}`
}

// Tool calls shaped like those of claude-agent-acp 0.108.0, which names the
// tool on the tool_call and on the permission request, and codex-acp 2.7.0,
// which reports an MCP server that cannot start as a failed tool call
// without a name.
var (
	claudeToolCall   = update(`{"sessionUpdate":"tool_call","toolCallId":"t1","name":"Write","title":"Write a.txt","kind":"edit","status":"pending"}`)
	claudePermission = `{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"s","toolCall":{"toolCallId":"t1","name":"Write","title":"Write a.txt"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}`
	claudeToolUpdate = update(`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed"}`)
	codexStartup     = update(`{"sessionUpdate":"tool_call","toolCallId":"mcp_startup.harness-broken.1","kind":"other","title":"mcp__harness-broken__startup","status":"failed"}`)
)

func TestCheckToolNames_ComparesTheWireWithTheDecodedName(t *testing.T) {
	rec, err := newRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := newHarnessClient(&harness{rec: rec}, 1)
	for _, line := range []string{claudeToolCall, claudePermission, claudeToolUpdate, codexStartup} {
		deliver(t, rec, c, line)
	}
	wire, decoded := rec.toolNamesOnWire(), c.decodedToolNames()
	st, detail := checkToolNames(wire, decoded, false)
	if st != pass || !strings.Contains(detail, "1 of 2 tool_call,") || !strings.Contains(detail, "1 of 1 session/request_permission") || !strings.HasSuffix(detail, `1 distinct: "Write"`) {
		t.Fatalf("checkToolNames = %s %q", st, detail)
	}

	dropped := append([]toolName(nil), decoded...)
	dropped[0].Name = "" // the SDK lost the tool_call's name
	invented := append([]toolName(nil), decoded...)
	invented[2].Name = `"Write"` // the SDK named the update, whose wire has no name
	for name, d := range map[string][]toolName{"SDK lost a name": dropped, "SDK invented a name": invented, "SDK lost an update": decoded[:3]} {
		if st, detail := checkToolNames(wire, d, false); st != fail || !strings.HasPrefix(detail, "decode mismatch: ") {
			t.Errorf("%s: %s %q, want a decode mismatch", name, st, detail)
		}
	}
	unnamed := wire[3:]
	for _, only := range []bool{false, true} {
		if st, detail := checkToolNames(unnamed, decoded[3:], only); st != fail || !strings.HasPrefix(detail, "no tool call on the wire carried a name") {
			t.Errorf("tool calls without a name, only=%v: %s %q, want FAIL", only, st, detail)
		}
	}
	if st, detail := checkToolNames(nil, nil, true); st != skip || !strings.HasPrefix(detail, skipNotSelected+": ") {
		t.Errorf("no tool call in a -only run: %s %q, want a documented SKIP", st, detail)
	}
	if st, _ := checkToolNames(nil, nil, false); st != fail {
		t.Errorf("no tool call in a full run: %s, want FAIL", st)
	}
}

func TestWireName_EncodesLikeTheSDK(t *testing.T) {
	sdk := "a<b>&é"
	cases := map[string]string{``: "", `null`: "", ` "Bash" `: `"Bash"`, `"a<b>&\u00e9"`: decodedName(&sdk), `42`: `42`}
	for raw, want := range cases {
		if got := wireName(json.RawMessage(raw)); got != want {
			t.Errorf("wireName(%s) = %s, want %s", raw, got, want)
		}
	}
	if decodedName(nil) != "" {
		t.Error("a nil name is not absent")
	}
}

// The notice and compaction updates that claude-agent-acp 0.108.0 sent on
// 2026-11-18, with the summary cut short.
var (
	claudeNotice     = update(`{"sessionUpdate":"notice","severity":"warning","title":"MCP server \"harness-broken\" failed to start","description":"Its tools are unavailable in this session. Run /mcp for the error."}`)
	claudeCompaction = []string{
		update(`{"sessionUpdate":"compaction_update","compactionId":"c1","status":"in_progress"}`),
		update(`{"sessionUpdate":"compaction_update","compactionId":"c1","status":"completed","summary":[{"type":"text","text":"1. Primary Request"}]}`),
		update(`{"sessionUpdate":"compaction_update","compactionId":"c1","status":"completed"}`),
	}
)

func TestSent_ComparesTheDecodedUpdatesWithTheWire(t *testing.T) {
	rec, err := newRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{rec: rec}
	a := &agentConn{n: 1, client: newHarnessClient(h, 1)}
	for _, line := range append([]string{claudeNotice, update(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"pong"}}`)}, claudeCompaction...) {
		deliver(t, rec, a.client, line)
	}
	notices, err := h.sent(a, "s", 0, 0, "notice")
	if err != nil || len(notices) != 1 || describeNotices(notices) != `warning "MCP server \"harness-broken\" failed to start" with description "Its tools are unavailable in this session. Run /mcp for the error."` {
		t.Fatalf("notices %d %q, %v", len(notices), describeNotices(notices), err)
	}
	wire, err := h.sent(a, "s", 2, 1, "compaction_update", "compaction_summary_chunk")
	if err != nil || len(wire) != 3 {
		t.Fatalf("compaction updates %d, %v", len(wire), err)
	}

	// The SDK hands the client a notice whose title differs from the wire.
	rec.line(1, agentToClient, []byte(update(`{"sessionUpdate":"notice","severity":"info","title":"A"}`)))
	var n acp.SessionNotification
	if err := json.Unmarshal([]byte(`{"sessionId":"s","update":{"sessionUpdate":"notice","severity":"info","title":"B"}}`), &n); err != nil {
		t.Fatal(err)
	}
	_ = a.client.SessionUpdate(context.Background(), n)
	if _, err := h.sent(a, "s", 0, 0, "notice"); err == nil || !strings.HasPrefix(err.Error(), "decode mismatch: ") {
		t.Fatalf("a changed title: %v, want a decode mismatch", err)
	}
}

func TestMatchDecoded_NeedsEveryUpdateOnce(t *testing.T) {
	x, y := json.RawMessage(`{"a":1,"b":[]}`), json.RawMessage(`{"a":2}`)
	if err := matchDecoded([]json.RawMessage{y, json.RawMessage(`{"b":[],"a":1}`)}, []json.RawMessage{x, y}); err != nil {
		t.Errorf("the same updates in another order: %v", err)
	}
	for name, decoded := range map[string][]json.RawMessage{"one lost": {x}, "one twice": {x, x}} {
		if err := matchDecoded(decoded, []json.RawMessage{x, y}); err == nil || !strings.HasPrefix(err.Error(), "decode mismatch: ") {
			t.Errorf("%s: %v, want a decode mismatch", name, err)
		}
	}
}

func TestCompactions_NeedsEachToEndCompleted(t *testing.T) {
	raw := func(us ...string) []json.RawMessage {
		var out []json.RawMessage
		for _, u := range us {
			out = append(out, json.RawMessage(u))
		}
		return out
	}
	var claude []string
	for _, line := range claudeCompaction {
		claude = append(claude, string(jsonAt(json.RawMessage(line), "params", "update")))
	}
	codex := []string{`{"sessionUpdate":"compaction_update","compactionId":"x","status":"in_progress"}`, `{"sessionUpdate":"compaction_update","compactionId":"x","status":"completed"}`}
	chunk := `{"sessionUpdate":"compaction_summary_chunk","compactionId":"x","content":{"type":"text","text":"sum"}}`
	cases := []struct {
		name    string
		wire    []json.RawMessage
		updates int
		detail  string
		ok      bool
	}{
		{"claude: a summary and a repeated completed", raw(claude...), 3, "c1: in_progress -> completed -> completed, summary 1 block(s) and 0 chunk(s), 18 chars", true},
		{"codex: no summary", raw(codex...), 2, "x: in_progress -> completed, summary 0 block(s) and 0 chunk(s), 0 chars", true},
		{"summary chunks", raw(codex[0], chunk, codex[1]), 2, "x: in_progress -> completed, summary 0 block(s) and 1 chunk(s), 3 chars", true},
		{"still in progress", raw(codex[0]), 1, "", false},
		{"failed", raw(codex[0], `{"sessionUpdate":"compaction_update","compactionId":"x","status":"failed","error":"boom"}`), 2, "", false},
		{"a chunk that no update announced", raw(chunk), 0, "", false},
		{"none", nil, 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updates, detail, problem := compactions(tc.wire)
			if updates != tc.updates || (problem == "") != tc.ok || (tc.detail != "" && detail != tc.detail) {
				t.Fatalf("compactions = %d, %q, %q; want %d, %q, ok=%v", updates, detail, problem, tc.updates, tc.detail, tc.ok)
			}
		})
	}
}
