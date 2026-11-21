package ir

import (
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk/cmd/generate/internal/load"
)

// proxySchema returns the schema and meta of a proxy role with the agent
// methods initialize and session/prompt and the client method
// session/update, less the definitions named in drop.
func proxySchema(drop ...string) (*load.Schema, *load.Meta) {
	def := func(side, method string) *load.Definition {
		return &load.Definition{Type: "object", XSide: side, XMethod: method}
	}
	defs := map[string]*load.Definition{
		"InitializeRequest":                  def("agent", "initialize"),
		"InitializeResponse":                 def("agent", "initialize"),
		"PromptRequest":                      def("agent", "session/prompt"),
		"PromptResponse":                     def("agent", "session/prompt"),
		"SessionNotification":                def("client", "session/update"),
		"UnstableInitializeProxyRequest":     def("proxy", "proxy/initialize"),
		"UnstableInitializeProxyResponse":    def("proxy", "proxy/initialize"),
		"UnstableProxySuccessorRequest":      def("proxy", "proxy/successor"),
		"UnstableProxySuccessorResponse":     def("proxy", "proxy/successor"),
		"UnstableProxySuccessorNotification": def("proxy", "proxy/successor"),
	}
	for _, name := range drop {
		delete(defs, name)
	}
	return &load.Schema{Defs: defs}, &load.Meta{
		AgentMethods:  map[string]string{"initialize": "initialize", "session_prompt": "session/prompt"},
		ClientMethods: map[string]string{"session_update": "session/update"},
		ProxyMethods:  map[string]string{"proxy_initialize": "proxy/initialize", "proxy_successor": "proxy/successor"},
	}
}

func names(methods []*MethodInfo) string {
	var out []string
	for _, mi := range methods {
		out = append(out, mi.Name)
	}
	return strings.Join(out, " ")
}

func TestBuildProxyRole_ReplacesInitializeFromTheClientSide(t *testing.T) {
	schema, meta := proxySchema()
	role, err := BuildProxyRole(BuildMethodGroups(schema, meta), meta)
	if err != nil {
		t.Fatalf("BuildProxyRole: %v", err)
	}
	for _, tt := range []struct{ what, got, want string }{
		{"FromClient", names(role.FromClient), "UnstableInitializeProxy Prompt"},
		{"Agent", names(role.Agent), "Initialize Prompt"},
		{"Client", names(role.Client), "SessionUpdate"},
		{"Replaces", role.Replaces.Method, "initialize"},
		{"Successor", role.Successor.Method, "proxy/successor"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.what, tt.got, tt.want)
		}
	}
}

func TestBuildProxyRole_NoProxyMethodsIsNoRole(t *testing.T) {
	schema, meta := proxySchema()
	meta.ProxyMethods = nil
	role, err := BuildProxyRole(BuildMethodGroups(schema, meta), meta)
	if role != nil || err != nil {
		t.Fatalf("BuildProxyRole = %+v, %v, want no role and no error", role, err)
	}
}

func TestBuildProxyRole_Fails(t *testing.T) {
	tests := []struct {
		name string
		drop []string
		edit func(groups Groups, meta *load.Meta)
		want string
	}{
		{name: "a proxy method without a rule", edit: func(_ Groups, meta *load.Meta) {
			meta.ProxyMethods["proxy_other"] = "proxy/other"
		}, want: "want exactly"},
		{name: "a missing successor notification", drop: []string{"UnstableProxySuccessorNotification"}, want: "proxy/successor needs"},
		{name: "a missing initialize response", drop: []string{"UnstableInitializeProxyResponse"}, want: "proxy/initialize needs"},
		{name: "no agent initialize", drop: []string{"InitializeRequest", "InitializeResponse"}, want: "initialize, which proxy/initialize replaces"},
		{name: "two agent methods with one Go name", edit: func(groups Groups, meta *load.Meta) {
			meta.AgentMethods["session_resume"] = "session/resume"
			groups[key("agent", "session/resume")] = &MethodInfo{Side: "agent", Method: "session/resume", Req: "ResumeRequest", Name: "Prompt"}
		}, want: `agent methods "session/prompt" and "session/resume" share the Go name Prompt`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema, meta := proxySchema(tt.drop...)
			groups := BuildMethodGroups(schema, meta)
			if tt.edit != nil {
				tt.edit(groups, meta)
			}
			_, err := BuildProxyRole(groups, meta)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("BuildProxyRole error = %v, want one that says %q", err, tt.want)
			}
		})
	}
}
