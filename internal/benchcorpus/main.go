// Command benchcorpus writes the corpus of recorded ACP traffic that the
// inbound decoding benchmarks in inbound_bench_test.go read, into
// testdata/bench. Run it from the repository root:
//
//	go run ./internal/benchcorpus                    # the adapters' fixtures
//	go run ./internal/benchcorpus -g3 "$ACP_VERIFY_RUN/evidence"
//
// It fetches the scenario fixtures of the Claude and Codex ACP adapters at
// the pinned commits below from GitHub and converts each to the transcript
// format of acpwire: one JSON object per line, {"dir","msg"}, where msg is a
// JSON-RPC message as it crosses the wire and dir is the way it goes, such as
// "agent->client". The fixtures record only what the agent sends, so a
// response names its request's method in "method".
//
// With -g3 it also copies the transcript.jsonl of each journey that
// verify-acp-go-sdk's acp-verify wrote (see testdata/bench/README.md).
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A source is one adapter's fixtures at a pinned commit. Each scenario is a
// file under dir/<profile>/; the first profile that has the scenario wins,
// so every scenario is in the corpus once.
type source struct {
	name     string
	repo     string
	commit   string
	dir      string
	profiles []string
	convert  func(line []byte, ids *int) ([]byte, error)
}

var sources = []source{
	{
		name:     "claude-agent-acp",
		repo:     "agentclientprotocol/claude-agent-acp",
		commit:   "1c90fb959a003eba1eed1dac94400c9cdef63195",
		dir:      "src/tests/acp-scenarios/origin-main",
		profiles: []string{"zed", "plain"},
		convert:  convertClaude,
	},
	{
		name:     "codex-acp",
		repo:     "agentclientprotocol/codex-acp",
		commit:   "ad09e0ead18511a65a0761cac832aa3087219b10",
		dir:      "src/__tests__/scenarios/data/baseline",
		profiles: []string{"zed", "plain"},
		convert:  convertCodex,
	},
}

func main() {
	out := flag.String("out", "testdata/bench", "the corpus folder")
	g3 := flag.String("g3", "", "an acp-verify evidence folder whose journeys to copy into <out>/g3")
	flag.Parse()
	if *g3 != "" {
		if err := copyJourneys(*g3, filepath.Join(*out, "g3")); err != nil {
			log.Fatal(err)
		}
		return
	}
	for _, src := range sources {
		if err := fetch(src, filepath.Join(*out, src.name)); err != nil {
			log.Fatalf("%s: %v", src.name, err)
		}
	}
}

// fetch writes the converted scenarios of src and its LICENSE to dir.
func fetch(src source, dir string) error {
	url := fmt.Sprintf("https://codeload.github.com/%s/tar.gz/%s", src.repo, src.commit)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	files, err := untar(resp.Body)
	if err != nil {
		return err
	}
	license, ok := files["LICENSE"]
	if !ok {
		return errors.New("no LICENSE")
	}
	scenarios := map[string][]byte{}
	for _, profile := range src.profiles {
		prefix := path.Join(src.dir, profile) + "/"
		for name, data := range files {
			scenario, isFixture := strings.CutPrefix(name, prefix)
			if !isFixture || strings.Contains(scenario, "/") || !strings.HasSuffix(scenario, ".jsonl") {
				continue
			}
			if _, seen := scenarios[scenario]; !seen {
				scenarios[scenario] = data
			}
		}
	}
	if len(scenarios) == 0 {
		return fmt.Errorf("no scenarios under %s", src.dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "LICENSE"), license, 0o644); err != nil {
		return err
	}
	for scenario, data := range scenarios {
		converted, err := convertFile(data, src.convert)
		if err != nil {
			return fmt.Errorf("%s: %w", scenario, err)
		}
		if err := os.WriteFile(filepath.Join(dir, scenario), converted, 0o644); err != nil {
			return err
		}
	}
	log.Printf("%s@%s: %d scenarios", src.name, src.commit[:12], len(scenarios))
	return nil
}

// untar returns the regular files of a GitHub tarball by their path in the
// repository.
func untar(r io.Reader) (map[string][]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// GitHub puts every file under one folder named after the commit.
		_, name, ok := strings.Cut(hdr.Name, "/")
		if !ok {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
}

func convertFile(data []byte, convert func([]byte, *int) ([]byte, error)) ([]byte, error) {
	var out bytes.Buffer
	ids := 0
	for n, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		converted, err := convert(line, &ids)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n+1, err)
		}
		if converted != nil {
			out.Write(converted)
			out.WriteByte('\n')
		}
	}
	return out.Bytes(), nil
}

// claudeMethods maps the kind of a claude-agent-acp fixture line, the name of
// the client method that the agent called or the response it returned, to
// its ACP method and message type.
var claudeMethods = map[string]struct{ method, kind string }{
	"initialize":          {"initialize", "response"},
	"newSession":          {"session/new", "response"},
	"loadSession":         {"session/load", "response"},
	"promptResponse":      {"session/prompt", "response"},
	"sessionUpdate":       {"session/update", "notification"},
	"completeElicitation": {"elicitation/complete", "notification"},
	"requestPermission":   {"session/request_permission", "request"},
	"createElicitation":   {"elicitation/create", "request"},
}

// convertClaude converts a line {"kind","payload"} of claude-agent-acp's
// src/tests/acp-scenarios fixtures.
func convertClaude(line []byte, ids *int) ([]byte, error) {
	var rec struct {
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, err
	}
	if rec.Kind == "extNotification" {
		var ext struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(rec.Payload, &ext); err != nil {
			return nil, err
		}
		return message(ext.Method, "notification", ext.Params, ids), nil
	}
	m, ok := claudeMethods[rec.Kind]
	if !ok {
		return nil, fmt.Errorf("unknown kind %q", rec.Kind)
	}
	return message(m.method, m.kind, rec.Payload, ids), nil
}

// convertCodex converts a line {"direction","method","params"} of codex-acp's
// src/__tests__/scenarios/data fixtures. A "codexResponse" line is the Codex
// app server's own protocol, not ACP, so it is dropped.
func convertCodex(line []byte, ids *int) ([]byte, error) {
	var rec struct {
		Direction string          `json:"direction"`
		Method    string          `json:"method"`
		Params    json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, err
	}
	switch rec.Direction {
	case "codexResponse":
		return nil, nil
	case "notify":
		return message(rec.Method, "notification", rec.Params, ids), nil
	case "request", "response":
		return message(rec.Method, rec.Direction, rec.Params, ids), nil
	}
	return nil, fmt.Errorf("unknown direction %q", rec.Direction)
}

// message returns the transcript line of a message that the agent sent to the
// client. It keeps payload as the fixture has it.
func message(method, kind string, payload json.RawMessage, ids *int) []byte {
	var msg bytes.Buffer
	msg.WriteString(`{"jsonrpc":"2.0",`)
	switch kind {
	case "notification":
		fmt.Fprintf(&msg, `"method":%q,"params":%s}`, method, payload)
	case "request":
		*ids++
		fmt.Fprintf(&msg, `"id":%d,"method":%q,"params":%s}`, *ids, method, payload)
	case "response":
		*ids++
		fmt.Fprintf(&msg, `"id":%d,"result":%s}`, *ids, payload)
	}
	line := `{"dir":"agent->client",`
	if kind == "response" {
		line += `"method":` + strconv.Quote(method) + `,`
	}
	return []byte(line + `"msg":` + msg.String() + `}`)
}

// copyJourneys copies <evidence>/<journey>/transcript.jsonl to
// <dir>/<journey>.jsonl for each journey.
func copyJourneys(evidence, dir string) error {
	paths, err := filepath.Glob(filepath.Join(evidence, "*", "transcript.jsonl"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no transcripts in %s", evidence)
	}
	sort.Strings(paths)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		journey := filepath.Base(filepath.Dir(p))
		if err := os.WriteFile(filepath.Join(dir, journey+".jsonl"), data, 0o644); err != nil {
			return err
		}
	}
	log.Printf("g3: %d journeys", len(paths))
	return nil
}
