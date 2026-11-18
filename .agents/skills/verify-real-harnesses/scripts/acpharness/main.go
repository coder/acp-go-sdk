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
// failed. Exit 1: a scenario failed, the budget stopped the run, or the
// evidence folder could not be set up. Exit 64 (EX_USAGE): usage error,
// before any agent starts. Any other exit means that the driver died, such
// as 2 for a Go panic, so the run happened and its evidence counts.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
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

// The documented reasons for a SKIP. Each SKIP detail starts with one, and
// SKILL.md lists them all. A SKIP for any other reason, such as a scenario
// blocked by an earlier failure or by the budget, fails the known-skips row.
const (
	skipNotSelected     = "not selected by -only"
	skipNoAuth          = "no -auth"
	skipNotAdvertised   = "not advertised"
	skipNoModelOption   = "no model option"
	skipNoOtherModel    = "no other model"
	skipNoBooleanOption = "no boolean option"
)

var knownSkips = []string{skipNotSelected, skipNoAuth, skipNotAdvertised, skipNoModelOption, skipNoOtherModel, skipNoBooleanOption}

// undocumentedSkips returns each SKIP whose detail does not start with a
// documented reason, as "name (detail)".
func undocumentedSkips(results []result) []string {
	var out []string
next:
	for _, r := range results {
		if r.Status != skip {
			continue
		}
		for _, reason := range knownSkips {
			if r.Detail == reason || strings.HasPrefix(r.Detail, reason+": ") {
				continue next
			}
		}
		out = append(out, fmt.Sprintf("%s (%s)", r.Name, clip(r.Detail, 120)))
	}
	return out
}

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
	sdkFile   *os.File
	conns     []*agentConn
	results   []result
	budgetHit bool

	// What the run learned along the way. Sessions go by the names that
	// scenarios.go gives them, "A" and "B".
	init          acp.InitializeResponse
	models        map[string]bool
	defaultModels []string // models of the first plain turn
	sessions      map[string]acp.SessionId
	cwds          map[string]string
	configs       map[string][]acp.SessionConfigOption
	newResults    map[string]json.RawMessage // raw session/new results, the wire truth for configs
	modes         map[string]*acp.SessionModeState
}

// exitUsage is the exit code of a usage error. g5 reads it as "no run
// happened". It must differ from 2, which a Go panic exits with.
const exitUsage = 64

func main() {
	var o options
	var only string
	fs := flag.NewFlagSet("acpharness", flag.ContinueOnError)
	fs.StringVar(&o.out, "out", "", "evidence folder (created)")
	fs.StringVar(&o.cwd, "cwd", "", "folder for the sessions' working directories (created)")
	fs.StringVar(&o.profile, "profile", "live", "live or smoke")
	fs.StringVar(&o.askMode, "ask-mode", "", "mode that makes the agent ask before it edits a file")
	fs.StringVar(&o.auth, "auth", "", "authenticate with this method id after initialize")
	fs.Float64Var(&o.budget, "budget-usd", 25, "stop before the next scenario once the reported cost reaches this")
	fs.DurationVar(&o.turnTimeout, "turn-timeout", 5*time.Minute, "longest prompt turn")
	fs.StringVar(&only, "only", "", "comma-separated scenarios to run; setup steps always run")
	fs.StringVar(&o.pgids, "pgids", "", "append each agent's process group and start time to this file")
	if err := fs.Parse(os.Args[1:]); errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	} else if err != nil {
		os.Exit(exitUsage)
	}
	o.agent = fs.Args()
	if o.out == "" || o.cwd == "" || len(o.agent) == 0 || (o.profile != "live" && o.profile != "smoke") {
		fs.Usage()
		os.Exit(exitUsage)
	}
	if only != "" {
		o.only = map[string]bool{}
		for _, s := range strings.Split(only, ",") {
			s = strings.TrimSpace(s)
			if !contains(profiles[o.profile], s) {
				fmt.Fprintf(os.Stderr, "acpharness: -only names %q, which is not a %s scenario: %s\n", s, o.profile, strings.Join(profiles[o.profile], ", "))
				os.Exit(exitUsage)
			}
			o.only[s] = true
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
			os.Exit(1)
		}
	}
	h, err := newHarness(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acpharness:", err)
		os.Exit(1)
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
		opts:       o,
		rec:        rec,
		sdkLog:     slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}),
		sdkFile:    logFile,
		models:     map[string]bool{},
		sessions:   map[string]acp.SessionId{},
		cwds:       map[string]string{},
		configs:    map[string][]acp.SessionConfigOption{},
		newResults: map[string]json.RawMessage{},
		modes:      map[string]*acp.SessionModeState{},
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
		st, detail = skip, skipNotSelected
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

// cost is the USD cost that the agents reported on the wire. The budget
// reads it from the raw usage_update lines, so a cost that the SDK fails to
// decode still counts; finish reports such a gap as a decode error.
func (h *harness) cost() float64 { return h.rec.cost() }

func (h *harness) trackPgid(pid int) error {
	if h.opts.pgids == "" {
		return nil
	}
	// The start time lets cleanup tell this process from a later one that
	// reuses the pid.
	start, err := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return fmt.Errorf("read the start time of pid %d: %w", pid, err)
	}
	f, err := os.OpenFile(h.opts.pgids, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%d\t%s\t%s\n", pid, strings.Join(strings.Fields(string(start)), " "), strings.Join(h.opts.agent, " "))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
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
	// The cost check gets its own scenario label, so that its finding does
	// not name the last scenario, agent-exit.
	h.rec.scenario.Store("cost")
	decoded := 0.0
	for _, a := range h.conns {
		decoded += a.client.cost()
	}
	if wire := h.rec.cost(); math.Abs(wire-decoded) > 1e-9 {
		h.rec.finding(decodeError, fmt.Sprintf("the usage_update costs on the wire add up to $%.4f, but the SDK decoded $%.4f", wire, decoded))
	}
	findings, inbound := h.rec.snapshot()
	h.globalStep("no-method-not-found", methodNotFound, findings)
	h.globalStep("no-decode-errors", decodeError, findings)
	h.globalStep("no-agent-errors", agentError, findings)
	var refused []string
	for _, a := range h.conns {
		refused = append(refused, a.client.refusedCalls()...)
	}
	if len(refused) == 0 {
		h.row("no-refused-calls", pass, "the agent called no file system or terminal method, which the client does not offer")
	} else {
		h.row("no-refused-calls", fail, fmt.Sprintf("the agent called %d method(s) that the client does not offer: %s",
			len(refused), clip(strings.Join(refused, ", "), 240)))
	}
	if odd := undocumentedSkips(h.results); len(odd) == 0 {
		h.row("known-skips", pass, "every SKIP has a reason that SKILL.md lists")
	} else {
		h.row("known-skips", fail, fmt.Sprintf("%d SKIP(s) for a reason that SKILL.md does not list: %s",
			len(odd), strings.Join(odd, "; ")))
	}

	var perms []permission
	var elicitations []elicitation
	// A session reopened on a later connection reports the same cumulative
	// usage again, so usage merges by session before it adds up.
	usage := map[acp.SessionId]acp.Usage{}
	other := map[string]float64{}
	for _, a := range h.conns {
		perms = append(perms, a.client.permissionsSince(0)...)
		elicitations = append(elicitations, a.client.elicitationsSince(0)...)
		u, o := a.client.totals()
		for sid, t := range u {
			m := usage[sid]
			m.InputTokens = max(m.InputTokens, t.InputTokens)
			m.OutputTokens = max(m.OutputTokens, t.OutputTokens)
			m.TotalTokens = max(m.TotalTokens, t.TotalTokens)
			usage[sid] = m
		}
		for k, v := range o {
			other[k] += v
		}
	}
	tokens := map[string]int{}
	for _, t := range usage {
		tokens["input"] += t.InputTokens
		tokens["output"] += t.OutputTokens
		tokens["total"] += t.TotalTokens
	}
	models := make([]string, 0, len(h.models))
	for m := range h.models {
		models = append(models, m)
	}
	sort.Strings(models)
	// Missing proof fails the run: a gate must not pass without its
	// evidence. A file that cannot be written below fails only the exit code.
	var lost []string
	if err := h.sdkFile.Close(); err != nil {
		h.rec.lost(fmt.Errorf("sdk.log: %w", err))
	}
	if err := h.rec.close(); err != nil {
		lost = append(lost, err.Error())
	}
	failed := h.budgetHit || len(lost) > 0
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
		"tokens":          tokens,
		"otherCosts":      other,
		"evidenceErrors":  lost,
		"permissions":     perms,
		"elicitations":    elicitations,
		"refusedCalls":    refused,
		"exits":           exits,
		"pass":            !failed,
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(filepath.Join(h.opts.out, "summary.json"), append(b, '\n'), 0o600); err != nil {
		lost = append(lost, err.Error())
	}

	var lines strings.Builder
	for _, r := range h.results {
		fmt.Fprintf(&lines, "%s  %s  %s\n", r.Status, r.Name, r.Detail)
	}
	for _, l := range lost {
		fmt.Fprintf(&lines, "FAIL  evidence  %s\n", l)
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
	if kind == agentError {
		detail = "the agent sent no JSON-RPC error response"
	}
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
