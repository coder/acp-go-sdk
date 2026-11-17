package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// A finding is one hit of the global predicate: a "Method not found" or a
// decode error that the SDK logged or answered on the wire.
type finding struct {
	Kind     string `json:"kind"`   // methodNotFound or decodeError
	Source   string `json:"source"` // sdk-log or sdk-reply
	Conn     int    `json:"conn"`
	Scenario string `json:"scenario"`
	Text     string `json:"text"`
}

const (
	methodNotFound = "method-not-found"
	decodeError    = "decode-error"
)

// recorder owns the evidence that every connection of a run shares: the
// JSON-RPC transcript, the scenario label that tags each line, and the
// findings of the global predicate.
type recorder struct {
	start    time.Time
	scenario atomic.Value // string

	mu         sync.Mutex
	transcript *os.File
	findings   []finding
	inbound    map[string]int // agent->client methods by name
}

func newRecorder(dir string) (*recorder, error) {
	f, err := os.Create(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		return nil, err
	}
	r := &recorder{start: time.Now(), transcript: f, inbound: map[string]int{}}
	r.scenario.Store("setup")
	return r, nil
}

func (r *recorder) current() string { return r.scenario.Load().(string) }

func (r *recorder) ms() int64 { return time.Since(r.start).Milliseconds() }

// line records one JSON-RPC line and checks the SDK's own replies for errors.
func (r *recorder) line(conn int, dir string, raw []byte) {
	entry := map[string]any{"t_ms": r.ms(), "conn": conn, "scenario": r.current(), "dir": dir}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		entry["raw"] = string(raw)
	} else {
		entry["msg"] = json.RawMessage(raw)
	}
	b, _ := json.Marshal(entry)

	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.transcript.Write(append(b, '\n'))
	if dir == agentToClient && msg.Method != "" {
		r.inbound[msg.Method]++
	}
	// The SDK answers an agent request that it cannot dispatch or decode
	// with an error, without logging it. Count those replies too.
	if dir == clientToAgent && msg.Method == "" && msg.Error != nil {
		kind := ""
		switch msg.Error.Code {
		case -32601:
			kind = methodNotFound
		case -32602, -32700:
			kind = decodeError
		}
		if kind != "" {
			r.findings = append(r.findings, finding{Kind: kind, Source: "sdk-reply", Conn: conn, Scenario: r.current(), Text: string(raw)})
		}
	}
}

// classify returns the global-predicate kind of an SDK log text, or "".
func classify(text string) string {
	switch {
	case strings.Contains(text, "Method not found"), strings.Contains(text, "-32601"):
		return methodNotFound
	case strings.Contains(text, "failed to parse"),
		strings.Contains(text, "cannot unmarshal"),
		strings.Contains(text, "unexpected end of JSON"),
		strings.Contains(text, "Invalid params"),
		strings.Contains(text, "-32602"),
		strings.Contains(text, "json: "):
		return decodeError
	}
	return ""
}

func (r *recorder) logged(conn int, text string) {
	kind := classify(text)
	if kind == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findings = append(r.findings, finding{Kind: kind, Source: "sdk-log", Conn: conn, Scenario: r.current(), Text: text})
}

func (r *recorder) snapshot() ([]finding, map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inbound := make(map[string]int, len(r.inbound))
	for k, v := range r.inbound {
		inbound[k] = v
	}
	return append([]finding(nil), r.findings...), inbound
}

const (
	clientToAgent = "client->agent"
	agentToClient = "agent->client"
)

// logTap writes the SDK's slog records to sdk.log, tagged with the
// connection and scenario, and feeds their text to the global predicate.
type logTap struct {
	next slog.Handler
	rec  *recorder
	conn int
}

func (h *logTap) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *logTap) Handle(ctx context.Context, rec slog.Record) error {
	var text strings.Builder
	text.WriteString(rec.Message)
	rec.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&text, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	h.rec.logged(h.conn, text.String())
	rec.AddAttrs(slog.Int("conn", h.conn), slog.String("scenario", h.rec.current()))
	return h.next.Handle(ctx, rec)
}

func (h *logTap) WithAttrs(as []slog.Attr) slog.Handler {
	return &logTap{next: h.next.WithAttrs(as), rec: h.rec, conn: h.conn}
}

func (h *logTap) WithGroup(name string) slog.Handler {
	return &logTap{next: h.next.WithGroup(name), rec: h.rec, conn: h.conn}
}

// tapWriter records each line that the SDK writes to the agent.
type tapWriter struct {
	w    io.Writer
	rec  *recorder
	conn int
	buf  []byte
}

func (t *tapWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		if line := bytes.TrimSpace(t.buf[:i]); len(line) > 0 {
			t.rec.line(t.conn, clientToAgent, line)
		}
		t.buf = t.buf[i+1:]
	}
	return t.w.Write(p)
}

// agentConn is one agent process and the SDK connection that talks to it.
type agentConn struct {
	n       int
	cmd     *exec.Cmd
	conn    *acp.ClientSideConnection
	client  *harnessClient
	stdin   io.WriteCloser
	exited  chan struct{}
	waitErr error
	ended   string // how close ended the agent; empty while it runs
}

// startAgent starts the agent command in its own process group, so that
// close can stop the agent and every process it started, and nothing else.
func (h *harness) startAgent() (*agentConn, error) {
	n := len(h.conns) + 1
	cmd := exec.Command(h.opts.agent[0], h.opts.agent[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	logFile, err := os.Create(filepath.Join(h.opts.out, fmt.Sprintf("agent-%d.stderr.log", n)))
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start %s: %w", h.opts.agent[0], err)
	}
	h.trackPgid(cmd.Process.Pid)

	a := &agentConn{n: n, cmd: cmd, stdin: stdin, exited: make(chan struct{})}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		defer func() { _ = logFile.Close() }()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			_, _ = fmt.Fprintf(logFile, "[+%dms %s] %s\n", h.rec.ms(), h.rec.current(), sc.Text())
		}
	}()

	// The SDK reads from a pipe that this goroutine fills line by line, so
	// each line from the agent is recorded before the SDK sees it.
	pr, pw := io.Pipe()
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(bytes.TrimSpace(line)) > 0 {
				h.rec.line(n, agentToClient, line)
			}
			if _, err := pw.Write(append(append([]byte(nil), line...), '\n')); err != nil {
				break
			}
		}
		pw.CloseWithError(sc.Err())
	}()
	go func() {
		// Wait closes the pipes, so it runs after both readers are done.
		<-stderrDone
		<-stdoutDone
		a.waitErr = cmd.Wait()
		close(a.exited)
	}()

	a.client = newHarnessClient(h, n)
	a.conn = acp.NewClientSideConnection(a.client, &tapWriter{w: stdin, rec: h.rec, conn: n}, pr)
	a.conn.SetLogger(slog.New(&logTap{next: h.sdkLog, rec: h.rec, conn: n}))
	h.conns = append(h.conns, a)
	return a, nil
}

// close ends the agent the way an editor does: it closes stdin and waits.
// An agent that is still running after grace gets SIGTERM, then SIGKILL,
// sent to its own process group. A second close returns the first result.
func (a *agentConn) close(grace time.Duration) string {
	if a.ended == "" {
		a.ended = a.stop(grace)
	}
	return a.ended
}

func (a *agentConn) stop(grace time.Duration) string {
	_ = a.stdin.Close()
	select {
	case <-a.exited:
		return exitText(a.waitErr)
	case <-time.After(grace):
	}
	_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-a.exited:
		return "stopped with SIGTERM after " + grace.String()
	case <-time.After(5 * time.Second):
	}
	_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGKILL)
	<-a.exited
	return "killed after " + grace.String()
}

func exitText(err error) string {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return "exit 0"
	case errors.As(err, &ee):
		return ee.String()
	default:
		return err.Error()
	}
}
