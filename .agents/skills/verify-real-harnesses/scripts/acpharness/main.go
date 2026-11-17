// Command acpharness is an ACP client built on this checkout's SDK. It
// starts a real agent over stdio, runs a fixed list of scenarios against
// it, and records the proof: the JSON-RPC transcript, the SDK's slog
// output, the agent's stderr and one PASS, FAIL or SKIP line per scenario.
//
// Usage:
//
//	acpharness -out DIR -cwd DIR [-profile live|smoke] [-ask-mode MODE]
//	    [-auth METHOD] [-budget-usd 25] [-turn-timeout 5m] [-only a,b]
//	    [-pgids FILE] -- AGENT [ARGS...]
//
// The live profile runs every scenario in scenarios.go. The smoke profile
// runs initialize, session/new and one prompt turn, for agents without a
// model, such as the TypeScript SDK's example agent. Exit 0: no scenario
// failed. Exit 1: a scenario failed or the budget stopped the run.
// Exit 2: usage error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

type status string

const (
	pass status = "PASS"
	fail status = "FAIL"
	skip status = "SKIP"
)

type result struct {
	Name   string `json:"name"`
	Status status `json:"status"`
	Detail string `json:"detail"`
	Ms     int64  `json:"ms"`
}

type options struct {
	out, cwd, profile string
	askMode, auth     string
	budget            float64
	turnTimeout       time.Duration
	only              map[string]bool
	pgids             string
	agent             []string
}

type harness struct {
	opts      options
	rec       *recorder
	sdkLog    slog.Handler
	conns     []*agentConn
	results   []result
	budgetHit bool

	// What the run learned along the way. Sessions go by the names that
	// scenarios.go gives them, "A" and "B".
	init          acp.InitializeResponse
	models        map[string]bool
	defaultModels []string // models of the first plain turn
	tokens        map[string]int
	sessions      map[string]acp.SessionId
	cwds          map[string]string
	configs       map[string][]acp.SessionConfigOption
	modes         map[string]*acp.SessionModeState
}

func main() {
	var o options
	var only string
	flag.StringVar(&o.out, "out", "", "evidence folder (created)")
	flag.StringVar(&o.cwd, "cwd", "", "folder for the sessions' working directories (created)")
	flag.StringVar(&o.profile, "profile", "live", "live or smoke")
	flag.StringVar(&o.askMode, "ask-mode", "", "mode that makes the agent ask before it edits a file")
	flag.StringVar(&o.auth, "auth", "", "authenticate with this method id after initialize")
	flag.Float64Var(&o.budget, "budget-usd", 25, "stop before the next scenario once the reported cost reaches this")
	flag.DurationVar(&o.turnTimeout, "turn-timeout", 5*time.Minute, "longest prompt turn")
	flag.StringVar(&only, "only", "", "comma-separated scenarios to run; setup steps always run")
	flag.StringVar(&o.pgids, "pgids", "", "append each agent's process group and start time to this file")
	flag.Parse()
	o.agent = flag.Args()
	if o.out == "" || o.cwd == "" || len(o.agent) == 0 || (o.profile != "live" && o.profile != "smoke") {
		flag.Usage()
		os.Exit(2)
	}
	if only != "" {
		o.only = map[string]bool{}
		for _, s := range strings.Split(only, ",") {
			o.only[strings.TrimSpace(s)] = true
		}
	}
	for _, d := range []*string{&o.out, &o.cwd} {
		// Resolve symlinks: codex-acp reports each session's cwd as a real
		// path and compares the session/list cwd filter to it as a string.
		abs, err := filepath.Abs(*d)
		if err == nil {
			err = os.MkdirAll(abs, 0o700)
		}
		if err == nil {
			*d, err = filepath.EvalSymlinks(abs)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "acpharness:", err)
			os.Exit(2)
		}
	}
	h, err := newHarness(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acpharness:", err)
		os.Exit(2)
	}
	if o.profile == "smoke" {
		h.runSmoke()
	} else {
		h.runLive()
	}
	os.Exit(h.finish())
}

func newHarness(o options) (*harness, error) {
	rec, err := newRecorder(o.out)
	if err != nil {
		return nil, err
	}
	logFile, err := os.Create(filepath.Join(o.out, "sdk.log"))
	if err != nil {
		return nil, err
	}
	return &harness{
		opts:     o,
		rec:      rec,
		sdkLog:   slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}),
		models:   map[string]bool{},
		tokens:   map[string]int{},
		sessions: map[string]acp.SessionId{},
		cwds:     map[string]string{},
		configs:  map[string][]acp.SessionConfigOption{},
		modes:    map[string]*acp.SessionModeState{},
	}, nil
}

// setupSteps run even when -only leaves them out, because every other
// scenario needs them.
var setupSteps = map[string]bool{"initialize": true, "authenticate": true, "session/new": true}

// step runs one scenario, records its result and prints its line.
func (h *harness) step(name string, fn func() (status, string)) status {
	h.rec.scenario.Store(name)
	start := time.Now()
	var st status
	var detail string
	switch {
	case h.opts.only != nil && !h.opts.only[name] && !setupSteps[name]:
		st, detail = skip, "not selected by -only"
	case h.budgetHit || h.cost() >= h.opts.budget:
		h.budgetHit = true
		st, detail = skip, fmt.Sprintf("budget: reported cost $%.2f reached -budget-usd %.2f", h.cost(), h.opts.budget)
	default:
		st, detail = fn()
	}
	h.results = append(h.results, result{Name: name, Status: st, Detail: detail, Ms: time.Since(start).Milliseconds()})
	fmt.Printf("%-4s  %-18s %s\n", st, name, detail)
	return st
}

func (h *harness) cost() float64 {
	total := 0.0
	for _, a := range h.conns {
		total += a.client.cost()
	}
	return total
}

func (h *harness) trackPgid(pid int) {
	if h.opts.pgids == "" {
		return
	}
	// The start time lets cleanup tell this process from a later one that
	// reuses the pid.
	start, _ := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid)).Output()
	f, err := os.OpenFile(h.opts.pgids, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%d\t%s\t%s\n", pid, strings.Join(strings.Fields(string(start)), " "), strings.Join(h.opts.agent, " "))
}

// finish closes every agent, adds the global predicate, and writes
// summary.json and result.txt. It returns the exit code.
func (h *harness) finish() int {
	h.rec.scenario.Store("agent-exit")
	var exits, bad []string
	for _, a := range h.conns {
		exit := fmt.Sprintf("conn %d: %s", a.n, a.close(10*time.Second))
		exits = append(exits, exit)
		if a.ended != "exit 0" {
			bad = append(bad, exit)
		}
	}
	if len(bad) == 0 {
		h.row("agent-exit", pass, fmt.Sprintf("%d agent process(es) exited 0 after their input closed", len(exits)))
	} else {
		h.row("agent-exit", fail, strings.Join(bad, "; "))
	}
	findings, inbound := h.rec.snapshot()
	h.globalStep("no-method-not-found", methodNotFound, findings)
	h.globalStep("no-decode-errors", decodeError, findings)

	var perms []permission
	var refused []string
	for _, a := range h.conns {
		perms = append(perms, a.client.permissionsSince(0)...)
		refused = append(refused, a.client.misc...)
	}
	models := make([]string, 0, len(h.models))
	for m := range h.models {
		models = append(models, m)
	}
	sort.Strings(models)
	failed := h.budgetHit
	for _, r := range h.results {
		failed = failed || r.Status == fail
	}
	var info any
	if h.init.AgentInfo != nil {
		info = h.init.AgentInfo
	}
	summary := map[string]any{
		"agent":           h.opts.agent,
		"profile":         h.opts.profile,
		"agentInfo":       info,
		"protocolVersion": h.init.ProtocolVersion,
		"results":         h.results,
		"findings":        findings,
		"inboundMethods":  inbound,
		"models":          models,
		"costUsd":         h.cost(),
		"budgetUsd":       h.opts.budget,
		"budgetHit":       h.budgetHit,
		"tokens":          h.tokens,
		"permissions":     perms,
		"refusedCalls":    refused,
		"exits":           exits,
		"pass":            !failed,
	}
	// Missing proof fails the run: a gate must not pass without its evidence.
	var lost []string
	if err := h.rec.close(); err != nil {
		lost = append(lost, err.Error())
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(filepath.Join(h.opts.out, "summary.json"), append(b, '\n'), 0o600); err != nil {
		lost = append(lost, err.Error())
	}

	var lines strings.Builder
	for _, r := range h.results {
		fmt.Fprintf(&lines, "%s  %s  %s\n", r.Status, r.Name, r.Detail)
	}
	fmt.Fprintf(&lines, "models=%s cost_usd=%.4f budget_hit=%v\n", strings.Join(models, ","), h.cost(), h.budgetHit)
	fmt.Printf("models=%s cost_usd=%.4f budget_hit=%v\n", strings.Join(models, ","), h.cost(), h.budgetHit)
	code := 0
	if failed {
		code = 1
	}
	fmt.Fprintf(&lines, "exit=%d\n", code)
	if err := os.WriteFile(filepath.Join(h.opts.out, "result.txt"), []byte(lines.String()), 0o600); err != nil {
		lost = append(lost, err.Error())
	}
	if len(lost) > 0 {
		fmt.Fprintln(os.Stderr, "acpharness: the evidence is incomplete:", strings.Join(lost, "; "))
		return 1
	}
	return code
}

// globalStep turns the findings of one kind into a scenario row.
func (h *harness) globalStep(name, kind string, findings []finding) {
	h.rec.scenario.Store(name)
	var hits []finding
	for _, f := range findings {
		if f.Kind == kind {
			hits = append(hits, f)
		}
	}
	st, detail := pass, "none in sdk.log or in the SDK's replies"
	if len(hits) > 0 {
		st = fail
		where := map[string]bool{}
		for _, f := range hits {
			where[f.Scenario] = true
		}
		scen := make([]string, 0, len(where))
		for s := range where {
			scen = append(scen, s)
		}
		sort.Strings(scen)
		detail = fmt.Sprintf("%d (in %s); first: %s", len(hits), strings.Join(scen, ","), clip(hits[0].Text, 240))
	}
	h.row(name, st, detail)
}

// row records a result that no scenario function produced and prints it.
func (h *harness) row(name string, st status, detail string) {
	h.results = append(h.results, result{Name: name, Status: st, Detail: detail})
	fmt.Printf("%-4s  %-18s %s\n", st, name, detail)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
