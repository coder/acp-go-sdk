package roundtrip

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// transcriptLine is one line of an acpwire transcript.jsonl: a JSON-RPC
// message and the direction it travelled, such as "client->agent".
type transcriptLine struct {
	Dir string          `json:"dir"`
	Msg json.RawMessage `json:"msg"`
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// TestTranscripts round-trips every message of the real adapter transcripts
// in testdata/transcripts through the SDK type for its method.
func TestTranscripts(t *testing.T) {
	s := load(t)
	paths, err := filepath.Glob("testdata/transcripts/*.jsonl")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no transcripts: %v", err)
	}
	var checked, extension int
	unknown := map[string]int{}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		pending := map[string]string{} // response direction + id -> method
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 16<<20)
		for n := 1; sc.Scan(); n++ {
			var line transcriptLine
			if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
				t.Fatalf("%s:%d: %v", path, n, err)
			}
			if len(line.Msg) == 0 {
				continue
			}
			var msg rpcMessage
			if err := json.Unmarshal(line.Msg, &msg); err != nil {
				t.Fatalf("%s:%d: %v", path, n, err)
			}
			hasID := len(msg.ID) > 0 && string(msg.ID) != "null"
			method, kind, payload := msg.Method, "Notification", msg.Params
			switch {
			case method != "" && hasID:
				kind = "Request"
				pending[reverse(line.Dir)+string(msg.ID)] = method
			case method == "":
				method, kind, payload = pending[line.Dir+string(msg.ID)], "Response", msg.Result
			}
			def := s.methodDefs[method][kind]
			if len(msg.Error) > 0 {
				def, payload = "Error", msg.Error
			}
			switch {
			case strings.HasPrefix(method, "_"):
				extension++
				continue
			case def == "":
				unknown[method+" "+kind]++
				continue
			}
			checked++
			t.Run(fmt.Sprintf("%s:%d %s %s", filepath.Base(path), n, method, kind), func(t *testing.T) {
				checkRoundTrip(t, def, payload, s.files)
			})
		}
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	var names []string
	for k, n := range unknown {
		names = append(names, fmt.Sprintf("%s x%d", k, n))
	}
	sort.Strings(names)
	t.Logf("SUMMARY transcripts: %d messages checked, %d extension (_*) skipped, %d not in the schema skipped %v",
		checked, extension, len(names), names)
}

func reverse(dir string) string {
	if a, b, ok := strings.Cut(dir, "->"); ok {
		return b + "->" + a
	}
	return dir
}

// credentialValue matches the shape of a secret value, not the name of one:
// a transcript may say ANTHROPIC_API_KEY or inputTokens.
var credentialValue = regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_-]{10,}\.|[Bb]earer [A-Za-z0-9._-]{10,}|xox[abprs]-|-----BEGIN`)

// TestCorpusHasNoCredentials keeps secrets out of committed transcripts. It
// names the file and line only, never the match.
func TestCorpusHasNoCredentials(t *testing.T) {
	paths, _ := filepath.Glob("testdata/transcripts/*.jsonl")
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if credentialValue.MatchString(line) {
				t.Errorf("%s:%d looks like it holds a credential; drop the line", path, i+1)
			}
		}
	}
}
