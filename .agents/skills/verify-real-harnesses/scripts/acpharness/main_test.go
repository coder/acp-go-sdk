package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func modelSelect(t *testing.T, raw string) *acp.SessionConfigOptionSelect {
	t.Helper()
	var o acp.SessionConfigOption
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("decode option: %v", err)
	}
	if o.Select == nil {
		t.Fatalf("not a select option: %s", raw)
	}
	return o.Select
}

// The model lists that claude-agent-acp 0.108.0 and codex-acp 2.7.0 offered.
const (
	claudeModels = `{"id":"model","name":"Model","category":"model","type":"select","currentValue":"default","options":[
		{"value":"default","name":"Default (recommended)","description":"Opus"},
		{"value":"opus","name":"Opus 5.5","description":"Opus 5.5 · Best for everyday, complex tasks · $4/$20 per Mtok"},
		{"value":"fable","name":"Fable 5.2","description":"Fable 5.2 · Most capable for your hardest and longest-running tasks · $8/$40 per Mtok"},
		{"value":"sonnet","name":"Sonnet 5.5","description":"Sonnet 5.5 · Efficient for routine tasks · $2/$10 per Mtok"},
		{"value":"haiku","name":"Haiku 5.5","description":"Haiku 5.5 · Fastest for quick answers"}]}`
	codexModels = `{"id":"model","name":"Model","category":"model","type":"select","currentValue":"gpt-6.1-sol","options":[
		{"value":"gpt-6.1-sol","name":"6.1 Sol","description":"Latest workhorse model for coding and everyday work."},
		{"value":"gpt-6-astra","name":"6 Astra","description":"Frontier intelligence for the most demanding work."},
		{"value":"gpt-6-luna","name":"6 Luna","description":"Fast and affordable model for easier tasks."}]}`
	onlyLesser = `{"id":"model","name":"Model","type":"select","currentValue":"big","options":[
		{"value":"big","name":"Big"},
		{"value":"small","name":"Small","description":"Legacy model."}]}`
)

func TestPickSwitchTarget_SkipsAliasesAndLesserModels(t *testing.T) {
	cases := []struct {
		name, raw string
		want      acp.SessionConfigValueId
	}{
		{"claude skips default's alias opus", claudeModels, "fable"},
		{"codex takes the next frontier model", codexModels, "gpt-6-astra"},
		{"no candidate", onlyLesser, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := pickSwitchTarget(modelSelect(t, tc.raw))
			if got != tc.want {
				t.Fatalf("pickSwitchTarget = %q (%s), want %q", got, why, tc.want)
			}
		})
	}
}

func TestClassify_SDKLogLines(t *testing.T) {
	cases := map[string]string{
		`failed to handle notification method=auth/status_update err={"code":-32601,"message":"Method not found"}`:  methodNotFound,
		`ignored unhandled notification method=auth/status_update err={"code":-32601,"message":"Method not found"}`: methodNotFound,
		`failed to parse incoming message err=invalid character 'x'`:                                                decodeError,
		`failed to handle notification method=session/update err={"code":-32602,"message":"Invalid params"}`:        decodeError,
		`connection closed cause=peer connection closed`:                                                            "",
	}
	for text, want := range cases {
		if got := classify(text); got != want {
			t.Errorf("classify(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestRecorder_CountsTheSDKsErrorReplies(t *testing.T) {
	r, err := newRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.line(1, clientToAgent, []byte(`{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"Method not found"}}`))
	r.line(1, clientToAgent, []byte(`{"jsonrpc":"2.0","id":8,"error":{"code":-32602,"message":"Invalid params"}}`))
	r.line(1, agentToClient, []byte(`{"jsonrpc":"2.0","id":9,"error":{"code":-32601,"message":"Method not found"}}`))
	r.line(1, agentToClient, []byte(`{"jsonrpc":"2.0","method":"_auth/status_update","params":{}}`))
	findings, inbound := r.snapshot()
	var kinds []string
	for _, f := range findings {
		kinds = append(kinds, f.Kind)
	}
	if want := []string{methodNotFound, decodeError, agentError}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("findings %v, want %v", kinds, want)
	}
	if inbound["_auth/status_update"] != 1 {
		t.Fatalf("inbound methods %v", inbound)
	}
}

func TestMetaModels_FindsModelKeysAtAnyDepth(t *testing.T) {
	var meta acp.Meta
	raw := `{"_claude/model":"claude-opus-5-5","quota":{"model_usage":[{"model":"claude-opus-5-5"},{"model":"claude-fable-5-2"}]}}`
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatal(err)
	}
	if got, want := metaModels(meta), []string{"claude-fable-5-2", "claude-opus-5-5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("metaModels = %v, want %v", got, want)
	}
}

func TestHarnessClient_AnswersByPolicyAndTracksCost(t *testing.T) {
	rec, err := newRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{rec: rec}
	c := newHarnessClient(h, 1)
	req := acp.RequestPermissionRequest{Options: []acp.PermissionOption{
		{Kind: acp.PermissionOptionKindAllowAlways, OptionId: "always"},
		{Kind: acp.PermissionOptionKindAllowOnce, OptionId: "once"},
		{Kind: acp.PermissionOptionKindRejectOnce, OptionId: "no"},
	}}
	for policy, want := range map[string]string{answerAllow: "once", answerReject: "no", answerCancel: ""} {
		c.setPolicy(policy)
		resp, _ := c.RequestPermission(context.Background(), req)
		got := ""
		if resp.Outcome.Selected != nil {
			got = string(resp.Outcome.Selected.OptionId)
		}
		if got != want {
			t.Errorf("policy %s selected %q, want %q", policy, got, want)
		}
	}

	update := func(sid string, amount float64) {
		var n acp.SessionNotification
		raw := fmt.Sprintf(`{"sessionId":%q,"update":{"sessionUpdate":"usage_update","used":1,"size":2,"cost":{"amount":%g,"currency":"USD"}}}`, sid, amount)
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatal(err)
		}
		_ = c.SessionUpdate(context.Background(), n)
	}
	update("a", 0.1)
	update("a", 0.3) // cumulative per session
	update("b", 0.05)
	if got := c.cost(); got < 0.349 || got > 0.351 {
		t.Fatalf("cost %v, want 0.35", got)
	}

	var eur acp.SessionNotification
	if err := json.Unmarshal([]byte(`{"sessionId":"c","update":{"sessionUpdate":"usage_update","used":1,"size":2,"cost":{"amount":9,"currency":"EUR"}}}`), &eur); err != nil {
		t.Fatal(err)
	}
	_ = c.SessionUpdate(context.Background(), eur)
	c.usage("a", &acp.Usage{InputTokens: 10, OutputTokens: 1, TotalTokens: 11})
	c.usage("a", &acp.Usage{InputTokens: 25, OutputTokens: 3, TotalTokens: 28}) // totals across turns
	usage, other := c.totals()
	if got := c.cost(); got < 0.349 || got > 0.351 {
		t.Fatalf("cost %v after a EUR update, want 0.35: another currency must not count as USD", got)
	}
	if other["EUR"] != 9 || usage["a"].TotalTokens != 28 {
		t.Fatalf("other costs %v, usage %v; want EUR 9 and session a total 28", other, usage)
	}
}

func TestSwitchProblem_NeedsModelEvidence(t *testing.T) {
	opus, fable := []string{"claude-opus-5-5"}, []string{"claude-fable-5-2", "claude-opus-5-5"}
	cases := []struct {
		name            string
		before, on, off []string
		ok              bool
	}{
		{"switch and switch back seen on the wire", opus, fable, opus, true},
		{"no new model after the switch", opus, opus, opus, false},
		{"switched model still used after the switch back", opus, fable, fable, false},
		{"no model ids at all", nil, nil, nil, false},
		{"no model ids after the switch", opus, nil, opus, false},
		{"no model ids after the switch back", opus, fable, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := switchProblem(tc.before, tc.on, tc.off); (got == "") != tc.ok {
				t.Fatalf("switchProblem(%v, %v, %v) = %q, want ok=%v", tc.before, tc.on, tc.off, got, tc.ok)
			}
		})
	}
}

func TestReplayProblem_NeedsTheAgentsReply(t *testing.T) {
	user := event{Kind: "user_message_chunk", Text: pongPrompt}
	cases := []struct {
		name string
		evs  []event
		ok   bool
	}{
		{"user prompt and agent reply", []event{user, {Kind: "agent_message_chunk", Text: "pong"}}, true},
		{"only the user prompt, which itself says pong", []event{user}, false},
		{"an agent reply without pong", []event{user, {Kind: "agent_message_chunk", Text: "ping"}}, false},
		{"an agent reply without the user prompt", []event{{Kind: "agent_message_chunk", Text: "pong"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replayProblem(tc.evs); (got == "") != tc.ok {
				t.Fatalf("replayProblem = %q, want ok=%v", got, tc.ok)
			}
		})
	}
}

// TestProfiles_MatchTheScenariosThatRun keeps -only honest: the steps that
// runLive and runSmoke call are the names in profiles, in order.
func TestProfiles_MatchTheScenariosThatRun(t *testing.T) {
	src, err := os.ReadFile("scenarios.go")
	if err != nil {
		t.Fatal(err)
	}
	step := regexp.MustCompile(`h\.step\("([^"]+)"`)
	for profile, fn := range map[string]string{"live": "runLive", "smoke": "runSmoke"} {
		body := string(src)
		start := strings.Index(body, "func (h *harness) "+fn+"() {")
		if start < 0 {
			t.Fatalf("no %s in scenarios.go", fn)
		}
		body = body[start:]
		body = body[:strings.Index(body, "\n}\n")]
		got := []string{"initialize", "authenticate"} // connect runs these two
		for _, m := range step.FindAllStringSubmatch(body, -1) {
			got = append(got, m[1])
		}
		if !reflect.DeepEqual(got, profiles[profile]) {
			t.Errorf("%s runs %v, profiles lists %v", fn, got, profiles[profile])
		}
	}
}

func TestRecorder_KeepsTheRawResultOfEachMethod(t *testing.T) {
	r, err := newRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.line(1, clientToAgent, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	r.line(2, clientToAgent, []byte(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}`))
	r.line(1, agentToClient, []byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`))
	r.line(2, agentToClient, []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"boom"}}`))
	if got := string(r.result(1, "initialize")); got != `{"protocolVersion":1}` {
		t.Fatalf("initialize result on conn 1 = %q", got)
	}
	if got := r.result(2, "session/new"); got != nil {
		t.Fatalf("an error response left a result: %q", got)
	}
}

// decodeInit decodes a raw initialize result with the SDK, as the driver
// does.
func decodeInit(t *testing.T, raw string) acp.InitializeResponse {
	t.Helper()
	var r acp.InitializeResponse
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}
	return r
}

// TestAdvertises_DecidesByTheWire covers the bug that a probe found: with
// the generated JSON tag of SessionCapabilities.List renamed, the SDK
// decodes list as absent, and session/list must fail, not skip.
func TestAdvertises_DecidesByTheWire(t *testing.T) {
	const withList = `{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"sessionCapabilities":{"list":{},"close":{}}}}`
	const without = `{"protocolVersion":1,"agentCapabilities":{"loadSession":false,"sessionCapabilities":{"list":null}}}`
	lostList := decodeInit(t, withList)
	lostList.AgentCapabilities.SessionCapabilities.List = nil
	inventedList := decodeInit(t, without)
	inventedList.AgentCapabilities.SessionCapabilities.List = &acp.SessionListCapabilities{}
	lostLoad := decodeInit(t, withList)
	lostLoad.AgentCapabilities.LoadSession = false

	cases := []struct {
		name    string
		raw     string
		decoded acp.InitializeResponse
		c       capability
		want    status
	}{
		{"advertised and decoded", withList, decodeInit(t, withList), capList, ""},
		{"null on the wire", without, decodeInit(t, without), capList, skip},
		{"missing on the wire", without, decodeInit(t, without), capResume, skip},
		{"loadSession false", without, decodeInit(t, without), capLoadSession, skip},
		{"SDK lost list", withList, lostList, capList, fail},
		{"SDK invented list", without, inventedList, capList, fail},
		{"SDK lost loadSession", withList, lostLoad, capLoadSession, fail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &agentConn{n: 1, init: tc.decoded, rawInit: json.RawMessage(tc.raw)}
			st, detail := need(a, tc.c)
			if st != tc.want {
				t.Fatalf("need(%s) = %q %q, want %q", tc.c.path, st, detail, tc.want)
			}
			if st == fail && !strings.Contains(detail, "decode mismatch") {
				t.Fatalf("a FAIL without a decode mismatch: %s", detail)
			}
			if st == skip && !strings.HasPrefix(detail, skipNotAdvertised+": ") {
				t.Fatalf("a SKIP without the documented reason: %s", detail)
			}
		})
	}
	if st, _ := need(&agentConn{n: 1}, capList); st != fail {
		t.Fatalf("no recorded initialize result gave %q, want FAIL", st)
	}
}

// newSessionDecoder returns a func that decodes the config options of a raw
// session/new result with the SDK.
func newSessionDecoder(t *testing.T) func(string) []acp.SessionConfigOption {
	return func(raw string) []acp.SessionConfigOption {
		var r acp.NewSessionResponse
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatalf("decode session/new: %v", err)
		}
		return r.ConfigOptions
	}
}

func TestConfigSelect_ComparesTheWireWithTheDecodedOption(t *testing.T) {
	decodeNew := newSessionDecoder(t)
	codex := `{"sessionId":"s","configOptions":[` + codexModels + `]}`
	grouped := `{"sessionId":"s","configOptions":[{"id":"model","name":"Model","type":"select","currentValue":"a","options":[
		{"group":"g1","name":"One","options":[{"value":"a","name":"A"},{"value":"b","name":"B"}]},
		{"group":"g2","name":"Two","options":[{"value":"c","name":"C"}]}]}]}`
	none := `{"sessionId":"s","configOptions":[{"id":"fast","name":"Fast","type":"boolean","currentValue":false}]}`

	if sel, err := configSelect(json.RawMessage(codex), decodeNew(codex), acp.SessionConfigOptionCategoryModel, "model"); err != nil || sel == nil || sel.CurrentValue != "gpt-6.1-sol" {
		t.Fatalf("codex options: %v, %v", sel, err)
	}
	if sel, err := configSelect(json.RawMessage(grouped), decodeNew(grouped), acp.SessionConfigOptionCategoryModel, "model"); err != nil || len(selectOptions(sel)) != 3 {
		t.Fatalf("grouped options: %v, %v", sel, err)
	}
	if sel, err := configSelect(json.RawMessage(none), decodeNew(none), acp.SessionConfigOptionCategoryModel, "model"); err != nil || sel != nil {
		t.Fatalf("no model option: %v, %v", sel, err)
	}

	lost := decodeNew(codex)
	lost[0].Select = nil
	noValues := decodeNew(codex)
	noValues[0].Select.Options = acp.SessionConfigSelectOptions{}
	for name, cfg := range map[string][]acp.SessionConfigOption{"SDK lost the option": lost, "SDK lost its values": noValues} {
		if _, err := configSelect(json.RawMessage(codex), cfg, acp.SessionConfigOptionCategoryModel, "model"); err == nil || !strings.Contains(err.Error(), "decode mismatch") {
			t.Errorf("%s: err %v, want a decode mismatch", name, err)
		}
	}
}

func TestConfigBoolean_ComparesEveryOptionAndPicksABoolean(t *testing.T) {
	decodeNew := newSessionDecoder(t)
	// Shaped like the options that claude-agent-acp 0.108.0 and codex-acp 2.7.0
	// offer a client that accepts booleans: a model_config boolean after other
	// options, and a category that the schema does not list.
	claude := `{"sessionId":"s","configOptions":[` +
		`{"id":"model","name":"Model","type":"select","category":"model","currentValue":"default","options":[{"value":"default","name":"Default"}]},` +
		`{"id":"ultra","name":"Ultra","type":"boolean","currentValue":true},` +
		`{"id":"fast","name":"Fast","type":"boolean","category":"model_config","currentValue":false},` +
		`{"id":"plan","name":"Plan","type":"select","category":"collaboration_mode","currentValue":"x","options":[{"value":"x","name":"X"}]}]}`
	noBoolean := `{"sessionId":"s","configOptions":[{"id":"model","name":"Model","type":"select","currentValue":"m","options":[{"value":"m","name":"M"}]}]}`

	opt, all, err := configBoolean(json.RawMessage(claude), decodeNew(claude), acp.SessionConfigOptionCategoryModelConfig)
	if err != nil || opt == nil || opt.ID != "fast" || opt.CurrentValue != "false" || len(all) != 4 {
		t.Fatalf("claude options: %v, %v, %v; want fast=false of 4", opt, all, err)
	}
	if opt, _, err := configBoolean(json.RawMessage(claude), decodeNew(claude), "other"); err != nil || opt == nil || opt.ID != "ultra" {
		t.Fatalf("no boolean in the category: %v, %v; want the first boolean, ultra", opt, err)
	}
	if opt, _, err := configBoolean(json.RawMessage(noBoolean), decodeNew(noBoolean), acp.SessionConfigOptionCategoryModelConfig); err != nil || opt != nil {
		t.Fatalf("no boolean option: %v, %v", opt, err)
	}

	flipped := decodeNew(claude)
	flipped[2].Boolean.CurrentValue = true
	lostCategory := decodeNew(claude)
	lostCategory[3].Select.Category = nil
	for name, cfg := range map[string][]acp.SessionConfigOption{"SDK flipped a value": flipped, "SDK lost a category": lostCategory, "SDK lost an option": decodeNew(claude)[:3]} {
		if _, _, err := configBoolean(json.RawMessage(claude), cfg, acp.SessionConfigOptionCategoryModelConfig); err == nil || !strings.Contains(err.Error(), "decode mismatch") {
			t.Errorf("%s: err %v, want a decode mismatch", name, err)
		}
	}
}

func TestUndocumentedSkips_FlagsEveryOtherReason(t *testing.T) {
	results := []result{
		{Name: "initialize", Status: pass},
		{Name: "authenticate", Status: skip, Detail: skipNoAuth + ": the driver runs this agent without -auth"},
		{Name: "cancel", Status: skip, Detail: skipNotSelected},
		{Name: "session/list", Status: skip, Detail: skipNotAdvertised + ": no list"},
		{Name: "config-option", Status: skip, Detail: skipNoOtherModel + ": current x"},
		{Name: "prompt", Status: skip, Detail: "blocked: session/new failed"},
		{Name: "session/load", Status: skip, Detail: "budget: reported cost $25.00 reached -budget-usd 25.00"},
		{Name: "session/close", Status: skip, Detail: "not advertisedx"},
	}
	got := undocumentedSkips(results)
	if len(got) != 3 || !strings.HasPrefix(got[0], "prompt ") || !strings.HasPrefix(got[1], "session/load ") || !strings.HasPrefix(got[2], "session/close ") {
		t.Fatalf("undocumentedSkips = %v, want prompt, session/load and session/close", got)
	}
}

// TestKnownSkips_AreDocumented keeps SKILL.md's list of SKIP reasons in
// step with the driver's.
func TestKnownSkips_AreDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range knownSkips {
		if !strings.Contains(string(doc), "`"+reason+"`") {
			t.Errorf("SKILL.md does not list the SKIP reason `%s`", reason)
		}
	}
}

func TestRecorder_CountsTheCostOnTheWire(t *testing.T) {
	r, err := newRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	update := func(conn int, sid, cost string) {
		r.line(conn, agentToClient, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"usage_update","used":1,"size":2,"cost":%s}}}`, sid, cost)))
	}
	update(1, "a", `{"amount":0.1,"currency":"USD"}`)
	update(1, "a", `{"amount":0.3,"currency":"usd"}`) // cumulative per session
	update(1, "b", `{"amount":0.05,"currency":"USD"}`)
	update(2, "a", `{"amount":0.3,"currency":"USD"}`) // a reopened session counts again
	update(1, "c", `{"amount":9,"currency":"EUR"}`)
	if got := r.cost(); got < 0.649 || got > 0.651 {
		t.Fatalf("cost %v, want 0.65", got)
	}
}

func TestModeOffered_SendsOnlyAnOfferedMode(t *testing.T) {
	sel := modelSelect(t, `{"id":"mode","name":"Mode","category":"mode","type":"select","currentValue":"auto","options":[
		{"value":"auto","name":"Auto"},{"value":"default","name":"Default"}]}`)
	modes := &acp.SessionModeState{AvailableModes: []acp.SessionMode{{Id: "read-only"}}, CurrentModeId: "read-only"}
	cases := []struct {
		name  string
		sel   *acp.SessionConfigOptionSelect
		modes *acp.SessionModeState
		mode  string
		ok    bool
	}{
		{"select offers it", sel, nil, "default", true},
		{"select lacks it", sel, modes, "read-only", false},
		{"session modes offer it", nil, modes, "read-only", true},
		{"session modes lack it", nil, modes, "default", false},
		{"no modes at all", nil, nil, "default", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := modeOffered(tc.sel, tc.modes, tc.mode); (err == nil) != tc.ok {
				t.Fatalf("modeOffered(%s) = %v, want ok=%v", tc.mode, err, tc.ok)
			}
		})
	}
}

func TestCheckModes_ComparesTheWireWithTheDecodedModes(t *testing.T) {
	decodeModes := func(raw string) *acp.SessionModeState {
		var r acp.NewSessionResponse
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatalf("decode session/new: %v", err)
		}
		return r.Modes
	}
	modes := `{"sessionId":"s","modes":{"currentModeId":"auto","availableModes":[{"id":"auto","name":"Auto"},{"id":"read-only","name":"Read only"}]}}`
	none := `{"sessionId":"s"}`
	if err := checkModes(json.RawMessage(modes), decodeModes(modes)); err != nil {
		t.Fatalf("modes that decode as on the wire: %v", err)
	}
	if err := checkModes(json.RawMessage(none), decodeModes(none)); err != nil {
		t.Fatalf("no modes on the wire and none decoded: %v", err)
	}

	lostOne := decodeModes(modes)
	lostOne.AvailableModes = lostOne.AvailableModes[:1]
	otherCurrent := decodeModes(modes)
	otherCurrent.CurrentModeId = "read-only"
	cases := map[string]*acp.SessionModeState{"SDK lost the modes": nil, "SDK lost a mode": lostOne, "SDK changed the current mode": otherCurrent}
	for name, decoded := range cases {
		if err := checkModes(json.RawMessage(modes), decoded); err == nil || !strings.Contains(err.Error(), "decode mismatch") {
			t.Errorf("%s: err %v, want a decode mismatch", name, err)
		}
	}
	if err := checkModes(json.RawMessage(none), decodeModes(modes)); err == nil {
		t.Error("modes that the wire does not have passed")
	}
}

func TestRecorder_CountsEverySDKErrorLog(t *testing.T) {
	r, err := newRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.logged(1, slog.LevelError, `received message with neither id nor method`)
	r.logged(1, slog.LevelInfo, `connection closed cause=peer connection closed`)
	r.logged(1, slog.LevelDebug, `failed to parse incoming message err=invalid character`)
	findings, _ := r.snapshot()
	if len(findings) != 2 || findings[0].Kind != decodeError || findings[1].Kind != decodeError {
		t.Fatalf("findings %+v, want two decode errors: the unknown ERROR and the parse failure", findings)
	}
}

// The clients of connections 2 and 3 do not implement auth/status_update, so
// the SDK may ignore it there below the error level. Every other "Method not
// found" stays a finding: at the error level, on connection 1, or for
// another method. A decode failure of auth/status_update stays one too.
func TestRecorder_IgnoredAuthStatusOnlyBelowErrorOnLaterConns(t *testing.T) {
	r, err := newRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const notFound = ` err={"code":-32601,"message":"Method not found"}`
	r.logged(2, slog.LevelDebug, `ignored unhandled notification method=auth/status_update`+notFound)
	r.logged(3, slog.LevelDebug, `ignored unhandled notification method=auth/status_update`+notFound)
	r.logged(1, slog.LevelDebug, `ignored unhandled notification method=auth/status_update`+notFound)
	r.logged(3, slog.LevelError, `failed to handle notification method=auth/status_update`+notFound)
	r.logged(3, slog.LevelDebug, `ignored unhandled notification method=session/new_thing`+notFound)
	r.logged(3, slog.LevelError, `failed to handle notification method=auth/status_update err={"code":-32602,"message":"Invalid params"}`)
	findings, _ := r.snapshot()
	var got []string
	for _, f := range findings {
		got = append(got, fmt.Sprintf("%d %s", f.Conn, f.Kind))
	}
	want := []string{"1 " + methodNotFound, "3 " + methodNotFound, "3 " + methodNotFound, "3 " + decodeError}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings %v, want %v", got, want)
	}
	if n := r.ignoredCount(); n != 2 {
		t.Fatalf("ignored %d, want 2", n)
	}
}

func TestReplayProblem_NeedsTheReplyAfterTheUserMessage(t *testing.T) {
	evs := []event{{Kind: "agent_message_chunk", Text: "pong"}, {Kind: "user_message_chunk", Text: pongPrompt}}
	if replayProblem(evs) == "" {
		t.Fatal("a replay with the agent's reply before the user's message passed")
	}
}

// The two forms are the elicitation/create params that claude-agent-acp
// 0.108.0 and codex-acp 2.7.0 sent for elicitPrompt on 2026-11-18.
const (
	claudeForm = `{"mode":"form","sessionId":"s","toolCallId":"t","message":"Which color do you prefer?","requestedSchema":{"type":"object","properties":{"question_0":{"type":"string","title":"Color","oneOf":[{"const":"Red","title":"Red"},{"const":"Blue","title":"Blue"}]},"question_0_custom":{"type":"string","title":"Other"}}}}`
	codexForm  = `{"sessionId":"s","toolCallId":"t","mode":"form","message":"Codex needs your input to continue.","requestedSchema":{"type":"object","properties":{"preferred_color":{"title":"Which color do you prefer?","_meta":{"codex":{"isOther":true}},"type":"string","oneOf":[{"const":"red","title":"red"},{"const":"blue","title":"blue"},{"const":"None of the above","title":"None of the above"}]},"preferred_color_note":{"type":"string","title":"Additional answer or note"}},"required":["preferred_color"]},"_meta":{"codex":{"autoResolutionMs":null}}}`
)

func TestHarnessClient_AnswersElicitationsAndKeepsWhatItDecoded(t *testing.T) {
	rec, err := newRecorder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := newHarnessClient(&harness{rec: rec}, 1)
	tests := []struct {
		name, raw, word, want string
	}{
		{"claude picks by title", claudeForm, "blue", `accept {"question_0":"Blue"}`},
		{"codex picks by value", codexForm, "blue", `accept {"preferred_color":"blue"}`},
		{"no word cancels", codexForm, "", "cancel"},
		{"required free text gets the word", `{"mode":"form","sessionId":"s","message":"m","requestedSchema":{"type":"object","properties":{"name":{"type":"string"},"n":{"type":"integer"}},"required":["name","n"]}}`, "blue", `accept {"name":"blue"}`},
		{"url is cancelled", `{"mode":"url","sessionId":"s","elicitationId":"e","message":"m","url":"https://example.com"}`, "blue", "cancel"},
	}
	for i, tt := range tests {
		var req acp.CreateElicitationRequest
		if err := json.Unmarshal([]byte(tt.raw), &req); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		c.setElicit(tt.word)
		if _, err := c.CreateElicitation(context.Background(), req); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		got := c.elicitationsSince(i)[0]
		if got.Answer != tt.want {
			t.Errorf("%s: answer %s, want %s", tt.name, got.Answer, tt.want)
		}
		if err := sameJSON(got.decoded, json.RawMessage(tt.raw)); err != nil {
			t.Errorf("%s: the SDK did not keep the request: %v", tt.name, err)
		}
	}
}

func TestSameJSON_IgnoresKeyOrderAndEmptyLists(t *testing.T) {
	if err := sameJSON([]acp.AuthMethod{}, nil); err != nil {
		t.Errorf("an empty list and an absent one differ: %v", err)
	}
	if err := sameJSON(map[string]any{"b": 1, "a": []int{2}}, json.RawMessage(`{"a":[2],"b":1}`)); err != nil {
		t.Errorf("key order matters: %v", err)
	}
	if err := sameJSON(map[string]any{"a": 1}, json.RawMessage(`{"a":1,"b":{}}`)); err == nil {
		t.Error("a dropped member went unnoticed")
	}
}
