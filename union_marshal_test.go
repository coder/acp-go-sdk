package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A union with no variant set has no JSON form: the Rust SDK's enums have no
// empty state, and none of these unions takes an empty value. MarshalJSON
// rejects it with an error that names the union, as Validate does.
func TestUnion_MarshalJSON_RejectsAZeroValueByName(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		zero interface{ Validate() error }
	}{
		{"ContentBlock", &ContentBlock{}},
		{"SessionUpdate", &SessionUpdate{}},
		{"ToolCallContent", &ToolCallContent{}},
		{"RequestPermissionOutcome", &RequestPermissionOutcome{}},
		{"McpServer", &McpServer{}},
		// null is a RequestId, but only its Null variant encodes as null.
		{"RequestId", &RequestId{}},
		{"CreateElicitationRequest", &CreateElicitationRequest{}},
	} {
		out, err := json.Marshal(tt.zero)
		if want := tt.name + " has no variant set"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("json.Marshal(%s{}) = %q, %v; want the error %q", tt.name, out, err, want)
		}
		if err := tt.zero.Validate(); err == nil {
			t.Errorf("%s{}.Validate() = nil, want an error as from MarshalJSON", tt.name)
		}
	}
}

func TestUnion_ZeroValueInAMessage_ValidateAgreesWithMarshalJSON(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		union string
		msg   interface{ Validate() error }
	}{
		{"SessionUpdate", &SessionNotification{SessionId: "s"}},
		{"ContentBlock", &PromptRequest{SessionId: "s", Prompt: []ContentBlock{{}}}},
		{"RequestPermissionOutcome", &RequestPermissionResponse{}},
	} {
		verr := tt.msg.Validate()
		_, merr := json.Marshal(tt.msg)
		if verr == nil || merr == nil {
			t.Errorf("%T with a zero %s: Validate() = %v, json.Marshal error = %v; want both to fail", tt.msg, tt.union, verr, merr)
			continue
		}
		if !strings.Contains(merr.Error(), tt.union+" has no variant set") {
			t.Errorf("%T with a zero %s: json.Marshal error = %v, want it to name %s", tt.msg, tt.union, merr, tt.union)
		}
	}
}

// A session/update with no update set fails in SessionUpdate and says why.
func TestAgentSideConnection_SessionUpdate_NamesAZeroUpdate(t *testing.T) {
	t.Parallel()
	inR, inW := io.Pipe()
	t.Cleanup(func() { _ = inW.Close() })
	conn := NewAgentSideConnection(agentFuncs{}, io.Discard, inR)
	err := conn.SessionUpdate(context.Background(), SessionNotification{SessionId: "s"})
	if err == nil || !strings.Contains(err.Error(), "SessionUpdate has no variant set") {
		t.Fatalf("SessionUpdate with no update set = %v, want an error that names SessionUpdate", err)
	}
}

// The union values that real agents sent in the G2 transcripts encode as
// they were read.
func TestUnion_MarshalJSON_RoundTripsTheG2Transcripts(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join(".agents", "skills", "verify-schema-bump", "roundtrip", "testdata", "transcripts", "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("find the G2 transcripts: %v (%d files)", err, len(files))
	}
	counts := map[string]int{}
	check := func(name string, in json.RawMessage, into any) {
		t.Helper()
		counts[name]++
		if err := json.Unmarshal(in, into); err != nil {
			t.Errorf("unmarshal %s %s: %v", name, in, err)
			return
		}
		out, err := json.Marshal(into)
		if err != nil {
			t.Errorf("marshal %s %s: %v", name, in, err)
			return
		}
		var want, got any
		_ = json.Unmarshal(in, &want)
		_ = json.Unmarshal(out, &got)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s %s encodes as %s", name, in, out)
		}
	}
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		lines := bufio.NewScanner(f)
		lines.Buffer(nil, 1<<20)
		for lines.Scan() {
			var rec struct {
				Msg struct {
					Method string `json:"method"`
					Params struct {
						Prompt []json.RawMessage `json:"prompt"`
						Update struct {
							Content json.RawMessage `json:"content"`
						} `json:"update"`
					} `json:"params"`
					Result struct {
						Outcome json.RawMessage `json:"outcome"`
					} `json:"result"`
				} `json:"msg"`
			}
			if err := json.Unmarshal(lines.Bytes(), &rec); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			m := rec.Msg
			for _, block := range m.Params.Prompt {
				check("ContentBlock", block, new(ContentBlock))
			}
			if m.Method == ClientMethodSessionUpdate && strings.HasPrefix(string(m.Params.Update.Content), "{") {
				check("ContentBlock", m.Params.Update.Content, new(ContentBlock))
			}
			if m.Result.Outcome != nil {
				check("RequestPermissionOutcome", m.Result.Outcome, new(RequestPermissionOutcome))
			}
		}
		_ = f.Close()
		if err := lines.Err(); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	if counts["ContentBlock"] == 0 || counts["RequestPermissionOutcome"] == 0 {
		t.Fatalf("checked %v, want each union at least once", counts)
	}
}
