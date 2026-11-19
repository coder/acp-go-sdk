package roundtrip

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// rustCase is one line of a cases file: a value and the schema def to read
// it as. A G2_PROBES file is a cases file too: its lines have no id, so they
// are named after the file and the line.
type rustCase struct {
	ID   string          `json:"id"`
	Def  string          `json:"def"`
	JSON json.RawMessage `json:"json"`
}

// sideResult is how one SDK read a case: the value encoded again, or the
// error. The Rust probe writes one per line.
type sideResult struct {
	ID  string          `json:"id"`
	OK  bool            `json:"ok"`
	Out json.RawMessage `json:"out,omitempty"`
	Err string          `json:"err,omitempty"`
}

// verdict is how the two reads of a case compare.
type verdict string

const (
	verdictSame       verdict = "same"        // both accept it and encode the same JSON
	verdictBothReject verdict = "both-reject" // both reject it
	verdictDiffers    verdict = "differs"     // both accept it and encode different JSON
	verdictGoOnly     verdict = "go-only"     // Go accepts it and Rust rejects it
	verdictRustOnly   verdict = "rust-only"   // Rust accepts it and Go rejects it
	verdictNoType     verdict = "no-type"     // an SDK has no type for the def
)

// agrees reports whether the two SDKs read the case alike.
func (v verdict) agrees() bool { return v == verdictSame || v == verdictBothReject }

// TestRust is the Rust probe gate: it reads each case of RUST_CASES with the
// SDK and with RUST_PROBE, the rustprobe binary, and reports each case that
// they read differently. scripts/rust-diff builds the binary and runs it.
func TestRust(t *testing.T) {
	probe := os.Getenv("RUST_PROBE")
	if probe == "" {
		t.Skip("RUST_PROBE is not set; run scripts/rust-diff")
	}
	path := os.Getenv("RUST_CASES")
	if path == "" {
		path = filepath.Join("testdata", "rust", "cases.jsonl")
	}
	cases, err := readCases(path)
	if err != nil {
		t.Fatal(err)
	}
	rust, err := runRustProbe(probe, cases)
	if err != nil {
		t.Fatal(err)
	}
	files := load(t).files
	counts := map[verdict]int{}
	for _, c := range cases {
		v, detail := compareSides(goSide(c.Def, c.JSON), rust[c.ID], defRefs(c.Def, files))
		counts[v]++
		if !v.agrees() {
			t.Errorf("RUST %s %s %s: %s", v, c.Def, c.ID, detail)
		}
	}
	t.Logf("SUMMARY rust: %d cases: %d same, %d both-reject, %d differs, %d go-only, %d rust-only, %d no-type",
		len(cases), counts[verdictSame], counts[verdictBothReject], counts[verdictDiffers],
		counts[verdictGoOnly], counts[verdictRustOnly], counts[verdictNoType])
}

// readCases reads a cases file and names each unnamed case FILE:LINE.
func readCases(path string) ([]rustCase, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []rustCase
	seen := map[string]bool{}
	for i, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var c rustCase
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		if c.Def == "" || c.JSON == nil {
			return nil, fmt.Errorf("%s:%d: a case needs def and json", path, i+1)
		}
		if c.ID == "" {
			c.ID = fmt.Sprintf("%s:%d", filepath.Base(path), i+1)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("%s:%d: id %q is not unique", path, i+1, c.ID)
		}
		seen[c.ID] = true
		cases = append(cases, c)
	}
	return cases, nil
}

// runRustProbe feeds every case to the probe in one run and returns its
// results by id.
func runRustProbe(probe string, cases []rustCase) (map[string]sideResult, error) {
	var in bytes.Buffer
	enc := json.NewEncoder(&in)
	for _, c := range cases {
		if err := enc.Encode(c); err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(probe)
	cmd.Stdin = &in
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", probe, err)
	}
	results := map[string]sideResult{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		var r sideResult
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("%s wrote %q: %w", probe, oneLine(sc.Text(), 120), err)
		}
		results[r.ID] = r
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(results) != len(cases) {
		return nil, fmt.Errorf("%s answered %d of %d cases", probe, len(results), len(cases))
	}
	return results, nil
}

// goSide reads data as the SDK type of def, as the connection reads a
// message: decode it, call its Validate method when it has one, and encode
// it again.
func goSide(def string, data []byte) sideResult {
	newValue, ok := registry[def]
	if !ok {
		return sideResult{Err: "NO-TYPE " + def}
	}
	v := newValue()
	if err := json.Unmarshal(data, v); err != nil {
		return sideResult{Err: "decode: " + err.Error()}
	}
	if val, ok := v.(interface{ Validate() error }); ok {
		if err := val.Validate(); err != nil {
			return sideResult{Err: "validate: " + err.Error()}
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return sideResult{Err: "encode: " + err.Error()}
	}
	return sideResult{OK: true, Out: out}
}

// compareSides compares the Go and the Rust read of one case, and describes
// the difference when they do not agree. Before it compares the two outputs
// it normalizes them against schemas as G2 does: a member that is null or
// equals its schema default counts as absent, because each SDK may write a
// default that the other leaves out.
func compareSides(g, r sideResult, schemas []schemaRef) (verdict, string) {
	switch {
	case strings.HasPrefix(g.Err, "NO-TYPE") || strings.HasPrefix(r.Err, "NO-TYPE"):
		return verdictNoType, fmt.Sprintf("go: %s; rust: %s", orOK(g.Err), orOK(r.Err))
	case !g.OK && !r.OK:
		return verdictBothReject, ""
	case !r.OK:
		return verdictGoOnly, fmt.Sprintf("go=%s; rust error: %s", oneLine(string(g.Out), 120), oneLine(r.Err, 160))
	case !g.OK:
		return verdictRustOnly, fmt.Sprintf("go error: %s; rust=%s", oneLine(g.Err, 160), oneLine(string(r.Out), 120))
	}
	a, errA := decodeExact(g.Out)
	b, errB := decodeExact(r.Out)
	if errA != nil || errB != nil {
		return verdictDiffers, fmt.Sprintf("unreadable output: go: %v; rust: %v", errA, errB)
	}
	var unknown, diffs []string
	diffSides("", normalize(a, schemas, "", &unknown), normalize(b, schemas, "", &unknown), &diffs)
	if len(diffs) == 0 {
		return verdictSame, ""
	}
	return verdictDiffers, fmt.Sprintf("%d difference(s): %s", len(diffs), strings.Join(diffs, "; "))
}

func orOK(err string) string {
	if err == "" {
		return "ok"
	}
	return oneLine(err, 160)
}

// diffSides lists each path where the Go output a and the Rust output b
// differ. Unlike walkDiff it tells an integer literal from a number literal
// of the same value (2 and 2.0), because a reader that picks a union variant
// by the JSON type reads them differently.
func diffSides(path string, a, b any, diffs *[]string) {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for _, k := range sortedKeys(keys) {
			av, inA := am[k]
			bv, inB := bm[k]
			if inA && inB {
				diffSides(path+"/"+k, av, bv, diffs)
			} else {
				*diffs = append(*diffs, fmt.Sprintf("%s/%s: go=%s rust=%s", path, k, member(av, inA), member(bv, inB)))
			}
		}
		return
	}
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if aok && bok && len(as) == len(bs) {
		for i := range as {
			diffSides(fmt.Sprintf("%s/%d", path, i), as[i], bs[i], diffs)
		}
		return
	}
	an, aNum := a.(json.Number)
	bn, bNum := b.(json.Number)
	if aNum && bNum && sameLiteral(an, bn) || !aNum && !bNum && reflect.DeepEqual(a, b) {
		return
	}
	if path == "" {
		path = "/"
	}
	*diffs = append(*diffs, fmt.Sprintf("%s: go=%s rust=%s", path, member(a, true), member(b, true)))
}

// member shows a member's value, or <absent>.
func member(v any, present bool) string {
	if !present {
		return "<absent>"
	}
	b, _ := json.Marshal(v)
	return oneLine(string(b), 120)
}

// sameLiteral reports whether two number literals have the same value and
// are both integer literals or both not.
func sameLiteral(a, b json.Number) bool {
	x, okA := new(big.Rat).SetString(string(a))
	y, okB := new(big.Rat).SetString(string(b))
	return okA && okB && x.Cmp(y) == 0 && isIntegerLiteral(a) == isIntegerLiteral(b)
}

func isIntegerLiteral(n json.Number) bool { return !strings.ContainsAny(string(n), ".eE") }

func TestCompareSides(t *testing.T) {
	ok := func(out string) sideResult { return sideResult{OK: true, Out: json.RawMessage(out)} }
	fail := func(err string) sideResult { return sideResult{Err: err} }
	tests := []struct {
		name string
		g, r sideResult
		want verdict
	}{
		{"same JSON in another key order", ok(`{"a":1,"b":"x"}`), ok(`{"b":"x","a":1}`), verdictSame},
		{"null reads as absent", ok(`{"a":1,"b":null}`), ok(`{"a":1}`), verdictSame},
		{"same float in another notation", ok(`{"n":1e21}`), ok(`{"n":1e+21}`), verdictSame},
		{"integer literal against number literal", ok(`{"n":2}`), ok(`{"n":2.0}`), verdictDiffers},
		{"empty object against absent", ok(`{}`), ok(`{"content":{}}`), verdictDiffers},
		{"empty array against absent", ok(`{}`), ok(`{"items":[]}`), verdictDiffers},
		{"null item against empty string", ok(`["a",""]`), ok(`["a",null]`), verdictDiffers},
		{"only Go accepts", ok(`{}`), fail("missing field `x`"), verdictGoOnly},
		{"only Rust accepts", fail("decode: bad"), ok(`{}`), verdictRustOnly},
		{"both reject", fail("decode: bad"), fail("bad"), verdictBothReject},
		{"no Rust type", ok(`{}`), fail("NO-TYPE X"), verdictNoType},
		{"no Go type", fail("NO-TYPE X"), ok(`{}`), verdictNoType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, detail := compareSides(tt.g, tt.r, nil)
			if got != tt.want {
				t.Errorf("verdict = %s (%s), want %s", got, detail, tt.want)
			}
		})
	}
}

func TestCompareSides_UsesTheSchemaDefault(t *testing.T) {
	schemas := []schemaRef{{node: map[string]any{
		"type":       "object",
		"properties": map[string]any{"loadSession": map[string]any{"type": "boolean", "default": false}},
	}}}
	got, detail := compareSides(sideResult{OK: true, Out: json.RawMessage(`{}`)},
		sideResult{OK: true, Out: json.RawMessage(`{"loadSession":false}`)}, schemas)
	if got != verdictSame {
		t.Errorf("verdict = %s (%s), want same: Rust writes the default that Go leaves out", got, detail)
	}
}

// A patch field reads null as "clear" and an absent member as "unchanged",
// so the two must not compare equal.
func TestCompareSides_KeepsNullApartFromAbsent(t *testing.T) {
	schemas := []schemaRef{{node: map[string]any{
		"type":       "object",
		"properties": map[string]any{"title": map[string]any{"type": []any{"string", "null"}}},
	}}}
	got, detail := compareSides(sideResult{OK: true, Out: json.RawMessage(`{"title":null}`)},
		sideResult{OK: true, Out: json.RawMessage(`{}`)}, schemas)
	if got != verdictDiffers {
		t.Errorf("verdict = %s (%s), want differs", got, detail)
	}
}

// The connection validates inbound requests and notifications, not the
// result of a request it sent.
func TestGoSide_ValidatesOnlyRequestsAndNotifications(t *testing.T) {
	if g := goSide("CreateTerminalRequest", []byte(`{"sessionId":"s"}`)); g.OK {
		t.Error("goSide accepts a CreateTerminalRequest without command, which the dispatcher rejects")
	}
	if g := goSide("ListSessionsResponse", []byte(`{}`)); !g.OK {
		t.Errorf("goSide rejects a ListSessionsResponse that Connection.SendRequest accepts: %s", g.Err)
	}
}

func TestReadCases(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases, err := readCases(write("probes.jsonl", `{"def":"A","json":1,"probe":"null"}`+"\n\n"+`{"id":"x","def":"B","json":{}}`+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 || cases[0].ID != "probes.jsonl:1" || cases[1].ID != "x" {
		t.Errorf("cases = %+v, want probes.jsonl:1 and x", cases)
	}
	if _, err := readCases(write("dup.jsonl", `{"id":"x","def":"A","json":1}`+"\n"+`{"id":"x","def":"A","json":2}`+"\n")); err == nil {
		t.Error("readCases accepts two cases with the same id")
	}
	if _, err := readCases(write("nodef.jsonl", `{"id":"x","json":1}`+"\n")); err == nil {
		t.Error("readCases accepts a case without def")
	}
}

// The committed cases must name defs that the SDK has a type for, so that a
// renamed def shows up in make test and not first in a hand run.
func TestRustCases_NameSDKTypes(t *testing.T) {
	cases, err := readCases(filepath.Join("testdata", "rust", "cases.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if _, ok := registry[c.Def]; !ok {
			t.Errorf("case %s: def %s has no SDK type", c.ID, c.Def)
		}
	}
}
