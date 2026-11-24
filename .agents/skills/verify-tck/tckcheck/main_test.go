package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// req is one requirement of a test report. msg is the message of its one
// test.
type req struct{ id, tier, status, msg string }

// reportJSON writes an acp-tck report with the field names of a real one
// (acp-tck 0.5.0 --report-json).
func reportJSON(t *testing.T, dir string, conformant bool, blockedByAuth bool, reqs ...req) string {
	t.Helper()
	rs := []map[string]any{}
	for _, q := range reqs {
		rs = append(rs, map[string]any{
			"id": q.id, "tier": q.tier, "capability": nil, "text": "t", "citation": "c", "status": q.status,
			"tests": []map[string]any{{
				"nodeid": "test_x.py::test_" + q.id, "status": q.status, "message": q.msg,
				"duration_s": 0.1, "properties": map[string]any{}, "transcript": nil, "stderr": nil,
			}},
		})
	}
	return writeJSON(t, dir, "report.json", map[string]any{
		"tck_version": "0.5.0", "protocol_version": 1, "schema_revision": "3ea94133bb4e7cfdb1105df75660ff83e66acf62",
		"requirements": rs,
		"verdict": map[string]any{
			"conformant": conformant, "blocked_by_auth": blockedByAuth, "blocked_by_version_mismatch": false,
			"tier_counts": map[string]any{},
		},
	})
}

// baselineJSON writes a baseline for the report reportJSON writes. Each entry
// is "TIER STATUS" or "TIER STATUS reason".
func baselineJSON(t *testing.T, dir string, conformant bool, flaky []string, entries map[string]string) string {
	t.Helper()
	es := map[string]any{}
	for id, e := range entries {
		f := strings.SplitN(e, " ", 3)
		m := map[string]any{"tier": f[0], "status": f[1]}
		if len(f) == 3 {
			m["reason"] = f[2]
		}
		es[id] = m
	}
	b := map[string]any{
		"tckVersion": "0.5.0", "schemaRevision": "3ea94133bb4e7cfdb1105df75660ff83e66acf62",
		"conformant": conformant, "requirements": es,
	}
	if flaky != nil {
		b["flaky"] = flaky
	}
	return writeJSON(t, dir, "baseline.json", b)
}

func writeJSON(t *testing.T, dir, name string, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// failMsg is the shape of a pytest failure message in a report.
const failMsg = "agent_launch = AgentLaunch(command=['agent'])\n\n    def test_x():\n>       assert ok\nE       AssertionError: expected code -32002, got -32603\nE       assert False\n\nsrc/tck/v1/conformance/test_errors.py:78: AssertionError"

func runCheck(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String() + errOut.String()
}

func TestCheck_Drift(t *testing.T) {
	pass := req{"ACP-A-001", "MANDATORY", "PASS", ""}
	skip := req{"ACP-B-001", "CAPABILITY", "SKIPPED", "('/x/test_b.py', 12, \"Skipped: capability 'agentCapabilities.x' not advertised by the agent under test\")"}
	cases := []struct {
		name       string
		conformant bool
		blocked    bool
		reqs       []req
		baseline   map[string]string
		flaky      []string
		// wantLines are the drift lines, in order; none means a match.
		wantLines []string
	}{{
		name:       "match",
		conformant: true,
		reqs:       []req{pass, skip},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
	}, {
		name:       "status drift names the first failure line",
		conformant: false,
		reqs:       []req{{"ACP-A-001", "MANDATORY", "FAIL", failMsg}, skip},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
		wantLines: []string{
			"run: expected conformant, got not conformant",
			"ACP-A-001 MANDATORY: expected PASS, got FAIL: AssertionError: expected code -32002, got -32603",
		},
	}, {
		name:       "a skip that starts to pass drifts",
		conformant: true,
		reqs:       []req{pass, {"ACP-B-001", "CAPABILITY", "PASS", ""}},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
		wantLines:  []string{"ACP-B-001 CAPABILITY: expected SKIPPED, got PASS"},
	}, {
		name:       "a requirement the baseline lacks",
		conformant: true,
		reqs:       []req{pass, skip},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS"},
		wantLines:  []string{"ACP-B-001 CAPABILITY: expected no entry, got SKIPPED: Skipped: capability 'agentCapabilities.x' not advertised by the agent under test"},
	}, {
		name:       "an entry the report lacks",
		conformant: true,
		reqs:       []req{pass},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
		wantLines:  []string{"ACP-B-001 CAPABILITY: expected SKIPPED, got no requirement"},
	}, {
		name:       "a tier change drifts",
		conformant: true,
		reqs:       []req{pass, {"ACP-B-001", "ADVISORY", "SKIPPED", skip.msg}},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
		wantLines:  []string{"ACP-B-001 ADVISORY: expected tier CAPABILITY, got ADVISORY"},
	}, {
		name:       "a flaky entry may change status",
		conformant: true,
		reqs:       []req{pass, {"ACP-B-001", "CAPABILITY", "PASS", ""}},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS", "ACP-B-001": "CAPABILITY SKIPPED the cancel race decides it"},
		flaky:      []string{"ACP-B-001"},
	}, {
		name:       "a flaky entry needs a reason",
		conformant: true,
		reqs:       []req{pass, skip},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
		flaky:      []string{"ACP-A-001"},
		wantLines:  []string{"ACP-A-001 MANDATORY: expected a reason, got none"},
	}, {
		name:       "a flaky requirement may flip the verdict",
		conformant: false,
		reqs:       []req{{"ACP-A-001", "MANDATORY", "FAIL", failMsg}, skip},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS the cancel race decides it", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
		flaky:      []string{"ACP-A-001"},
	}, {
		name:       "a passing requirement must not keep a reason",
		conformant: true,
		reqs:       []req{pass, skip},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS an old SDK gap", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
		wantLines:  []string{"ACP-A-001 MANDATORY: expected no reason, got a reason for a PASS requirement"},
	}, {
		name:       "a flaky id the report lacks drifts",
		conformant: true,
		reqs:       []req{pass, skip},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
		flaky:      []string{"ACP-Z-001"},
		wantLines:  []string{"ACP-Z-001 flaky: expected a flaky requirement, got no requirement"},
	}, {
		name:       "a non-PASS entry needs a reason that is not TODO",
		conformant: true,
		reqs:       []req{pass, skip},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS", "ACP-B-001": "CAPABILITY SKIPPED TODO"},
		wantLines:  []string{"ACP-B-001 CAPABILITY: expected a reason, got TODO"},
	}, {
		name:       "NOT_TESTED is drift even when the baseline names it",
		conformant: false,
		reqs:       []req{{"ACP-A-001", "MANDATORY", "NOT_TESTED", ""}, skip},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY NOT_TESTED the agent died", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
		wantLines: []string{
			"run: expected conformant, got not conformant",
			"ACP-A-001 MANDATORY: expected a tested requirement, got NOT_TESTED",
		},
	}, {
		name:       "a run blocked by authentication is drift",
		conformant: false,
		blocked:    true,
		reqs:       []req{pass, skip},
		baseline:   map[string]string{"ACP-A-001": "MANDATORY PASS", "ACP-B-001": "CAPABILITY SKIPPED x is not advertised"},
		wantLines: []string{
			"run: expected conformant, got not conformant",
			"run: expected a run that authenticated, got blocked_by_auth",
		},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rep := reportJSON(t, dir, tc.conformant, tc.blocked, tc.reqs...)
			base := baselineJSON(t, dir, true, tc.flaky, tc.baseline)
			code, out := runCheck(t, "-report", rep, "-baseline", base)
			var got []string
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				if !strings.HasPrefix(l, "tck ") {
					got = append(got, l)
				}
			}
			if strings.Join(got, "\n") != strings.Join(tc.wantLines, "\n") {
				t.Errorf("drift lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.wantLines, "\n"))
			}
			if want := map[bool]int{true: 1, false: 0}[len(tc.wantLines) > 0]; code != want {
				t.Errorf("exit %d, want %d", code, want)
			}
		})
	}
}

func TestCheck_WrongTCKRelease(t *testing.T) {
	dir := t.TempDir()
	rep := reportJSON(t, dir, true, false, req{"ACP-A-001", "MANDATORY", "PASS", ""})
	base := writeJSON(t, dir, "baseline.json", map[string]any{
		"tckVersion": "0.4.0", "schemaRevision": "3ea94133bb4e7cfdb1105df75660ff83e66acf62", "conformant": true,
		"requirements": map[string]any{"ACP-A-001": map[string]any{"tier": "MANDATORY", "status": "PASS"}},
	})
	code, out := runCheck(t, "-report", rep, "-baseline", base)
	if code != 1 || !strings.Contains(out, "run: expected tck 0.4.0, got 0.5.0") {
		t.Errorf("exit %d, output:\n%s", code, out)
	}
}

func TestUpdate_KeepsReasons(t *testing.T) {
	dir := t.TempDir()
	rep := reportJSON(
		t, dir, true, false,
		req{"ACP-A-001", "MANDATORY", "PASS", ""},
		req{"ACP-B-001", "CAPABILITY", "SKIPPED", "Skipped: x"},
		req{"ACP-C-001", "ADVISORY", "FAIL", failMsg},
		req{"ACP-D-001", "ADVISORY", "PASS", ""},
		req{"ACP-F-001", "ADVISORY", "PASS", ""},
	)
	// ACP-B keeps its reason, ACP-C is new and not PASS, ACP-D now passes,
	// ACP-E is gone, ACP-F is flaky and keeps its reason though it passes.
	base := baselineJSON(t, dir, false, []string{"ACP-F-001", "ACP-E-001"}, map[string]string{
		"ACP-A-001": "MANDATORY PASS",
		"ACP-B-001": "CAPABILITY SKIPPED x is not advertised",
		"ACP-D-001": "ADVISORY FAIL the SDK lacks y",
		"ACP-E-001": "ADVISORY FAIL removed upstream",
		"ACP-F-001": "ADVISORY FAIL the cancel race decides it",
	})
	code, out := runCheck(t, "-update", "-report", rep, "-baseline", base)
	if code != 1 || !strings.Contains(out, "ACP-C-001 ADVISORY: expected a reason, got TODO") {
		t.Errorf("exit %d, want 1 with the TODO named; output:\n%s", code, out)
	}
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		TCKVersion string                       `json:"tckVersion"`
		Conformant bool                         `json:"conformant"`
		Flaky      []string                     `json:"flaky"`
		Reqs       map[string]map[string]string `json:"requirements"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{
		"ACP-A-001": {"tier": "MANDATORY", "status": "PASS"},
		"ACP-B-001": {"tier": "CAPABILITY", "status": "SKIPPED", "reason": "x is not advertised"},
		"ACP-C-001": {"tier": "ADVISORY", "status": "FAIL", "reason": "TODO"},
		"ACP-D-001": {"tier": "ADVISORY", "status": "PASS"},
		"ACP-F-001": {"tier": "ADVISORY", "status": "PASS", "reason": "the cancel race decides it"},
	}
	gotJSON, _ := json.Marshal(got.Reqs)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("requirements:\n%s\nwant:\n%s", gotJSON, wantJSON)
	}
	if !got.Conformant || got.TCKVersion != "0.5.0" || strings.Join(got.Flaky, ",") != "ACP-F-001" {
		t.Errorf("conformant %v, tck %q, flaky %v; want true, 0.5.0, [ACP-F-001]", got.Conformant, got.TCKVersion, got.Flaky)
	}

	// Once a person writes the reason, the updated baseline matches.
	fixed := strings.Replace(string(data), `"TODO"`, `"the SDK lacks z"`, 1)
	if err := os.WriteFile(base, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := runCheck(t, "-report", rep, "-baseline", base); code != 0 {
		t.Errorf("exit %d after the reason was written, output:\n%s", code, out)
	}
	// A second update leaves a matching baseline byte for byte.
	if code, out := runCheck(t, "-update", "-report", rep, "-baseline", base); code != 0 {
		t.Errorf("second update: exit %d, output:\n%s", code, out)
	}
	again, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal([]byte(fixed), &a)
	_ = json.Unmarshal(again, &b)
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	if string(aj) != string(bj) {
		t.Errorf("second update changed the baseline:\n%s\nwas:\n%s", again, fixed)
	}
}

func TestUpdate_WritesAMissingBaseline(t *testing.T) {
	dir := t.TempDir()
	rep := reportJSON(t, dir, true, false, req{"ACP-A-001", "MANDATORY", "PASS", ""})
	base := filepath.Join(dir, "baseline.json")
	if code, out := runCheck(t, "-update", "-report", rep, "-baseline", base); code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
	if code, out := runCheck(t, "-report", rep, "-baseline", base); code != 0 {
		t.Errorf("check of the written baseline: exit %d, output:\n%s", code, out)
	}
}

func TestRun_BadInput(t *testing.T) {
	dir := t.TempDir()
	rep := reportJSON(t, dir, true, false, req{"ACP-A-001", "MANDATORY", "PASS", ""})
	trunc := filepath.Join(dir, "trunc.json")
	if err := os.WriteFile(trunc, []byte(`{"requirements": [`), 0o644); err != nil {
		t.Fatal(err)
	}
	typo := writeJSON(t, dir, "typo.json", map[string]any{
		"tckVersion": "0.5.0", "schemaRevision": "3ea94133bb4e7cfdb1105df75660ff83e66acf62", "conformant": true,
		"requirements": map[string]any{"ACP-A-001": map[string]any{"tier": "MANDATORY", "status": "PASS", "reasn": "x"}},
	})
	for name, args := range map[string][]string{
		"no flags":           {},
		"baseline typo":      {"-report", rep, "-baseline", typo},
		"missing baseline":   {"-report", rep, "-baseline", filepath.Join(dir, "none.json")},
		"truncated report":   {"-report", trunc, "-baseline", rep},
		"missing report":     {"-report", filepath.Join(dir, "none.json"), "-baseline", rep},
		"unexpected operand": {"-report", rep, "-baseline", rep, "extra"},
	} {
		if code, out := runCheck(t, args...); code != 2 {
			t.Errorf("%s: exit %d, want 2; output:\n%s", name, code, out)
		}
	}
}
