package main

import (
	"context"
	"encoding/json"
	"fmt"
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
		`failed to handle notification method=auth/status_update err={"code":-32601,"message":"Method not found"}`: methodNotFound,
		`failed to parse incoming message err=invalid character 'x'`:                                               decodeError,
		`failed to handle notification method=session/update err={"code":-32602,"message":"Invalid params"}`:       decodeError,
		`connection closed cause=peer connection closed`:                                                           "",
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
	if want := []string{methodNotFound, decodeError}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("findings %v, want %v (an agent's error reply is the scenario's failure, not the SDK's)", kinds, want)
	}
	if inbound["_auth/status_update"] != 1 {
		t.Fatalf("inbound methods %v", inbound)
	}
}

func TestMetaModels_FindsModelKeysAtAnyDepth(t *testing.T) {
	var meta map[string]any
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
	tokens, other := c.totals()
	if got := c.cost(); got < 0.349 || got > 0.351 {
		t.Fatalf("cost %v after a EUR update, want 0.35: another currency must not count as USD", got)
	}
	if other["EUR"] != 9 || tokens["total"] != 28 {
		t.Fatalf("other costs %v, tokens %v; want EUR 9 and total 28", other, tokens)
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
