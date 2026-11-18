package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a fake agent: run() starts os.Args[0] with
// ACPWIRE_FAKE_AGENT set, and TestMain serves the protocol instead of tests.
func TestMain(m *testing.M) {
	if mode := os.Getenv("ACPWIRE_FAKE_AGENT"); mode != "" {
		fakeAgent(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeAgent(mode string) {
	if mode == "garbage" {
		fmt.Println("debug: hello")
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req map[string]any
		if json.Unmarshal(sc.Bytes(), &req) != nil || req["id"] == nil {
			continue
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{"ok": true}}
		if mode == "asks" {
			// Ask the client twice, and answer with its two results.
			resp["result"] = map[string]any{"form": askClient(sc, 100, "form"), "url": askClient(sc, 101, "url")}
		}
		if mode == "echo" {
			resp["result"] = req["params"]
		}
		if mode == "no-result" {
			delete(resp, "result")
		}
		b, _ := json.Marshal(resp)
		fmt.Println(string(b))
	}
}

// askClient sends the client an "ask" request with the given mode, and returns
// the result of the client's response.
func askClient(sc *bufio.Scanner, id int, mode string) any {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "ask", "params": map[string]any{"mode": mode}})
	fmt.Println(string(b))
	for sc.Scan() {
		var resp map[string]any
		if json.Unmarshal(sc.Bytes(), &resp) == nil && resp["id"] == float64(id) {
			return resp["result"]
		}
	}
	return nil
}

func writeJourney(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journey.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadJourney_RejectsBadSteps(t *testing.T) {
	tests := []struct {
		name, line, wantErr string
	}{
		{"two actions", `{"send":"initialize","close":true}`, "two actions"},
		{"field of another action", `{"send":"x","where":{}}`, `field "where" does not apply`},
		{"no action", `{"step":"label only"}`, "no action"},
		{"trailing content", `{"close":true} {"send":"x"}`, "content after the step object"},
		{"bad duration", `{"sleep":"5 sec"}`, "unknown unit"},
		{"respond without result or error", `{"respond":"m"}`, "exactly one of result and error"},
		{"respond with both", `{"respond":"m","result":{},"error":{}}`, "exactly one of result and error"},
		{"close false", `{"close":false}`, `"close":true`},
		{"unknown field", `{"sned":"x"}`, "no action"},
		{"expect on an async send", `{"send":"m","async":true,"expect":{"result.ok":true}}`, "async send cannot expect"},
		{"negative count", `{"waitFor":"m","count":-1}`, "must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := readJourney(writeJourney(t, tt.line))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("readJourney(%s) error = %v, want it to contain %q", tt.line, err, tt.wantErr)
			}
		})
	}
}

func TestReadJourney_AcceptsEveryAction(t *testing.T) {
	steps, lines, err := readJourney(writeJourney(t,
		"# comment",
		`{"respond":"session/request_permission","result":{"outcome":{"outcome":"cancelled"}}}`,
		`{"respond":"elicitation/create","where":{"params.mode":"url"},"error":{"code":1}}`,
		`{"send":"initialize","params":{},"save":"init","async":true,"timeout":"2s"}`,
		`{"await":"init","expect":{"result.ok":true}}`,
		`{"notify":"session/cancel","params":{}}`,
		`{"waitFor":"session/update","where":{"params.x":1},"count":2}`,
		`{"raw":"{not json"}`,
		`{"stderrHas":"x","timeout":"1s"}`,
		`{"stderrLacks":"y"}`,
		`{"sleep":"10ms"}`,
		`{"step":"end","close":true,"expectExit":0}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 11 || lines[0] != 2 {
		t.Fatalf("got %d steps, first at line %d; want 11 steps, first at line 2", len(steps), lines[0])
	}
}

func TestValidEnvelope(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{`{"jsonrpc":"2.0","id":1,"result":{}}`, true},
		{`{"jsonrpc":"2.0","id":"a","result":null}`, true},
		{`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}`, true},
		{`{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{}}`, true},
		{`{"jsonrpc":"2.0","method":"session/update"}`, true},
		{`{"jsonrpc":"2.0","id":1}`, false},
		{`{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"x"}}`, false},
		{`{"jsonrpc":"2.0","id":1,"error":"boom"}`, false},
		{`{"jsonrpc":"2.0","method":7}`, false},
		{`{"jsonrpc":"2.0","method":"m","result":{}}`, false},
		{`{"jsonrpc":"2.0","id":{},"result":{}}`, false},
		{`{"jsonrpc":"1.0","id":1,"result":{}}`, false},
		{`{"jsonrpc":"2.0","method":"m","params":1}`, false},
		{`{"jsonrpc":"2.0","id":1,"method":"m","params":[1]}`, true},
	}
	for _, tt := range tests {
		var msg map[string]any
		if err := json.Unmarshal([]byte(tt.msg), &msg); err != nil {
			t.Fatal(err)
		}
		if got := validEnvelope(msg); got != tt.want {
			t.Errorf("validEnvelope(%s) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

func TestRun_AgentOutput(t *testing.T) {
	journey := []string{
		`{"send":"ping","expect":{"result.ok":true},"timeout":"2s"}`,
		`{"close":true,"expectExit":0}`,
	}
	tests := []struct {
		mode     string
		wantPass bool
		wantStep string
	}{
		{"valid", true, ""},
		{"garbage", false, "agent stdout is JSON-RPC"},
		{"no-result", false, "ping"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Setenv("ACPWIRE_FAKE_AGENT", tt.mode)
			steps, lines, err := readJourney(writeJourney(t, journey...))
			if err != nil {
				t.Fatal(err)
			}
			results, err := run(os.Args[0], nil, steps, lines, t.TempDir(), 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			pass, failed := true, ""
			for _, r := range results {
				if !r.Pass {
					pass = false
					if failed == "" {
						failed = r.Step
					}
				}
			}
			if pass != tt.wantPass || !strings.Contains(failed, tt.wantStep) {
				t.Fatalf("results %+v: pass=%v first failure %q; want pass=%v and a failure in %q", results, pass, failed, tt.wantPass, tt.wantStep)
			}
		})
	}
}

// TestRun_RespondPicksTheNewestMatchingRule checks that a rule with "where"
// answers only the requests that match it, that a rule without "where" answers
// the rest, and that "waitFor" sees the agent's requests.
func TestRun_RespondPicksTheNewestMatchingRule(t *testing.T) {
	t.Setenv("ACPWIRE_FAKE_AGENT", "asks")
	steps, lines, err := readJourney(writeJourney(t,
		`{"respond":"ask","result":{"v":"old"}}`,
		`{"respond":"ask","result":{"v":"any"}}`,
		`{"respond":"ask","where":{"params.mode":"url"},"result":{"v":"url"}}`,
		`{"send":"ping","expect":{"result.form.v":"any","result.url.v":"url"},"timeout":"2s"}`,
		`{"waitFor":"ask","where":{"params.mode":"form"},"timeout":"1s"}`,
		`{"close":true,"expectExit":0}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	results, err := run(os.Args[0], nil, steps, lines, t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if !r.Pass {
			t.Fatalf("step %q failed: %+v", r.Step, r)
		}
	}
}

func TestRun_SleepStopsAtDeadline(t *testing.T) {
	t.Setenv("ACPWIRE_FAKE_AGENT", "valid")
	steps, lines, err := readJourney(writeJourney(t, `{"sleep":"30s"}`))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	results, err := run(os.Args[0], nil, steps, lines, t.TempDir(), 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("run took %v, want it to stop at the 200ms deadline", took)
	}
	if results[0].Pass || !strings.Contains(results[0].Detail, "deadline") {
		t.Fatalf("sleep result = %+v, want a failure that names the deadline", results[0])
	}
}

func TestRun_ExpectExpandsTemplates(t *testing.T) {
	tests := []struct {
		name, expect string
		wantPass     bool
	}{
		{"same value", `{"result.v":"{{first.result.id}}"}`, true},
		{"value in an array", `{"result.list":[{"id":"{{first.result.id}}"}]}`, true},
		{"other value", `{"result.v":"not-{{first.result.id}}"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ACPWIRE_FAKE_AGENT", "echo")
			steps, lines, err := readJourney(writeJourney(t,
				`{"send":"first","params":{"id":"sess-1"},"save":"first"}`,
				`{"send":"second","params":{"v":"sess-1","list":[{"id":"sess-1"}]},"expect":`+tt.expect+`}`,
				`{"close":true,"expectExit":0}`,
			))
			if err != nil {
				t.Fatal(err)
			}
			results, err := run(os.Args[0], nil, steps, lines, t.TempDir(), 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if got := results[1].Pass; got != tt.wantPass {
				t.Fatalf("second step = %+v, want pass=%v", results[1], tt.wantPass)
			}
		})
	}
}
