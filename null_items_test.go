package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// rawReply sends the JSON-RPC request line to a connection that connect builds
// on a fake peer (newRawPeer), and returns the connection's reply.
func rawReply(t *testing.T, connect func(peerInput io.Writer, peerOutput io.Reader), line string) anyMessage {
	t.Helper()
	p := newRawPeer(t, connect)
	p.send(line)
	return p.await("the reply to "+line, func(m anyMessage) bool { return m.Method == "" })
}

// nullItemsCase is a request that a connection receives. want is a part of
// the -32602 error that the connection must answer with, and "" for a valid
// request that reaches the handler. check, when set, looks at the request that
// the handler got.
type nullItemsCase struct {
	name, method, params string
	invalid              bool
	want                 string
	check                func(t *testing.T, params any)
}

func runNullItemsCase(t *testing.T, tt nullItemsCase, connect func(called chan<- any, peerInput io.Writer, peerOutput io.Reader)) {
	t.Helper()
	called := make(chan any, 1)
	line := `{"jsonrpc":"2.0","id":1,"method":"` + tt.method + `","params":` + tt.params + `}`
	reply := rawReply(t, func(w io.Writer, r io.Reader) { connect(called, w, r) }, line)
	var got any
	select {
	case got = <-called:
	default:
	}
	if tt.invalid {
		if reply.Error == nil || reply.Error.Code != -32602 {
			t.Fatalf("reply = %+v, want error -32602", reply)
		}
		if msg := reply.Error.Error(); !strings.Contains(msg, tt.want) {
			t.Errorf("error %s does not contain %q", msg, tt.want)
		}
		if got != nil {
			t.Errorf("the handler got %+v, want it not called", got)
		}
		return
	}
	if reply.Error != nil {
		t.Fatalf("reply error = %v, want a result", reply.Error)
	}
	if got == nil {
		t.Fatal("the handler was not called")
	}
	if tt.check != nil {
		tt.check(t, got)
	}
}

// encoding/json reads a null item of a string array, or a null value of a
// string map, as "". The schema does not allow null there, and the Rust SDK
// fails the message, so the agent answers -32602 and calls no handler. A
// whole-field null still reads as the field's default.
func TestAgentSideConnection_RejectsNullItemsAndValues(t *testing.T) {
	t.Parallel()
	const stdio = `{"name":"m","command":"c","env":[],"args":`
	tests := []nullItemsCase{
		{name: "new session additional directory", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[],"additionalDirectories":["/a",null]}`, invalid: true, want: "NewSessionRequest.additionalDirectories"},
		{name: "new session stdio server arg", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[` + stdio + `["-v",null]}]}`, invalid: true},
		{name: "load session additional directory", method: AgentMethodSessionLoad, params: `{"sessionId":"s","cwd":"/w","mcpServers":[],"additionalDirectories":[null]}`, invalid: true, want: "LoadSessionRequest.additionalDirectories"},
		{name: "resume session additional directory", method: AgentMethodSessionResume, params: `{"sessionId":"s","cwd":"/w","additionalDirectories":["/a",null]}`, invalid: true, want: "ResumeSessionRequest.additionalDirectories"},
		{name: "fork session additional directory", method: AgentMethodSessionFork, params: `{"sessionId":"s","cwd":"/w","additionalDirectories":["/a",null]}`, invalid: true, want: "UnstableForkSessionRequest.additionalDirectories"},
		{name: "set provider header", method: AgentMethodProvidersSet, params: `{"providerId":"p","apiType":"anthropic","baseUrl":"https://x","headers":{"h":null}}`, invalid: true, want: "UnstableSetProviderRequest.headers"},

		{name: "new session additional directories", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[],"additionalDirectories":["/a",""]}`, check: func(t *testing.T, p any) {
			if got := p.(NewSessionRequest).AdditionalDirectories; len(got) != 2 || got[0] != "/a" || got[1] != "" {
				t.Errorf("additionalDirectories = %q, want [/a ]", got)
			}
		}},
		{name: "new session null additional directories", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[],"additionalDirectories":null}`, check: func(t *testing.T, p any) {
			if got := p.(NewSessionRequest).AdditionalDirectories; got != nil {
				t.Errorf("additionalDirectories = %q, want nil", got)
			}
		}},
		{name: "new session null stdio args", method: AgentMethodSessionNew, params: `{"cwd":"/w","mcpServers":[` + stdio + `null}]}`, check: func(t *testing.T, p any) {
			server := p.(NewSessionRequest).McpServers[0].Stdio
			if server == nil || server.Args == nil || len(server.Args) != 0 {
				t.Errorf("stdio server = %+v, want args read as the default []", server)
			}
		}},
		{name: "set provider headers", method: AgentMethodProvidersSet, params: `{"providerId":"p","apiType":"anthropic","baseUrl":"https://x","headers":{"h":"v"}}`, check: func(t *testing.T, p any) {
			if got := p.(UnstableSetProviderRequest).Headers; len(got) != 1 || got["h"] != "v" {
				t.Errorf("headers = %v, want map[h:v]", got)
			}
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runNullItemsCase(t, tt, func(called chan<- any, w io.Writer, r io.Reader) {
				NewAgentSideConnection(agentFuncs{
					NewSessionFunc: func(_ context.Context, p NewSessionRequest) (NewSessionResponse, error) {
						called <- p
						return NewSessionResponse{SessionId: "s"}, nil
					},
					LoadSessionFunc: func(_ context.Context, p LoadSessionRequest) (LoadSessionResponse, error) {
						called <- p
						return LoadSessionResponse{}, nil
					},
					ResumeSessionFunc: func(_ context.Context, p ResumeSessionRequest) (ResumeSessionResponse, error) {
						called <- p
						return ResumeSessionResponse{}, nil
					},
					UnstableForkSessionFunc: func(_ context.Context, p UnstableForkSessionRequest) (UnstableForkSessionResponse, error) {
						called <- p
						return UnstableForkSessionResponse{SessionId: "f"}, nil
					},
					UnstableSetProviderFunc: func(_ context.Context, p UnstableSetProviderRequest) (UnstableSetProviderResponse, error) {
						called <- p
						return UnstableSetProviderResponse{}, nil
					},
				}, w, r)
			})
		})
	}
}

// The client side of the same rule: terminal arguments and the strings of an
// elicitation form schema.
func TestClientSideConnection_RejectsNullItemsAndValues(t *testing.T) {
	t.Parallel()
	form := func(schema string) string {
		return `{"mode":"form","message":"m","sessionId":"s","requestedSchema":` + schema + `}`
	}
	tests := []nullItemsCase{
		{name: "terminal arg", method: ClientMethodTerminalCreate, params: `{"sessionId":"s","command":"ls","args":["-l",null]}`, invalid: true, want: "CreateTerminalRequest.args"},
		{name: "elicitation enum value", method: ClientMethodElicitationCreate, params: form(`{"type":"object","properties":{"x":{"type":"string","enum":["a",null]}}}`), invalid: true},
		{name: "elicitation multi-select enum value", method: ClientMethodElicitationCreate, params: form(`{"type":"object","properties":{"x":{"type":"array","items":{"type":"string","enum":["a",null]}}}}`), invalid: true},
		{name: "elicitation required property", method: ClientMethodElicitationCreate, params: form(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x",null]}`), invalid: true},

		{name: "terminal args", method: ClientMethodTerminalCreate, params: `{"sessionId":"s","command":"ls","args":["-l"]}`, check: func(t *testing.T, p any) {
			if got := p.(CreateTerminalRequest).Args; len(got) != 1 || got[0] != "-l" {
				t.Errorf("args = %q, want [-l]", got)
			}
		}},
		{name: "terminal null args", method: ClientMethodTerminalCreate, params: `{"sessionId":"s","command":"ls","args":null}`, check: func(t *testing.T, p any) {
			if got := p.(CreateTerminalRequest).Args; got != nil {
				t.Errorf("args = %q, want nil", got)
			}
		}},
		{name: "elicitation enum", method: ClientMethodElicitationCreate, params: form(`{"type":"object","properties":{"x":{"type":"string","enum":["a","b"]}},"required":["x"]}`)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runNullItemsCase(t, tt, func(called chan<- any, w io.Writer, r io.Reader) {
				NewClientSideConnection(&clientFuncs{
					CreateTerminalFunc: func(_ context.Context, p CreateTerminalRequest) (CreateTerminalResponse, error) {
						called <- p
						return CreateTerminalResponse{TerminalId: "t"}, nil
					},
					CreateElicitationFunc: func(_ context.Context, p CreateElicitationRequest) (CreateElicitationResponse, error) {
						called <- p
						return CreateElicitationResponse{Cancel: &CreateElicitationCancel{}}, nil
					},
				}, w, r)
			})
		})
	}
}

// A subagent update whose additionalDirectories holds null fails to decode, so
// the client drops the notification. The connection handles notifications in
// order, so once the valid update after it arrives, the bad one is done.
func TestClientSideConnection_DropsASubagentUpdateWithANullDirectory(t *testing.T) {
	t.Parallel()
	inR, inW := io.Pipe()
	t.Cleanup(func() { _ = inW.Close() })
	updates := make(chan SessionNotification, 2)
	NewClientSideConnection(&clientFuncs{SessionUpdateFunc: func(_ context.Context, n SessionNotification) error {
		updates <- n
		return nil
	}}, io.Discard, inR)
	notify := func(dirs string) string {
		return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"subagent_update","sessionId":"c","additionalDirectories":` + dirs + `}}}` + "\n"
	}
	go func() { _, _ = io.WriteString(inW, notify(`["/a",null]`)+notify(`["/b"]`)) }()
	select {
	case n := <-updates:
		sub := n.Update.SubagentUpdate
		if sub == nil {
			t.Fatalf("first update the handler got = %+v, want a subagent update", n.Update)
		}
		if dirs, ok := sub.AdditionalDirectories.Value(); !ok || len(dirs) != 1 || dirs[0] != "/b" {
			t.Fatalf("first update has additionalDirectories %+v, want the valid one with [/b]", sub.AdditionalDirectories)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler got no update")
	}
}

// AuthMethodTerminal reaches a client in the authMethods of InitializeResponse.
// Decoded on its own, a null arg or env value is an error, as in the Rust SDK.
// authMethods has x-deserialize-skip-invalid-items, so there the bad method is
// dropped and the response still decodes, as the Rust SDK does too.
func TestAuthMethodTerminal_UnmarshalJSON_RejectsNullArgsAndEnvValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		into any
		in   string
		want string // part of the error, "" for no error
	}{
		{"env value", new(AuthMethodTerminal), `{"id":"t","name":"n","env":{"A":null}}`, "AuthMethodTerminal.env"},
		{"arg", new(AuthMethodTerminal), `{"id":"t","name":"n","args":["a",null]}`, "AuthMethodTerminal.args"},
		{"env value in the union", new(AuthMethod), `{"type":"terminal","id":"t","name":"n","env":{"A":null}}`, "invalid variant payload"},
		{"arg in the union", new(AuthMethod), `{"type":"terminal","id":"t","name":"n","args":[null]}`, "invalid variant payload"},
		{"args and env", new(AuthMethodTerminal), `{"id":"t","name":"n","args":["a",""],"env":{"A":""}}`, ""},
		{"null args and env", new(AuthMethodTerminal), `{"id":"t","name":"n","args":null,"env":null}`, ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := json.Unmarshal([]byte(tt.in), tt.into)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("%s: %v", tt.in, err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				got, _ := json.Marshal(tt.into)
				t.Errorf("%s decoded as %s with error %v, want an error with %q", tt.in, got, err, tt.want)
			}
		})
	}

	var resp InitializeResponse
	const in = `{"protocolVersion":1,"authMethods":[{"type":"terminal","id":"t","name":"n","env":{"A":null}},{"id":"a","name":"A"}]}`
	if err := json.Unmarshal([]byte(in), &resp); err != nil {
		t.Fatalf("%s: %v", in, err)
	}
	if len(resp.AuthMethods) != 1 || resp.AuthMethods[0].Agent == nil || resp.AuthMethods[0].Agent.Id != "a" {
		t.Errorf("authMethods = %+v, want only the agent method", resp.AuthMethods)
	}
}

// An elicitation answer whose string array holds null matches no variant of
// ElicitationContentValue, as in the Rust SDK, instead of reading the null as "".
func TestCreateElicitationResponse_UnmarshalJSON_RejectsANullItemOfAStringArray(t *testing.T) {
	var resp CreateElicitationResponse
	if err := json.Unmarshal([]byte(`{"action":"accept","content":{"x":["ok",null]}}`), &resp); err == nil {
		t.Errorf("an answer with a null string array item decodes to %+v, want an error", resp)
	}
	if err := json.Unmarshal([]byte(`{"action":"accept","content":{"x":["ok",""]}}`), &resp); err != nil {
		t.Errorf("an answer with a string array decodes with error %v", err)
	}
}
