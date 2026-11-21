package emit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// typeNamesSchema has a union whose name is one letter, so the names of its
// variants start with two capitals, and a union with a variant that refers to
// a $defs entry whose name has an acronym. Each type must keep the name that
// the generator gave it: the name of the $defs entry, or the name it chose
// for an inline variant.
const typeNamesSchema = `{"$defs": {
  "U": {"oneOf": [
    {"type": "object", "required": ["a"], "properties": {"a": {"type": "string"}}},
    {"type": "string"}]},
  "HTTPHeader": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}},
  "Header": {"anyOf": [{"$ref": "#/$defs/HTTPHeader"}, {"type": "string"}]}
}}`

const typeNamesTest = `package acp

import (
	"encoding/json"
	"testing"
)

func TestUnions_KeepTheNamesOfTheirVariantTypes(t *testing.T) {
	var u U
	if err := json.Unmarshal([]byte(` + "`" + `{"a":"x"}` + "`" + `), &u); err != nil {
		t.Fatal(err)
	}
	var object *UVariant1 = u.UVariant1
	if object == nil || object.A != "x" {
		t.Errorf("U decoded {\"a\":\"x\"} as %+v", u)
	}
	var h Header
	if err := json.Unmarshal([]byte(` + "`" + `{"name":"n"}` + "`" + `), &h); err != nil {
		t.Fatal(err)
	}
	var header *HTTPHeader = h.HTTPHeader
	if header == nil || header.Name != "n" {
		t.Errorf("Header decoded {\"name\":\"n\"} as %+v", h)
	}
}
`

func TestWriteTypesJen_TypesKeepTheNamesTheyAreRegisteredUnder(t *testing.T) {
	checkGenerated(t, typeNamesSchema, map[string]string{"names_test.go": typeNamesTest})
}

// renamedCopiesTest decodes real messages into the union of schema 1.32.0
// and into its copy under another name, and requires the same result.
const renamedCopiesTest = `package acp

import (
	"encoding/json"
	"os"
	"testing"
)

// decodesAsOriginal decodes each value in file as an O and as a C and
// requires that both fail or both succeed and encode the same JSON. It
// returns the number of values that decode.
func decodesAsOriginal[O, C any](t *testing.T, file string, check func(C)) int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	decoded := 0
	for _, value := range values {
		var original O
		var copied C
		errOriginal, errCopied := json.Unmarshal(value, &original), json.Unmarshal(value, &copied)
		if (errOriginal == nil) != (errCopied == nil) {
			t.Errorf("%s: the original decodes with %v, the copy with %v", value, errOriginal, errCopied)
			continue
		}
		if errOriginal != nil {
			continue
		}
		decoded++
		check(copied)
		a, errA := json.Marshal(original)
		b, errB := json.Marshal(copied)
		if errA != nil || errB != nil || string(a) != string(b) {
			t.Errorf("%s: the original encodes as %s (%v), the copy as %s (%v)", value, a, errA, b, errB)
		}
	}
	return decoded
}

func TestRenamedCopies_DecodeRealMessagesAsTheOriginals(t *testing.T) {
	chunks := 0
	if n := decodesAsOriginal[SessionUpdate, U](t, "updates.json", func(u U) {
		var chunk *UAgentMessageChunk = u.AgentMessageChunk
		if chunk != nil {
			chunks++
		}
	}); n == 0 || chunks == 0 {
		t.Errorf("%d session updates decode, %d of them agent message chunks", n, chunks)
	}
	terminal, agent := 0, 0
	if n := decodesAsOriginal[AuthMethod, ACPAuthMethod](t, "auth_methods.json", func(m ACPAuthMethod) {
		var tm *ACPAuthMethodTerminal = m.Terminal
		var am *ACPAuthMethodAgent = m.Agent
		if tm != nil {
			terminal++
		}
		if am != nil {
			agent++
		}
	}); n == 0 || terminal == 0 || agent == 0 {
		t.Errorf("%d auth methods decode, %d terminal and %d agent", n, terminal, agent)
	}
}
`

// TestWriteTypesJen_RenamedCopiesOfRealUnionsDecodeRealMessages generates
// schema 1.32.0 with two copies of real unions under names that the
// generator must not re-case: SessionUpdate as U, and AuthMethod as
// ACPAuthMethod, whose agent variant refers to ACPAuthMethodAgent, a copy of
// AuthMethodAgent. The generated package decodes the session updates and auth
// methods of the G2 transcripts into each union and its copy.
func TestWriteTypesJen_RenamedCopiesOfRealUnionsDecodeRealMessages(t *testing.T) {
	repo := filepath.Join("..", "..", "..", "..")
	data, err := os.ReadFile(filepath.Join(repo, "schema", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	defs := schema["$defs"].(map[string]any)
	defs["U"] = defs["SessionUpdate"]
	authMethod, err := json.Marshal(defs["AuthMethod"])
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.ReplaceAll(string(authMethod), `"#/$defs/AuthMethodAgent"`, `"#/$defs/ACPAuthMethodAgent"`)
	if renamed == string(authMethod) {
		t.Fatal("AuthMethod of schema 1.32.0 does not refer to AuthMethodAgent")
	}
	defs["ACPAuthMethod"] = json.RawMessage(renamed)
	defs["ACPAuthMethodAgent"] = defs["AuthMethodAgent"]
	fixture, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}

	var updates, authMethods []json.RawMessage
	transcripts, err := filepath.Glob(filepath.Join(repo, ".agents", "skills", "verify-schema-bump", "roundtrip", "testdata", "transcripts", "*.jsonl"))
	if err != nil || len(transcripts) == 0 {
		t.Fatalf("find the G2 transcripts: %v", err)
	}
	for _, path := range transcripts {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := bufio.NewScanner(f)
		lines.Buffer(nil, 16<<20)
		for lines.Scan() {
			var record struct {
				Msg struct {
					Method string `json:"method"`
					Params struct {
						Update json.RawMessage `json:"update"`
					} `json:"params"`
					Result struct {
						AuthMethods []json.RawMessage `json:"authMethods"`
					} `json:"result"`
				} `json:"msg"`
			}
			if err := json.Unmarshal(lines.Bytes(), &record); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			if record.Msg.Method == "session/update" {
				updates = append(updates, record.Msg.Params.Update)
			}
			authMethods = append(authMethods, record.Msg.Result.AuthMethods...)
		}
		f.Close()
		if err := lines.Err(); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	files := map[string]string{"renamed_test.go": renamedCopiesTest}
	for name, values := range map[string][]json.RawMessage{"updates.json": updates, "auth_methods.json": authMethods} {
		data, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(data)
	}
	checkGenerated(t, string(fixture), files)
}
