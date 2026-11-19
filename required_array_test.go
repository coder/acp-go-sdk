package acp

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNewSessionRequest_MarshalJSON_NilMcpServersEncodesEmptyArray(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(NewSessionRequest{Cwd: "/work"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if ok, got, want := equalJSON(got, []byte(`{"cwd":"/work","mcpServers":[]}`)); !ok {
		t.Fatalf("request without MCP servers encodes to %s, want %s", got, want)
	}
}

func TestMcpServer_MarshalJSON_NilArgsAndEnvEncodeEmptyArrays(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(McpServer{Stdio: &McpServerStdio{Name: "fs", Command: "mcp-fs"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if ok, got, want := equalJSON(got, []byte(`{"name":"fs","command":"mcp-fs","args":[],"env":[]}`)); !ok {
		t.Fatalf("stdio server without args and env encodes to %s, want %s", got, want)
	}
}

func TestPlan_UnmarshalJSON_BadOrNullEntriesReadAsEmpty(t *testing.T) {
	t.Parallel()
	for _, in := range []string{`{"entries":"oops"}`, `{"entries":null}`} {
		var plan Plan
		if err := json.Unmarshal([]byte(in), &plan); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		if plan.Entries == nil || len(plan.Entries) != 0 {
			t.Errorf("%s decodes entries to %#v, want an empty non-nil slice", in, plan.Entries)
		}
	}
	// The hints read a bad value as [], but the member must be there, as in
	// the Rust SDK.
	var plan Plan
	var typeErr *json.UnmarshalTypeError
	if err := json.Unmarshal([]byte(`{}`), &plan); !errors.As(err, &typeErr) || typeErr.Value != "missing member" || typeErr.Field != "entries" {
		t.Errorf("unmarshal {}: error %v, want a missing member error at Plan.entries", err)
	}
}

func TestPromptRequest_UnmarshalJSON_RejectsANullPrompt(t *testing.T) {
	t.Parallel()
	var req PromptRequest
	var typeErr *json.UnmarshalTypeError
	if err := json.Unmarshal([]byte(`{"sessionId":"s","prompt":null}`), &req); !errors.As(err, &typeErr) || typeErr.Value != "null" || typeErr.Field != "prompt" {
		t.Errorf("unmarshal: error %v, want a null type error at PromptRequest.prompt", err)
	}
}
