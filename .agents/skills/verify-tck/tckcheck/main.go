// Command tckcheck compares an acp-tck JSON report with the checked-in
// baseline.json and lists every requirement whose result drifted.
//
//	tckcheck -report report.json -baseline baseline.json [-update]
//
// The baseline is exactly what -update writes from a report, reasons and the
// flaky list aside. So a check passes when -update would leave the baseline
// as it is, and every result it accepts is one a person has explained:
//
//   - every requirement of the report has an entry, and every entry has a
//     requirement of the report;
//   - each entry's tier and status match the report (a flaky entry's status
//     may change);
//   - every entry that is not PASS, and every flaky entry, has a reason that is
//     not TODO;
//   - the report comes from the TCK release and spec revision the baseline
//     names, and its verdict is the baseline's;
//   - no requirement is NOT_TESTED, and the run was not forced NOT CONFORMANT
//     by authentication or version negotiation: those runs did not exercise
//     the agent, so no baseline accepts them.
//
// -update rewrites the baseline from the report, keeps the reason of each
// requirement still present, writes "TODO" for each one that needs a reason
// and has none, then checks the new baseline. Exit status: 0 no drift, 1
// drift, 2 bad usage or an unreadable file.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
)

// The statuses of a requirement in an acp-tck report.
const (
	statusPass      = "PASS"
	statusNotTested = "NOT_TESTED"
)

// todo is the reason -update writes for an entry a person must explain.
const todo = "TODO"

// report is the part of an acp-tck --report-json file that tckcheck reads.
type report struct {
	TCKVersion     string        `json:"tck_version"`
	SchemaRevision string        `json:"schema_revision"`
	Verdict        verdict       `json:"verdict"`
	Requirements   []requirement `json:"requirements"`
}

type verdict struct {
	Conformant               bool `json:"conformant"`
	BlockedByAuth            bool `json:"blocked_by_auth"`
	BlockedByVersionMismatch bool `json:"blocked_by_version_mismatch"`
}

type requirement struct {
	ID     string    `json:"id"`
	Tier   string    `json:"tier"`
	Status string    `json:"status"`
	Tests  []outcome `json:"tests"`
}

// outcome is one test bound to a requirement.
type outcome struct {
	NodeID  string `json:"nodeid"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// baseline is the checked-in expectation, keyed by requirement id.
type baseline struct {
	TCKVersion     string           `json:"tckVersion"`
	SchemaRevision string           `json:"schemaRevision"`
	Conformant     bool             `json:"conformant"`
	Flaky          []string         `json:"flaky,omitempty"`
	Requirements   map[string]entry `json:"requirements"`
}

type entry struct {
	Tier   string `json:"tier"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// drift is one difference between a baseline and a report. ID is empty for a
// difference of the whole run.
type drift struct {
	ID, Tier, Expected, Got, Detail string
}

func (d drift) String() string {
	if d.ID == "" {
		return fmt.Sprintf("run: expected %s, got %s", d.Expected, d.Got)
	}
	s := fmt.Sprintf("%s %s: expected %s, got %s", d.ID, d.Tier, d.Expected, d.Got)
	if d.Detail != "" {
		s += ": " + d.Detail
	}
	return s
}

// updated returns the baseline that report r gives, keeping the flaky list
// and the reasons of prev for the requirements r still has.
func updated(prev baseline, r report) baseline {
	present := make(map[string]bool, len(r.Requirements))
	for _, q := range r.Requirements {
		present[q.ID] = true
	}
	next := baseline{
		TCKVersion:     r.TCKVersion,
		SchemaRevision: r.SchemaRevision,
		Conformant:     r.Verdict.Conformant,
		Requirements:   make(map[string]entry, len(r.Requirements)),
	}
	for _, id := range prev.Flaky {
		if present[id] {
			next.Flaky = append(next.Flaky, id)
		}
	}
	for _, q := range r.Requirements {
		e := entry{Tier: q.Tier, Status: q.Status}
		if q.Status != statusPass || slices.Contains(next.Flaky, q.ID) {
			e.Reason = prev.Requirements[q.ID].Reason
			if e.Reason == "" {
				e.Reason = todo
			}
		}
		next.Requirements[q.ID] = e
	}
	return next
}

// check lists the drifts of report r from baseline b, in requirement id
// order, after the drifts of the whole run.
func check(b baseline, r report) []drift {
	var run []drift
	if b.TCKVersion != r.TCKVersion {
		run = append(run, drift{Expected: "tck " + b.TCKVersion, Got: r.TCKVersion})
	}
	if b.SchemaRevision != r.SchemaRevision {
		run = append(run, drift{Expected: "spec " + b.SchemaRevision, Got: r.SchemaRevision})
	}
	// A flaky requirement that changed status can flip the verdict; its own
	// status is exempt, so the verdict it flips is too.
	if b.Conformant != r.Verdict.Conformant && !flakyChanged(b, r) {
		run = append(run, drift{Expected: conformance(b.Conformant), Got: conformance(r.Verdict.Conformant)})
	}
	if r.Verdict.BlockedByAuth {
		run = append(run, drift{Expected: "a run that authenticated", Got: "blocked_by_auth"})
	}
	if r.Verdict.BlockedByVersionMismatch {
		run = append(run, drift{Expected: "a run that negotiated the protocol version", Got: "blocked_by_version_mismatch"})
	}

	want := updated(b, r)
	var reqs []drift
	for id, e := range b.Requirements {
		if _, ok := want.Requirements[id]; !ok {
			reqs = append(reqs, drift{ID: id, Tier: e.Tier, Expected: e.Status, Got: "no requirement"})
		}
	}
	for _, q := range r.Requirements {
		e, ok := b.Requirements[q.ID]
		d := drift{ID: q.ID, Tier: q.Tier}
		switch reason := want.Requirements[q.ID].Reason; {
		case q.Status == statusNotTested:
			d.Expected, d.Got = "a tested requirement", q.Status
		case !ok:
			d.Expected, d.Got, d.Detail = "no entry", q.Status, firstFailure(q)
		case e.Tier != q.Tier:
			d.Expected, d.Got = "tier "+e.Tier, q.Tier
		case e.Status != q.Status && !slices.Contains(b.Flaky, q.ID):
			d.Expected, d.Got, d.Detail = e.Status, q.Status, firstFailure(q)
		case reason != "" && e.Reason == "":
			d.Expected, d.Got = "a reason", "none"
		case reason == todo:
			d.Expected, d.Got = "a reason", todo
		case reason == "" && e.Reason != "":
			d.Expected, d.Got = "no reason", "a reason for a "+q.Status+" requirement"
		default:
			continue
		}
		reqs = append(reqs, d)
	}
	for _, id := range b.Flaky {
		if !slices.Contains(want.Flaky, id) {
			reqs = append(reqs, drift{ID: id, Tier: "flaky", Expected: "a flaky requirement", Got: "no requirement"})
		}
	}
	slices.SortFunc(reqs, func(x, y drift) int { return strings.Compare(x.ID, y.ID) })
	return append(run, reqs...)
}

// flakyChanged reports whether a requirement that b lists as flaky has
// another status in r than in b.
func flakyChanged(b baseline, r report) bool {
	for _, q := range r.Requirements {
		if slices.Contains(b.Flaky, q.ID) && b.Requirements[q.ID].Status != q.Status {
			return true
		}
	}
	return false
}

func conformance(ok bool) string {
	if ok {
		return "conformant"
	}
	return "not conformant"
}

// firstFailure returns why the first test of q that did not pass did not.
func firstFailure(q requirement) string {
	for _, t := range q.Tests {
		if t.Status != statusPass {
			return firstLine(t.Message)
		}
	}
	return ""
}

// firstLine returns the line of a test message that says why it did not
// pass: pytest's first "E" line for a failure, the reason of a skip, or else
// the message's first line.
func firstLine(msg string) string {
	lines := strings.Split(msg, "\n")
	line := strings.TrimSpace(lines[0])
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(l, "E "); ok {
			line = strings.TrimSpace(rest)
			break
		}
	}
	if i := strings.Index(line, "Skipped: "); i >= 0 {
		line = strings.TrimRight(line[i:], `"')`)
	}
	const limit = 200
	if r := []rune(line); len(r) > limit {
		line = string(r[:limit]) + "..."
	}
	return line
}

// readJSON decodes the file at path into v. strict rejects a field v does
// not have, which catches a typo in the hand-edited baseline.
func readJSON(path string, v any, strict bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// writeBaseline writes b with one requirement per line, so that a change of
// one result is a change of one line in review.
func writeBaseline(path string, b baseline) error {
	var w bytes.Buffer
	fmt.Fprintf(&w, "{\n  \"tckVersion\": %s,\n  \"schemaRevision\": %s,\n  \"conformant\": %t,\n", marshal(b.TCKVersion), marshal(b.SchemaRevision), b.Conformant)
	if len(b.Flaky) > 0 {
		fmt.Fprintf(&w, "  \"flaky\": %s,\n", marshal(b.Flaky))
	}
	w.WriteString("  \"requirements\": {")
	ids := slices.Sorted(maps.Keys(b.Requirements))
	for i, id := range ids {
		if i > 0 {
			w.WriteByte(',')
		}
		fmt.Fprintf(&w, "\n    %s: %s", marshal(id), marshal(b.Requirements[id]))
	}
	w.WriteString("\n  }\n}\n")
	return os.WriteFile(path, w.Bytes(), 0o644)
}

// marshal encodes v on one line, leaving <, > and & as they are.
func marshal(v any) string {
	var w bytes.Buffer
	enc := json.NewEncoder(&w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err) // strings, string slices and entries always encode
	}
	return strings.TrimSuffix(w.String(), "\n")
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tckcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reportPath := fs.String("report", "", "acp-tck --report-json file")
	baselinePath := fs.String("baseline", "", "baseline.json to check, or to rewrite with -update")
	update := fs.Bool("update", false, "rewrite the baseline from the report, keeping its reasons, then check it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *reportPath == "" || *baselinePath == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: tckcheck -report report.json -baseline baseline.json [-update]")
		return 2
	}
	var r report
	if err := readJSON(*reportPath, &r, false); err != nil {
		fmt.Fprintf(stderr, "tckcheck: read report: %v\n", err)
		return 2
	}
	var b baseline
	if err := readJSON(*baselinePath, &b, true); err != nil && !(*update && errors.Is(err, os.ErrNotExist)) {
		fmt.Fprintf(stderr, "tckcheck: read baseline: %v\n", err)
		return 2
	}
	if *update {
		b = updated(b, r)
		if err := writeBaseline(*baselinePath, b); err != nil {
			fmt.Fprintf(stderr, "tckcheck: write baseline: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "wrote %s\n", *baselinePath)
	}
	drifts := check(b, r)
	for _, d := range drifts {
		fmt.Fprintln(stdout, d)
	}
	fmt.Fprintf(stdout, "tck %s, spec %.8s: %d requirements, %d drifted\n", r.TCKVersion, r.SchemaRevision, len(r.Requirements), len(drifts))
	if len(drifts) > 0 {
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
