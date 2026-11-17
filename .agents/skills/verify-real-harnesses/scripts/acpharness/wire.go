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
// decode error that the SDK logged or answered on the wire, or an error
// response from the agent.
type finding struct {
	Kind     string `json:"kind"`   // methodNotFound, decodeError or agentError
	Source   string `json:"source"` // sdk-log, sdk-reply, agent-reply or driver
	Conn     int    `json:"conn"`
	Scenario string `json:"scenario"`
	Text     string `json:"text"`
}

const (
	methodNotFound = "method-not-found"
	decodeError    = "decode-error"
	agentError     = "agent-error"
)

// recorder owns the evidence that every connection of a run shares: the
// JSON-RPC transcript, the scenario label that tags each line, and the
// findings of the global predicate.
type recorder struct {
	start    time.Time
	scenario atomic.Value // string

	mu         sync.Mutex
	transcript *os.File
	writeErr   error // the first evidence write that failed
	findings   []finding
	inbound    map[string]int // agent->client methods by name
	// Notifications that the SDK must route, per connection, and how many
	// it has: a client callback or a "failed to handle notification" log.
	notified map[int]int
	handled  map[int]int
	// Agent requests per connection, and the SDK's replies to them.
	requested map[int]int
	answered  map[int]int
	// Client requests that wait for a response, by connection and id, and
	// the raw result of the last response to each method, which
	// advertised.go compares with what the SDK decoded.
	sent    map[int]map[string]string
	results map[int]map[string]json.RawMessage
	// The highest USD cost of each session on each connection, read from
	// raw usage_update notifications. The budget counts these, so a cost
	// that the SDK fails to decode still stops a paid run.
	costs map[string]float64
}

func newRecorder(dir string) (*recorder, error) {
	f, err := os.Create(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		return nil, err
	}
	r := &recorder{start: time.Now(), transcript: f, inbound: map[string]int{}, notified: map[int]int{}, handled: map[int]int{},
		requested: map[int]int{}, answered: map[int]int{}, sent: map[int]map[string]string{}, results: map[int]map[string]json.RawMessage{},
		costs: map[string]float64{}}
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
		Result json.RawMessage `json:"result"`
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
	if _, err := r.transcript.Write(append(b, '\n')); err != nil && r.writeErr == nil {
		r.writeErr = fmt.Errorf("transcript.jsonl: %w", err)
	}
	if dir == agentToClient && msg.Method == "session/update" {
		var n struct {
			Params struct {
				SessionID string `json:"sessionId"`
				Update    struct {
					Cost *struct {
						Amount   float64 `json:"amount"`
						Currency string  `json:"currency"`
					} `json:"cost"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &n) == nil && n.Params.Update.Cost != nil && strings.EqualFold(n.Params.Update.Cost.Currency, "USD") {
			key := fmt.Sprintf("%d/%s", conn, n.Params.SessionID)
			r.costs[key] = max(r.costs[key], n.Params.Update.Cost.Amount)
		}
	}
	if dir == agentToClient && msg.Method != "" {
		r.inbound[msg.Method]++
		// The SDK ignores _ extension notifications and handles
		// $/cancel_request itself, so neither reaches a callback or a log.
		if len(msg.ID) == 0 && !strings.HasPrefix(msg.Method, "_") && msg.Method != "$/cancel_request" {
			r.notified[conn]++
		}
		if len(msg.ID) > 0 {
			r.requested[conn]++
		}
	}
	if dir == clientToAgent && msg.Method == "" && len(msg.ID) > 0 {
		r.answered[conn]++
	}
	if dir == clientToAgent && msg.Method != "" && len(msg.ID) > 0 {
		if r.sent[conn] == nil {
			r.sent[conn] = map[string]string{}
		}
		r.sent[conn][string(msg.ID)] = msg.Method
	}
	if dir == agentToClient && msg.Method == "" && len(msg.ID) > 0 {
		if method, ok := r.sent[conn][string(msg.ID)]; ok {
			delete(r.sent[conn], string(msg.ID))
			if len(msg.Result) > 0 {
				if r.results[conn] == nil {
					r.results[conn] = map[string]json.RawMessage{}
				}
				r.results[conn][method] = msg.Result
			}
		}
	}
	// The SDK drops an error response whose request no longer waits, so
	// every error response from the agent is recorded here.
	if dir == agentToClient && msg.Method == "" && msg.Error != nil {
		r.findings = append(r.findings, finding{Kind: agentError, Source: "agent-reply", Conn: conn, Scenario: r.current(), Text: string(raw)})
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

// result returns the raw result of the last response to method on conn, or
// nil when none arrived. The forwarder records each agent line before the
// SDK reads it, so the result is here once the SDK call returns.
func (r *recorder) result(conn int, method string) json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.results[conn][method]
}

// cost returns the USD cost that the raw usage_update notifications
// report: the highest cost of each session on each connection, added up.
func (r *recorder) cost() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0.0
	for _, v := range r.costs {
		total += v
	}
	return total
}

// finding adds a finding that the driver itself found.
func (r *recorder) finding(kind, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findings = append(r.findings, finding{Kind: kind, Source: "driver", Scenario: r.current(), Text: text})
}

// classify returns the global-predicate kind of an SDK log text, or "".
func classify(text string) string {
	switch {
	case strings.Contains(text, "Method not found"), strings.Contains(text, "-32601"):
		return methodNotFound
	case strings.Contains(text, "failed to parse"),
		strings.Contains(text, "token too long"),
		strings.Contains(text, "cannot unmarshal"),
		strings.Contains(text, "unexpected end of JSON"),
		strings.Contains(text, "Invalid params"),
		strings.Contains(text, "-32602"),
		strings.Contains(text, "json: "):
		return decodeError
	}
	return ""
}

// handledOne counts a notification that the SDK routed on conn.
func (r *recorder) handledOne(conn int) {
	r.mu.Lock()
	r.handled[conn]++
	r.mu.Unlock()
}

// pending is how many notifications on conn the SDK has not routed yet,
// plus how many agent requests it has not answered.
func (r *recorder) pending(conn int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.notified[conn] - r.handled[conn] + r.requested[conn] - r.answered[conn]
}

// logged records an SDK log record as a finding when classify knows its
// text, or when it is an error that classify does not know, such as a
// message with neither id nor method: every SDK error log fails a row.
func (r *recorder) logged(conn int, level slog.Level, text string) {
	kind := classify(text)
	if kind == "" && level >= slog.LevelError {
		kind = decodeError
	}
	if kind == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findings = append(r.findings, finding{Kind: kind, Source: "sdk-log", Conn: conn, Scenario: r.current(), Text: text})
}

// lost records an evidence write that failed. The run fails on it.
func (r *recorder) lost(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr == nil {
		r.writeErr = err
	}
}

// close closes the transcript and returns the first evidence error.
func (r *recorder) close() error {
	if err := r.transcript.Close(); err != nil {
		r.lost(fmt.Errorf("transcript.jsonl: %w", err))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeErr
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
	h.rec.logged(h.conn, rec.Level, text.String())
	if rec.Message == "failed to handle notification" {
		h.rec.handledOne(h.conn)
	}
	rec.AddAttrs(slog.Int("conn", h.conn), slog.String("scenario", h.rec.current()))
	if err := h.next.Handle(ctx, rec); err != nil {
		h.rec.lost(fmt.Errorf("sdk.log: %w", err))
		return err
	}
	return nil
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
	rec     *recorder
	// The initialize result as the SDK decoded it and as it crossed the
	// wire. Scenarios decide what the agent advertises from raw.
	init    acp.InitializeResponse
	rawInit json.RawMessage
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
	// An agent that cleanup could not find must not run: stop it at once.
	if err := h.trackPgid(cmd.Process.Pid); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = logFile.Close()
		return nil, fmt.Errorf("record the process group of %s: %w", h.opts.agent[0], err)
	}

	a := &agentConn{n: n, cmd: cmd, stdin: stdin, exited: make(chan struct{}), rec: h.rec}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		name := logFile.Name()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			if _, err := fmt.Fprintf(logFile, "[+%dms %s] %s\n", h.rec.ms(), h.rec.current(), sc.Text()); err != nil {
				h.rec.lost(fmt.Errorf("%s: %w", name, err))
			}
		}
		if err := sc.Err(); err != nil {
			h.rec.lost(fmt.Errorf("%s: read agent stderr: %w", name, err))
		}
		if err := logFile.Close(); err != nil {
			h.rec.lost(fmt.Errorf("%s: %w", name, err))
		}
	}()

	// The SDK reads from a pipe that this goroutine fills line by line, so
	// each line from the agent is recorded before the SDK sees it. The SDK
	// starts reading when the connection is made, so the goroutine waits for
	// ready: a line that the SDK cannot parse must reach the tapped logger,
	// not the default one.
	pr, pw := io.Pipe()
	ready := make(chan struct{})
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		<-ready
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
	close(ready)
	// When the SDK stops reading, such as after a line over its size limit,
	// unblock the forwarder so that the agent can still be reaped.
	go func() {
		<-a.conn.Done()
		_ = pr.Close()
	}()
	h.conns = append(h.conns, a)
	return a, nil
}

// close ends the agent the way an editor does: it closes stdin and waits.
// An agent that is still running after grace gets SIGTERM, then SIGKILL,
// sent to its own process group. A second close returns the first result.
func (a *agentConn) close(grace time.Duration) string {
	if a.ended == "" {
		a.ended = a.stop(grace)
		// The SDK may still be reading the agent's last lines, and it routes
		// queued notifications after its reader stops. Wait for both, so that
		// their decode errors reach the findings.
		deadline := time.After(5 * time.Second)
		select {
		case <-a.conn.Done():
		case <-deadline:
			return a.ended
		}
		for a.rec.pending(a.n) > 0 {
			select {
			case <-deadline:
				return a.ended
			case <-time.After(20 * time.Millisecond):
			}
		}
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
