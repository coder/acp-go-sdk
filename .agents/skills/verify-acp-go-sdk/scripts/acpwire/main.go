// Command acpwire drives an ACP agent over stdio the way an editor does. It
// starts the agent, writes JSON-RPC lines to its stdin, reads its stdout,
// answers the agent's own requests from rules in a journey file, checks the
// expectations in that file, and records every line in both directions.
//
// Usage:
//
//	acpwire -agent BIN -journey FILE -out DIR [-timeout 60s] [-- AGENT_ARGS...]
//
// It writes DIR/transcript.jsonl, DIR/agent.stderr.log and DIR/summary.json.
// Exit 0: every step passed. Exit 1: a step failed. Exit 2: usage error.
//
// A journey has one JSON object per line. Lines that start with # are comments.
//
//	{"send":"initialize","params":{...},"save":"init","expect":{"result.protocolVersion":1}}
//	{"send":"session/prompt","params":{...},"save":"turn","async":true}
//	{"await":"turn","expect":{"result.stopReason":"cancelled"}}
//	{"notify":"session/cancel","params":{"sessionId":"{{new.result.sessionId}}"}}
//	{"respond":"session/request_permission","result":{...}}  (rule for later agent requests)
//	{"waitFor":"session/update","where":{"params.update.sessionUpdate":"tool_call"},"count":1}
//	{"raw":"{not json"}
//	{"stderrHas":"failed to parse"}   {"stderrLacks":"failed to handle notification"}
//	{"sleep":"300ms"}
//	{"close":true,"expectExit":0}
//
// "{{name.path}}" inside a JSON string is replaced with the string at that
// path of the message saved as name. Agent requests with no rule get a
// -32601 error.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type step struct {
	Label       string                     `json:"step"`
	Send        string                     `json:"send"`
	Notify      string                     `json:"notify"`
	Params      json.RawMessage            `json:"params"`
	Save        string                     `json:"save"`
	Async       bool                       `json:"async"`
	Await       string                     `json:"await"`
	Expect      map[string]json.RawMessage `json:"expect"`
	Respond     string                     `json:"respond"`
	Result      json.RawMessage            `json:"result"`
	Error       json.RawMessage            `json:"error"`
	WaitFor     string                     `json:"waitFor"`
	Where       map[string]json.RawMessage `json:"where"`
	Count       int                        `json:"count"`
	Raw         *string                    `json:"raw"`
	StderrHas   string                     `json:"stderrHas"`
	StderrLacks string                     `json:"stderrLacks"`
	Sleep       string                     `json:"sleep"`
	Timeout     string                     `json:"timeout"`
	Close       bool                       `json:"close"`
	ExpectExit  *int                       `json:"expectExit"`
}

// actionFields lists, for each step action, the other fields that the
// action reads. A step has exactly one action and no other fields.
var actionFields = map[string][]string{
	"send":        {"params", "save", "async", "expect", "timeout"},
	"notify":      {"params"},
	"await":       {"expect", "timeout"},
	"respond":     {"result", "error"},
	"waitFor":     {"where", "count", "timeout"},
	"raw":         nil,
	"stderrHas":   {"timeout"},
	"stderrLacks": nil,
	"sleep":       nil,
	"close":       {"expectExit", "timeout"},
}

// checkStepShape rejects a step whose fields do not fit exactly one action,
// so a mistyped journey line fails instead of silently skipping work.
func checkStepShape(fields map[string]json.RawMessage) error {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	action := ""
	for _, k := range keys {
		if _, ok := actionFields[k]; ok {
			if action != "" {
				return fmt.Errorf("step has two actions, %q and %q", action, k)
			}
			action = k
		}
	}
	if action == "" {
		return errors.New("step has no action")
	}
	allowed := map[string]bool{action: true, "step": true}
	for _, f := range actionFields[action] {
		allowed[f] = true
	}
	for _, k := range keys {
		if !allowed[k] {
			return fmt.Errorf("field %q does not apply to a %q step", k, action)
		}
	}
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	if action == "respond" && hasResult == hasError {
		return errors.New("a respond step needs exactly one of result and error")
	}
	if action == "close" && string(fields["close"]) != "true" {
		return errors.New(`a close step needs "close":true`)
	}
	return nil
}

// validEnvelope reports whether msg is a JSON-RPC 2.0 request,
// notification or response.
func validEnvelope(msg map[string]any) bool {
	if msg["jsonrpc"] != "2.0" {
		return false
	}
	id, hasID := msg["id"]
	switch id.(type) {
	case string, float64, nil:
	default:
		return false
	}
	_, hasResult := msg["result"]
	errObj, hasError := msg["error"]
	if p, ok := msg["params"]; ok {
		switch p.(type) {
		case map[string]any, []any:
		default:
			return false
		}
	}
	if m, ok := msg["method"]; ok {
		method, isString := m.(string)
		return isString && method != "" && !hasResult && !hasError
	}
	if !hasID || hasResult == hasError {
		return false
	}
	if hasError {
		e, ok := errObj.(map[string]any)
		if !ok {
			return false
		}
		_, codeOK := e["code"].(float64)
		_, msgOK := e["message"].(string)
		return codeOK && msgOK
	}
	return true
}

type stepResult struct {
	Line   int    `json:"line"`
	Step   string `json:"step"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

type call struct {
	method string
	sentAt int // notifications seen when the request was sent
	done   chan struct{}
	msg    map[string]any
	before int // notifications seen between send and response
}

type driver struct {
	start  time.Time
	stdin  io.WriteCloser
	trans  *os.File
	stderr *os.File

	mu       sync.Mutex
	wmu      sync.Mutex // orders transcript records with the writes they describe
	nextID   int
	calls    map[string]*call // by request id
	saved    map[string]*call // by save name
	notes    []map[string]any
	rules    map[string]step
	stderrSB strings.Builder
	exited   chan struct{}
	badOut   []string // agent stdout that is not a JSON-RPC message, or a read error
	errRead  string   // why the agent stderr reader stopped early
	exitCode int
	changed  chan struct{} // closed and replaced whenever state changes
}

func main() {
	agent := flag.String("agent", "", "agent binary to start")
	journey := flag.String("journey", "", "journey file (JSON lines)")
	out := flag.String("out", "", "directory for transcript.jsonl, agent.stderr.log, summary.json")
	timeout := flag.Duration("timeout", 60*time.Second, "limit for the whole run")
	flag.Parse()
	if *agent == "" || *journey == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	steps, lines, err := readJourney(*journey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acpwire:", err)
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "acpwire:", err)
		os.Exit(2)
	}
	results, err := run(*agent, flag.Args(), steps, lines, *out, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acpwire:", err)
		os.Exit(2)
	}
	pass := true
	for _, r := range results {
		mark := "PASS"
		if !r.Pass {
			mark, pass = "FAIL", false
		}
		fmt.Printf("%s  line %d  %s", mark, r.Line, r.Step)
		if r.Detail != "" {
			fmt.Printf("  (%s)", r.Detail)
		}
		fmt.Println()
	}
	if !pass {
		fmt.Println("RESULT fail")
		os.Exit(1)
	}
	fmt.Println("RESULT pass")
}

func readJourney(path string) ([]step, []int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	var steps []step
	var lines []int
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		var fields map[string]json.RawMessage
		fd := json.NewDecoder(strings.NewReader(text))
		if err := fd.Decode(&fields); err != nil {
			return nil, nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		var extra json.RawMessage
		if err := fd.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, nil, fmt.Errorf("%s:%d: content after the step object", path, n)
		}
		if err := checkStepShape(fields); err != nil {
			return nil, nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		var s step
		dec := json.NewDecoder(strings.NewReader(text))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return nil, nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		if s.Async && s.Expect != nil {
			return nil, nil, fmt.Errorf("%s:%d: an async send cannot expect; put expect on its await step", path, n)
		}
		if s.Count < 0 {
			return nil, nil, fmt.Errorf("%s:%d: count must not be negative", path, n)
		}
		for _, dur := range []string{s.Sleep, s.Timeout} {
			if _, err := time.ParseDuration(dur); dur != "" && err != nil {
				return nil, nil, fmt.Errorf("%s:%d: %w", path, n, err)
			}
		}
		steps = append(steps, s)
		lines = append(lines, n)
	}
	return steps, lines, sc.Err()
}

func run(agent string, args []string, steps []step, lines []int, out string, limit time.Duration) ([]stepResult, error) {
	trans, err := os.Create(filepath.Join(out, "transcript.jsonl"))
	if err != nil {
		return nil, err
	}
	defer trans.Close()
	stderrLog, err := os.Create(filepath.Join(out, "agent.stderr.log"))
	if err != nil {
		return nil, err
	}
	defer stderrLog.Close()

	cmd := exec.Command(agent, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	agentErr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	d := &driver{
		start: time.Now(), stdin: stdin, trans: trans, stderr: stderrLog,
		calls: map[string]*call{}, saved: map[string]*call{}, rules: map[string]step{},
		exited: make(chan struct{}), changed: make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "acpwire: agent pid %d\n", cmd.Process.Pid)

	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); d.readStdout(stdout) }()
	go func() { defer readers.Done(); d.readStderr(agentErr) }()
	go func() {
		readers.Wait()
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		d.mu.Lock()
		d.exitCode = code
		d.mu.Unlock()
		close(d.exited)
		d.notify()
	}()

	deadline := time.Now().Add(limit)
	var results []stepResult
	for i, s := range steps {
		label, detail, err := d.do(s, deadline)
		results = append(results, stepResult{Line: lines[i], Step: label, Pass: err == nil, Detail: detailOr(detail, err)})
		if err != nil {
			break
		}
	}

	// Leave no agent behind: close stdin, then kill after a grace period.
	_ = d.stdin.Close()
	select {
	case <-d.exited:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-d.exited
		results = append(results, stepResult{Step: "agent exit", Pass: false, Detail: "killed: agent did not exit 5s after stdin closed"})
	}
	d.mu.Lock()
	if len(d.badOut) > 0 {
		results = append(results, stepResult{Step: "agent stdout is JSON-RPC", Pass: false,
			Detail: fmt.Sprintf("%d bad line(s), first: %q", len(d.badOut), d.badOut[0])})
	}
	if d.errRead != "" {
		results = append(results, stepResult{Step: "agent stderr read", Pass: false, Detail: d.errRead})
	}
	d.mu.Unlock()

	summary := map[string]any{"agent": agent, "args": args, "results": results, "exitCode": d.exitCode, "notifications": len(d.notes)}
	b, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "summary.json"), append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	return results, nil
}

func detailOr(detail string, err error) string {
	if err != nil {
		return err.Error()
	}
	return detail
}

func (d *driver) notify() {
	d.mu.Lock()
	close(d.changed)
	d.changed = make(chan struct{})
	d.mu.Unlock()
}

// waitUntil polls cond (under d.mu) each time the driver state changes.
func (d *driver) waitUntil(deadline time.Time, what string, cond func() bool) error {
	for {
		d.mu.Lock()
		ok := cond()
		ch := d.changed
		d.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ch:
		case <-d.exited:
			d.mu.Lock()
			ok := cond()
			code := d.exitCode
			d.mu.Unlock()
			if ok {
				return nil
			}
			return fmt.Errorf("agent exited (code %d) before %s", code, what)
		case <-time.After(time.Until(deadline)):
			return fmt.Errorf("timed out waiting for %s", what)
		}
	}
}

func (d *driver) record(dir, line string) {
	entry := map[string]any{"t_ms": time.Since(d.start).Milliseconds(), "dir": dir}
	if json.Valid([]byte(line)) {
		entry["msg"] = json.RawMessage(line)
	} else {
		entry["raw"] = line
	}
	b, _ := json.Marshal(entry)
	d.mu.Lock()
	_, _ = d.trans.Write(append(b, '\n'))
	d.mu.Unlock()
}

func (d *driver) write(line string) error {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	d.record("client->agent", line)
	_, err := io.WriteString(d.stdin, line+"\n")
	return err
}

func (d *driver) readStdout(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 10<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		d.record("agent->client", line)
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil || !validEnvelope(msg) {
			d.mu.Lock()
			d.badOut = append(d.badOut, line)
			d.mu.Unlock()
			d.notify()
			continue
		}
		id, hasID := msg["id"]
		method, _ := msg["method"].(string)
		switch {
		case hasID && method != "":
			d.answer(id, method)
		case method != "":
			d.mu.Lock()
			d.notes = append(d.notes, msg)
			d.mu.Unlock()
		case hasID:
			key := string(mustJSON(id))
			d.mu.Lock()
			if c := d.calls[key]; c != nil && c.msg == nil {
				c.msg = msg
				c.before = len(d.notes) - c.sentAt
				close(c.done)
			}
			d.mu.Unlock()
		}
		d.notify()
	}
	if err := sc.Err(); err != nil {
		d.mu.Lock()
		d.badOut = append(d.badOut, "read error: "+err.Error())
		d.mu.Unlock()
		d.notify()
	}
}

func (d *driver) readStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 10<<20)
	for sc.Scan() {
		line := sc.Text()
		d.mu.Lock()
		fmt.Fprintf(d.stderr, "%d %s\n", time.Since(d.start).Milliseconds(), line)
		d.stderrSB.WriteString(line + "\n")
		d.mu.Unlock()
		d.notify()
	}
	if err := sc.Err(); err != nil {
		d.mu.Lock()
		d.errRead = err.Error()
		d.mu.Unlock()
		d.notify()
	}
}

// answer replies to a request from the agent with the matching rule.
func (d *driver) answer(id any, method string) {
	d.mu.Lock()
	rule, ok := d.rules[method]
	d.mu.Unlock()
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	switch {
	case !ok:
		resp["error"] = map[string]any{"code": -32601, "message": "Method not found"}
	case rule.Error != nil:
		resp["error"] = rule.Error
	default:
		resp["result"] = rule.Result
	}
	_ = d.write(string(mustJSON(resp)))
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

var tmplRE = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\.([A-Za-z0-9_.]+)\}\}`)

func (d *driver) expand(raw json.RawMessage) (json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}
	var firstErr error
	out := tmplRE.ReplaceAllFunc(raw, func(m []byte) []byte {
		parts := tmplRE.FindSubmatch(m)
		d.mu.Lock()
		c := d.saved[string(parts[1])]
		d.mu.Unlock()
		if c == nil || c.msg == nil {
			firstErr = fmt.Errorf("no saved response %q", parts[1])
			return m
		}
		v, ok := lookup(c.msg, string(parts[2]))
		s, isString := v.(string)
		if !ok || !isString {
			firstErr = fmt.Errorf("%s.%s is not a string", parts[1], parts[2])
			return m
		}
		quoted := mustJSON(s)
		return quoted[1 : len(quoted)-1]
	})
	if firstErr != nil {
		return nil, firstErr
	}
	if !json.Valid(out) {
		return nil, fmt.Errorf("params are not valid JSON after expansion")
	}
	return out, nil
}

func lookup(v any, path string) (any, bool) {
	for _, key := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return v, true
}

func matches(msg map[string]any, want map[string]json.RawMessage) error {
	for path, raw := range want {
		var w any
		if err := json.Unmarshal(raw, &w); err != nil {
			return fmt.Errorf("bad expectation for %s: %w", path, err)
		}
		got, ok := lookup(msg, path)
		if !ok {
			return fmt.Errorf("%s missing", path)
		}
		if !bytes.Equal(mustJSON(got), mustJSON(w)) {
			return fmt.Errorf("%s = %s, want %s", path, mustJSON(got), mustJSON(w))
		}
	}
	return nil
}

func stepTimeout(s step, deadline time.Time) time.Time {
	limit := 30 * time.Second
	if s.Timeout != "" {
		if d, err := time.ParseDuration(s.Timeout); err == nil {
			limit = d
		}
	}
	if t := time.Now().Add(limit); t.Before(deadline) {
		return t
	}
	return deadline
}

// do runs one step and returns its label and a detail for the summary.
func (d *driver) do(s step, runDeadline time.Time) (string, string, error) {
	deadline := stepTimeout(s, runDeadline)
	label := func(def string) string {
		if s.Label != "" {
			return s.Label
		}
		return def
	}
	switch {
	case s.Send != "":
		params, err := d.expand(s.Params)
		if err != nil {
			return label("send " + s.Send), "", err
		}
		d.mu.Lock()
		d.nextID++
		id := d.nextID
		c := &call{method: s.Send, sentAt: len(d.notes), done: make(chan struct{})}
		d.calls[fmt.Sprint(id)] = c
		if s.Save != "" {
			d.saved[s.Save] = c
		}
		d.mu.Unlock()
		req := map[string]any{"jsonrpc": "2.0", "id": id, "method": s.Send}
		if params != nil {
			req["params"] = params
		}
		if err := d.write(string(mustJSON(req))); err != nil {
			return label("send " + s.Send), "", err
		}
		if s.Async {
			return label("send " + s.Send + " (async)"), fmt.Sprintf("id %d", id), nil
		}
		return d.finish(label("send "+s.Send), c, s.Expect, deadline)
	case s.Await != "":
		d.mu.Lock()
		c := d.saved[s.Await]
		d.mu.Unlock()
		if c == nil {
			return label("await " + s.Await), "", fmt.Errorf("no request saved as %q", s.Await)
		}
		return d.finish(label("await "+s.Await), c, s.Expect, deadline)
	case s.Notify != "":
		params, err := d.expand(s.Params)
		if err != nil {
			return label("notify " + s.Notify), "", err
		}
		n := map[string]any{"jsonrpc": "2.0", "method": s.Notify}
		if params != nil {
			n["params"] = params
		}
		return label("notify " + s.Notify), "", d.write(string(mustJSON(n)))
	case s.Respond != "":
		d.mu.Lock()
		d.rules[s.Respond] = s
		d.mu.Unlock()
		return label("rule: answer " + s.Respond), "", nil
	case s.WaitFor != "":
		count := s.Count
		if count == 0 {
			count = 1
		}
		what := fmt.Sprintf("%d %s notification(s) matching %v", count, s.WaitFor, s.Where)
		err := d.waitUntil(deadline, what, func() bool {
			n := 0
			for _, m := range d.notes {
				if m["method"] == s.WaitFor && matches(m, s.Where) == nil {
					n++
				}
			}
			return n >= count
		})
		return label("wait for " + s.WaitFor), "", err
	case s.Raw != nil:
		return label("raw line"), "", d.write(*s.Raw)
	case s.StderrHas != "":
		err := d.waitUntil(deadline, fmt.Sprintf("agent stderr to contain %q", s.StderrHas), func() bool {
			return strings.Contains(d.stderrSB.String(), s.StderrHas)
		})
		return label("stderr has " + s.StderrHas), "", err
	case s.StderrLacks != "":
		d.mu.Lock()
		found := strings.Contains(d.stderrSB.String(), s.StderrLacks)
		d.mu.Unlock()
		if found {
			return label("stderr lacks " + s.StderrLacks), "", fmt.Errorf("agent stderr contains %q", s.StderrLacks)
		}
		return label("stderr lacks " + s.StderrLacks), "", nil
	case s.Sleep != "":
		dur, err := time.ParseDuration(s.Sleep)
		if err != nil {
			return label("sleep"), "", err
		}
		if left := time.Until(runDeadline); dur > left {
			time.Sleep(max(left, 0))
			return label("sleep " + s.Sleep), "", errors.New("the sleep runs past the -timeout deadline")
		}
		time.Sleep(dur)
		return label("sleep " + s.Sleep), "", nil
	case s.Close:
		d.wmu.Lock()
		_ = d.stdin.Close()
		d.record("client->agent", "<EOF>")
		d.wmu.Unlock()
		select {
		case <-d.exited:
		case <-time.After(time.Until(deadline)):
			return label("close stdin"), "", errors.New("agent did not exit after stdin closed")
		}
		d.mu.Lock()
		code := d.exitCode
		d.mu.Unlock()
		if s.ExpectExit != nil && code != *s.ExpectExit {
			return label("close stdin"), "", fmt.Errorf("agent exit code %d, want %d", code, *s.ExpectExit)
		}
		return label("close stdin"), fmt.Sprintf("agent exit code %d", code), nil
	}
	return "unknown step", "", errors.New("step has no action")
}

func (d *driver) finish(label string, c *call, expect map[string]json.RawMessage, deadline time.Time) (string, string, error) {
	select {
	case <-c.done:
	case <-d.exited:
		select {
		case <-c.done:
		default:
			return label, "", errors.New("agent exited before the response")
		}
	case <-time.After(time.Until(deadline)):
		return label, "", errors.New("timed out waiting for the response")
	}
	d.mu.Lock()
	msg, before := c.msg, c.before
	d.mu.Unlock()
	detail := fmt.Sprintf("%d notifications before the response", before)
	if r, ok := msg["result"].(map[string]any); ok {
		if sr, ok := r["stopReason"].(string); ok {
			detail = "stopReason " + sr + ", " + detail
		}
	}
	if e, ok := msg["error"].(map[string]any); ok {
		detail = fmt.Sprintf("error %v %v, %s", e["code"], e["message"], detail)
		if !expectsError(expect) {
			return label, detail, errors.New("unexpected error response")
		}
	}
	return label, detail, matches(msg, expect)
}

// expectsError reports whether the journey asks for an error response.
func expectsError(expect map[string]json.RawMessage) bool {
	for path := range expect {
		if path == "error" || strings.HasPrefix(path, "error.") {
			return true
		}
	}
	return false
}
