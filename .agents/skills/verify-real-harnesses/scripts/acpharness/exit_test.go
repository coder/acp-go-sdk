package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain runs the driver itself when a test starts this binary with
// ACPHARNESS_AS_DRIVER=1, so a test can read the driver's exit code.
func TestMain(m *testing.M) {
	if os.Getenv("ACPHARNESS_AS_DRIVER") == "1" {
		main()
	}
	os.Exit(m.Run())
}

// TestDriver_UsageErrorsExit64 pins the exit code that g5 reads as "the
// driver rejected its arguments". It must differ from 2, the exit code of
// a Go panic, or g5 deletes the evidence of a driver that died.
func TestDriver_UsageErrorsExit64(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]string{
		"unknown flag":      {"-bogus"},
		"no -out":           {"-cwd", dir, "--", "agent"},
		"no agent":          {"-out", dir, "-cwd", dir},
		"unknown profile":   {"-out", dir, "-cwd", dir, "-profile", "nope", "--", "agent"},
		"unknown -only one": {"-out", dir, "-cwd", dir, "-only", "prompt,nope", "--", "agent"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(os.Environ(), "ACPHARNESS_AS_DRIVER=1")
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 64 {
				first, _, _ := strings.Cut(string(out), "\n")
				t.Fatalf("driver %v: %v, want exit 64; first output line: %s", args, err, first)
			}
		})
	}
}

// stubDriver stands in for acpharness in a fake run folder. With STUB_EXIT
// 64 it rejects its arguments, as the driver does before it starts an
// agent. Else it records one paid usage_update in the transcript, leaves a
// tagged process running, as the agent of a driver that died would, writes
// a panic to stderr, and exits with STUB_EXIT.
const stubDriver = `#!/usr/bin/env bash
if [ "$STUB_EXIT" = 64 ]; then
	echo "acpharness: -only names \"nope\", which is not a live scenario" >&2
	exit 64
fi
while [ $# -gt 0 ]; do
	[ "$1" = -out ] && out=$2
	shift
done
printf '%s\n' '{"t_ms":1,"conn":1,"scenario":"prompt","dir":"agent->client","msg":{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"usage_update","used":1,"size":2,"cost":{"amount":0.42,"currency":"USD"}}}}}' >"$out/transcript.jsonl"
sleep 30 </dev/null >/dev/null 2>&1 &
echo "panic: stub driver died" >&2
exit "$STUB_EXIT"
`

// fakeRun makes a run folder that harness accepts, with stubDriver as the
// driver, and returns it.
func fakeRun(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the harness finds leftover processes through /proc")
	}
	for _, tool := range []string{"bash", "jq", "timeout", "pgrep", "ps"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("harness needs %s", tool)
		}
	}
	// The run folder's name is the tag that stop_tagged looks for, so it
	// must be unique on this host.
	run := filepath.Join(t.TempDir(), fmt.Sprintf("stub-run-%d-%d", os.Getpid(), time.Now().UnixNano()))
	for _, d := range []string{"work/bin", "evidence"} {
		if err := os.MkdirAll(filepath.Join(run, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		".acp-harness-run":       "",
		"work/pgids":             "",
		"work/pids":              "",
		"work/bin/acpharness":    stubDriver,
		"evidence/versions.json": `{"source":"stub","schema":"stub","node":"stub","go":"stub","claude":{},"codex":{},"ts":{}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(run, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

// harnessCmd runs the harness script on run and returns its output and
// exit code.
func harnessCmd(t *testing.T, run, stubExit string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"../harness"}, args...)...)
	// The stub needs no credential, but g5 checks that the variable is set.
	cmd.Env = append(os.Environ(), "HARNESS_RUN="+run, "STUB_EXIT="+stubExit, "ANTHROPIC_API_KEY=stub")
	// A broken harness can leave the stub's sleep holding the output pipe.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("harness %v: %v", args, err)
	}
	return string(out), cmd.ProcessState.ExitCode()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("evidence lost: %v", err)
	}
	return string(b)
}

// TestHarnessG5_KeepsTheEvidenceOfADriverThatDies: a Go panic exits 2.
// The run happened and may have cost money, so g5 must keep its evidence
// and stop its agents, and report must show it as INCOMPLETE, count its
// cost and fail.
func TestHarnessG5_KeepsTheEvidenceOfADriverThatDies(t *testing.T) {
	run := fakeRun(t)
	out, code := harnessCmd(t, run, "2", "g5", "claude", "prompt")
	if code != 2 {
		t.Fatalf("g5 exited %d, want the driver's 2; output:\n%s", code, out)
	}
	if !strings.Contains(out, "INCOMPLETE") {
		t.Errorf("g5 did not say that the run is INCOMPLETE; output:\n%s", out)
	}
	ev := filepath.Join(run, "evidence", "g5-claude")
	if got := readFile(t, filepath.Join(ev, "transcript.jsonl")); !strings.Contains(got, "usage_update") {
		t.Errorf("transcript.jsonl lost its usage_update: %s", got)
	}
	if got := readFile(t, filepath.Join(ev, "driver.stderr.log")); !strings.Contains(got, "panic: stub driver died") {
		t.Errorf("driver.stderr.log does not hold the panic: %q", got)
	}
	if got := readFile(t, filepath.Join(ev, "leftovers.txt")); !strings.Contains(got, "sleep 30") {
		t.Errorf("leftovers.txt does not name the process that the dead driver left: %q", got)
	}

	out, code = harnessCmd(t, run, "2", "report")
	if code == 0 {
		t.Errorf("report exited 0 with an INCOMPLETE run; output:\n%s", out)
	}
	report := readFile(t, filepath.Join(run, "evidence", "report.md"))
	for _, want := range []string{"g5-claude: INCOMPLETE", "Its transcript reports $0.42", "from the transcripts: $0.42", "panic: stub driver died"} {
		if !strings.Contains(report, want) {
			t.Errorf("report.md lacks %q:\n%s", want, report)
		}
	}
}

// TestHarnessG5_RecordsNoRunWhenTheDriverRejectsItsArguments: exit 64
// means that the driver started no agent, so g5 frees the label.
func TestHarnessG5_RecordsNoRunWhenTheDriverRejectsItsArguments(t *testing.T) {
	run := fakeRun(t)
	out, code := harnessCmd(t, run, "64", "g5", "claude", "prompt")
	if code != 64 {
		t.Fatalf("g5 exited %d, want 64; output:\n%s", code, out)
	}
	if !strings.Contains(out, "rejected its arguments") {
		t.Errorf("g5 did not say that the driver rejected its arguments; output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(run, "evidence", "g5-claude")); !os.IsNotExist(err) {
		t.Errorf("g5 kept a run folder for rejected arguments: %v", err)
	}
}
