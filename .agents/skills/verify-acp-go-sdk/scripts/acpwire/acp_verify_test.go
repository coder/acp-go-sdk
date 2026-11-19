package main

// Tests for the acp-verify script. They live in this package so that
// `make test`, which runs the acpwire tests, runs them too. Each test makes a
// run folder by hand with small shell scripts in place of the example
// binaries, so no test builds the SDK.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const acpVerify = "../acp-verify"

// fakeRun makes a run folder that acp-verify accepts, with the given scripts
// as the example client and agent.
func fakeRun(t *testing.T, client, agent string) string {
	t.Helper()
	for _, tool := range []string{"bash", "ps", "timeout"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("acp-verify needs %s: %v", tool, err)
		}
	}
	run := t.TempDir()
	bin := filepath.Join(run, "work", "bin")
	for _, dir := range []string{bin, filepath.Join(run, "evidence")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(run, ".acp-verify-run"):    "fake\n",
		filepath.Join(run, "work", "pids"):       "",
		filepath.Join(bin, "acp-example-client"): "#!/usr/bin/env bash\n" + client,
		filepath.Join(bin, "acp-example-agent"):  "#!/usr/bin/env bash\n" + agent,
	}
	for path, text := range files {
		if err := os.WriteFile(path, []byte(text), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

// writePID is a script line that writes the shell's pid to $ACP_TEST_PIDFILE
// in one rename, so a reader never sees half of it.
const writePID = `echo $$ >"$ACP_TEST_PIDFILE.tmp" && mv "$ACP_TEST_PIDFILE.tmp" "$ACP_TEST_PIDFILE"` + "\n"

// sleepIgnoringTERM is a script tail that keeps running after SIGTERM. An
// ignored signal stays ignored across exec.
const sleepIgnoringTERM = "trap '' TERM\n" + writePID + "exec sleep 300\n"

func acpVerifyCmd(run string, env []string, args ...string) *exec.Cmd {
	cmd := exec.Command("bash", append([]string{acpVerify}, args...)...)
	cmd.Env = append(os.Environ(), append([]string{"ACP_VERIFY_RUN=" + run}, env...)...)
	return cmd
}

// waitForPID reads the pid that a fake binary wrote, and kills it when the
// test ends, so that a failing test leaves nothing behind.
func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid in %s after 10s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// running reports whether pid is a process that has not exited. A zombie has
// exited: only its parent has not read its status yet.
func running(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	state := strings.TrimSpace(string(out))
	return err == nil && state != "" && !strings.HasPrefix(state, "Z")
}

// TestAcpVerify_CleanupStopsTheAgentThatTheClientStarted checks that cleanup
// stops an agent that outlives the client that started it, even when the
// agent ignores SIGTERM.
func TestAcpVerify_CleanupStopsTheAgentThatTheClientStarted(t *testing.T) {
	run := fakeRun(t, `"$1" </dev/null >/dev/null 2>&1 &`+"\n", sleepIgnoringTERM)
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	env := []string{"ACP_TEST_PIDFILE=" + pidFile, "ACP_VERIFY_STOP_GRACE=1"}
	if out, err := acpVerifyCmd(run, env, "client", "orphan", "").CombinedOutput(); err != nil {
		t.Fatalf("acp-verify client: %v\n%s", err, out)
	}
	agent := waitForPID(t, pidFile)
	if !running(agent) {
		t.Fatalf("agent pid %d is not running before cleanup", agent)
	}
	out, err := acpVerifyCmd(run, env, "cleanup").CombinedOutput()
	if err != nil {
		t.Fatalf("acp-verify cleanup: %v\n%s", err, out)
	}
	if running(agent) {
		t.Fatalf("agent pid %d still runs after cleanup printed:\n%s", agent, out)
	}
}

// TestAcpVerify_CleanupWaitsForARecordedProcess checks that cleanup reports a
// recorded process as stopped only once it is gone. The client here ignores
// SIGTERM, and acp-verify itself is killed mid-run, as when a tool call hits
// its time limit.
func TestAcpVerify_CleanupWaitsForARecordedProcess(t *testing.T) {
	run := fakeRun(t, sleepIgnoringTERM, "exit 0\n")
	pidFile := filepath.Join(t.TempDir(), "client.pid")
	env := []string{"ACP_TEST_PIDFILE=" + pidFile, "ACP_VERIFY_STOP_GRACE=1", "ACP_VERIFY_TIMEOUT=300"}
	log, err := os.Create(filepath.Join(t.TempDir(), "client.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	drive := acpVerifyCmd(run, env, "client", "hung", "")
	drive.Stdout, drive.Stderr = log, log
	if err := drive.Start(); err != nil {
		t.Fatal(err)
	}
	client := waitForPID(t, pidFile)
	_ = drive.Process.Kill()
	_ = drive.Wait()
	out, err := acpVerifyCmd(run, env, "cleanup").CombinedOutput()
	if err != nil {
		t.Fatalf("acp-verify cleanup: %v\n%s", err, out)
	}
	if running(client) {
		t.Fatalf("client pid %d still runs after cleanup printed:\n%s", client, out)
	}
	if !strings.Contains(string(out), "stopped") {
		t.Fatalf("cleanup output does not report the stopped client:\n%s", out)
	}
}

// TestAcpVerify_LaunchRefusesARootThatAnACLOpens checks that launch refuses a
// root folder that a POSIX ACL entry lets another user write to. Such an
// entry raises the ACL mask, and the mask is what the group mode bits show.
func TestAcpVerify_LaunchRefusesARootThatAnACLOpens(t *testing.T) {
	if _, err := exec.LookPath("setfacl"); err != nil {
		t.Skip("no setfacl")
	}
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("setfacl", "-m", "u:nobody:rwx", root).CombinedOutput(); err != nil {
		t.Skipf("this file system takes no ACL: %v\n%s", err, out)
	}
	cmd := exec.Command("bash", acpVerify, "launch")
	cmd.Env = append(os.Environ(), "ACP_VERIFY_ROOT="+root)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "other users can write") {
		t.Fatalf("launch with an ACL-opened root: err=%v, output:\n%s\nwant exit 2 and a refusal", err, out)
	}
}
