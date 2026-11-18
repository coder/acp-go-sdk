package ir

import (
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

func TestBuildMethodGroups_PutsOnlyStableBaselineMethodsInAgentAndClient(t *testing.T) {
	def := func(side, method string) *load.Definition {
		return &load.Definition{Type: "object", XSide: side, XMethod: method}
	}
	schema := &load.Schema{Defs: map[string]*load.Definition{
		"NewSessionRequest":            def("agent", "session/new"),
		"NewSessionResponse":           def("agent", "session/new"),
		"CancelNotification":           def("agent", "session/cancel"),
		"LoadSessionRequest":           def("agent", "session/load"),
		"ForkSessionRequest":           def("agent", "session/fork"),
		"UnstableDeleteSessionRequest": def("agent", "session/delete"),
		"SessionNotification":          def("client", "session/update"),
		"UnstableMessageMcpRequest":    def("client", "mcp/message"),
	}}
	meta := &load.Meta{
		AgentMethods: map[string]string{
			"session_new": "session/new", "session_cancel": "session/cancel", "session_load": "session/load",
			"session_fork": "session/fork", "session_delete": "session/delete",
		},
		ClientMethods: map[string]string{"session_update": "session/update", "mcp_message": "mcp/message"},
	}
	groups := BuildMethodGroups(schema, meta)
	tests := []struct {
		key, name, optional string
	}{
		{"agent|session/new", "NewSession", ""},
		{"agent|session/cancel", "Cancel", ""},
		// Optional since the SDK's first release, under its old name.
		{"agent|session/load", "LoadSession", "AgentLoader"},
		// A stable method outside the baseline, as a newly stabilized one is.
		{"agent|session/fork", "ForkSession", "AgentForkSession"},
		// A baseline method that this schema still has as unstable.
		{"agent|session/delete", "UnstableDeleteSession", "AgentUnstableDeleteSession"},
		{"client|session/update", "SessionUpdate", ""},
		{"client|mcp/message", "UnstableMessageMcp", "ClientUnstableMessageMcp"},
	}
	for _, tt := range tests {
		mi := groups[tt.key]
		if mi == nil {
			t.Errorf("%s: no method group", tt.key)
			continue
		}
		if mi.Name != tt.name || mi.Optional != tt.optional {
			t.Errorf("%s: Name %q, Optional %q, want %q, %q", tt.key, mi.Name, mi.Optional, tt.name, tt.optional)
		}
	}
}
