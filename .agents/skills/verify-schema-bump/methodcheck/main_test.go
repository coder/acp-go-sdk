package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCheck_ReportsUnwiredMethods(t *testing.T) {
	const metaJSON = `{"agentMethods":{"session_new":"session/new"},"clientMethods":{},"protocolMethods":{"cancel_request":"$/cancel_request"}}`
	const constants = "package acp\n\nconst (\n\tAgentMethodSessionNew = \"session/new\"\n\tProtocolMethodCancelRequest = \"$/cancel_request\"\n)\n"
	const dispatch = "package acp\n\nfunc (a *AgentSideConnection) handle(method string) {\n\tswitch method {\n\tcase AgentMethodSessionNew:\n\t}\n}\n"
	tests := []struct {
		name     string
		files    map[string]string
		wantGaps []string
	}{
		{
			name: "protocol method only sent",
			files: map[string]string{
				"constants_gen.go": constants,
				"agent_gen.go":     dispatch,
				"connection.go":    "package acp\n\nfunc send(c interface{ Notify(string) }) { c.Notify(\"$/cancel_request\") }\n",
			},
			wantGaps: []string{"MISSING handler protocol $/cancel_request in hand-written code"},
		},
		{
			name: "protocol method compared on send",
			files: map[string]string{
				"constants_gen.go": constants,
				"agent_gen.go":     dispatch,
				"connection.go":    "package acp\n\nfunc send(method string) bool { return method == ProtocolMethodCancelRequest }\n",
			},
			wantGaps: []string{"MISSING handler protocol $/cancel_request in hand-written code"},
		},
		{
			name: "protocol method compared on receive",
			files: map[string]string{
				"constants_gen.go": constants,
				"agent_gen.go":     dispatch,
				"connection.go":    receive("ProtocolMethodCancelRequest"),
			},
		},
		{
			name: "protocol method compared before decoding",
			files: map[string]string{
				"constants_gen.go": constants,
				"agent_gen.go":     dispatch,
				"connection.go":    "package acp\n\nimport \"encoding/json\"\n\nfunc recv(line []byte) bool {\n\tvar msg struct{ Method string }\n\tok := msg.Method == ProtocolMethodCancelRequest\n\t_ = json.Unmarshal(line, &msg)\n\treturn ok\n}\n",
			},
			wantGaps: []string{"MISSING handler protocol $/cancel_request in hand-written code"},
		},
		{
			name: "protocol method compared on a shadowing variable",
			files: map[string]string{
				"constants_gen.go": constants,
				"agent_gen.go":     dispatch,
				"connection.go":    "package acp\n\nimport \"encoding/json\"\n\nfunc recv(line []byte, out struct{ Method string }) bool {\n\tvar msg struct{ Method string }\n\t_ = json.Unmarshal(line, &msg)\n\t{\n\t\tmsg := out\n\t\treturn msg.Method == ProtocolMethodCancelRequest\n\t}\n}\n",
			},
			wantGaps: []string{"MISSING handler protocol $/cancel_request in hand-written code"},
		},
		{
			name: "agent method without a case",
			files: map[string]string{
				"constants_gen.go": constants,
				"agent_gen.go":     "package acp\n\nfunc (a *AgentSideConnection) handle(method string) {}\n",
				"connection.go":    receive(`"$/cancel_request"`),
			},
			wantGaps: []string{"MISSING case agent session/new in (*AgentSideConnection).handle"},
		},
		{
			name: "case in a switch on another value",
			files: map[string]string{
				"constants_gen.go": constants,
				"agent_gen.go":     "package acp\n\nfunc (a *AgentSideConnection) handle(method string) {\n\tother := \"\"\n\tswitch other {\n\tcase AgentMethodSessionNew:\n\t}\n}\n",
				"connection.go":    receive(`"$/cancel_request"`),
			},
			wantGaps: []string{"MISSING case agent session/new in (*AgentSideConnection).handle"},
		},
		{
			name: "case in a switch on a shadowing method variable",
			files: map[string]string{
				"constants_gen.go": constants,
				"agent_gen.go":     "package acp\n\nfunc (a *AgentSideConnection) handle(method string) {\n\t{\n\t\tmethod := \"\"\n\t\tswitch method {\n\t\tcase AgentMethodSessionNew:\n\t\t}\n\t}\n}\n",
				"connection.go":    receive(`"$/cancel_request"`),
			},
			wantGaps: []string{"MISSING case agent session/new in (*AgentSideConnection).handle"},
		},
		{
			name: "constant only in hand-written code",
			files: map[string]string{
				"constants_gen.go": "package acp\n\nconst AgentMethodSessionNew = \"session/new\"\n",
				"agent_gen.go":     dispatch,
				"connection.go":    receive("cancelRequest") + "\nconst cancelRequest = \"$/cancel_request\"\n",
			},
			wantGaps: []string{"MISSING constant protocol $/cancel_request"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{"schema/meta.json": metaJSON, "schema/meta.unstable.json": metaJSON}
			for name, src := range tt.files {
				files[name] = src
			}
			for name, src := range files {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			gaps, checked, err := check(dir)
			if err != nil {
				t.Fatal(err)
			}
			if checked != 2 || !reflect.DeepEqual(gaps, tt.wantGaps) {
				t.Errorf("check() = %q, %d checked; want %q, 2 checked", gaps, checked, tt.wantGaps)
			}
		})
	}
}

// receive is a hand-written file whose receive path decodes a message and
// compares its method with want.
func receive(want string) string {
	return "package acp\n\nimport \"encoding/json\"\n\nfunc recv(line []byte) bool {\n\tvar msg struct{ Method string }\n\t_ = json.Unmarshal(line, &msg)\n\treturn msg.Method == " + want + "\n}\n"
}
