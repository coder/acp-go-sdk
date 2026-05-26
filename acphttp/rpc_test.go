package acphttp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPeekMethod(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"request", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "initialize"},
		{"notification", `{"jsonrpc":"2.0","method":"session/update","params":{}}`, "session/update"},
		{"response", `{"jsonrpc":"2.0","id":1,"result":{}}`, ""},
		{"empty", ``, ""},
		{"malformed", `{not json}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PeekMethod([]byte(tc.raw)))
		})
	}
}

func TestHasMethod(t *testing.T) {
	assert.True(t, HasMethod([]byte(`{"method":"x"}`)))
	assert.False(t, HasMethod([]byte(`{"id":1,"result":{}}`)))
	assert.False(t, HasMethod([]byte(`bad`)))
}

func TestPeekParamsSessionID(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"present", `{"method":"session/prompt","params":{"sessionId":"sess-1"}}`, "sess-1"},
		{"absent", `{"method":"initialize","params":{}}`, ""},
		{"no params", `{"method":"initialize"}`, ""},
		{"response not request", `{"id":1,"result":{"sessionId":"sess-1"}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PeekParamsSessionID([]byte(tc.raw)))
		})
	}
}

func TestPeekResultSessionID(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"new session", `{"id":2,"result":{"sessionId":"sess-1"}}`, "sess-1"},
		{"no result", `{"id":2,"error":{"code":-32601,"message":"x"}}`, ""},
		{"request not response", `{"method":"session/new","params":{"sessionId":"x"}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PeekResultSessionID([]byte(tc.raw)))
		})
	}
}

func TestCanonicalID(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"number", `{"id":42,"method":"x"}`, "42"},
		{"string", `{"id":"abc","method":"x"}`, `"abc"`},
		{"null", `{"id":null,"method":"x"}`, ""},
		{"absent", `{"method":"x"}`, ""},
		// Whitespace inside the id is preserved but surrounding whitespace
		// is trimmed.
		{"padded number", `{"id":  7  ,"method":"x"}`, "7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, CanonicalID([]byte(tc.raw)))
		})
	}
}

func TestCanonicalIDFromRaw(t *testing.T) {
	assert.Equal(t, "42", CanonicalIDFromRaw(json.RawMessage(`42`)))
	assert.Equal(t, `"abc"`, CanonicalIDFromRaw(json.RawMessage(`"abc"`)))
	assert.Equal(t, "", CanonicalIDFromRaw(json.RawMessage(`null`)))
	assert.Equal(t, "", CanonicalIDFromRaw(json.RawMessage(``)))
}

func TestIsSessionScoped(t *testing.T) {
	scoped := []string{
		"session/cancel",
		"session/close",
		"session/load",
		"session/prompt",
		"session/resume",
		"session/set_config_option",
		"session/set_mode",
		"session/set_model",
		"session/fork",
		"nes/accept",
		"nes/close",
		"nes/reject",
		"nes/suggest",
		"document/didChange",
		"document/didClose",
		"document/didFocus",
		"document/didOpen",
		"document/didSave",
	}
	for _, m := range scoped {
		assert.True(t, IsSessionScoped(m), "expected %q to be session-scoped", m)
	}
	// session/new and session/list create or enumerate sessions and carry no
	// required sessionId in their params, so they are connection-scoped.
	// nes/start likewise has no sessionId.
	for _, m := range []string{"initialize", "session/new", "session/list", "nes/start", "fs/read_text_file", "", "session/unknown"} {
		assert.False(t, IsSessionScoped(m), "expected %q NOT to be session-scoped", m)
	}
}

func TestIsInitialize(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"basic", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, true},
		{"string id", `{"jsonrpc":"2.0","id":"abc","method":"initialize"}`, true},
		{"no id (notification)", `{"jsonrpc":"2.0","method":"initialize"}`, false},
		{"null id", `{"jsonrpc":"2.0","id":null,"method":"initialize"}`, false},
		{"other method", `{"jsonrpc":"2.0","id":1,"method":"session/new"}`, false},
		{"malformed", `not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsInitialize([]byte(tc.raw)))
		})
	}
}

// nopLookup is a ResponseRouteLookup that always says "no entry" — used
// when classifying request/notification messages that never consult the
// table.
func nopLookup(string) (OutboundTarget, bool) { return OutboundTarget{}, false }

func TestClassifyOutbound_RequestWithSessionId(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":3,"method":"session/request_permission","params":{"sessionId":"sess-9"}}`)
	target := ClassifyOutbound(raw, nopLookup)
	assert.True(t, target.IsSession())
	assert.Equal(t, "sess-9", target.SessionID)
}

func TestClassifyOutbound_NotificationWithoutSessionId(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","method":"some/connection_level_event","params":{}}`)
	target := ClassifyOutbound(raw, nopLookup)
	assert.False(t, target.IsSession())
}

func TestClassifyOutbound_ResponseUsesLookup(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":42,"result":{}}`)

	// No entry → connection stream.
	target := ClassifyOutbound(raw, nopLookup)
	assert.False(t, target.IsSession())

	// Lookup returns session route → session stream.
	target = ClassifyOutbound(raw, func(idKey string) (OutboundTarget, bool) {
		assert.Equal(t, "42", idKey)
		return SessionTarget("sess-1"), true
	})
	assert.True(t, target.IsSession())
	assert.Equal(t, "sess-1", target.SessionID)

	// Lookup returns connection route → connection stream.
	target = ClassifyOutbound(raw, func(idKey string) (OutboundTarget, bool) {
		return ConnectionTarget(), true
	})
	assert.False(t, target.IsSession())
}

func TestClassifyOutbound_NilLookupIsSafe(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	target := ClassifyOutbound(raw, nil)
	assert.False(t, target.IsSession())
}

func TestClassifyOutbound_AbsentOrNullID(t *testing.T) {
	// Response without an id (e.g. notification-style or batch reply) →
	// connection stream, lookup not invoked.
	called := 0
	target := ClassifyOutbound([]byte(`{"jsonrpc":"2.0","result":{}}`), func(string) (OutboundTarget, bool) {
		called++
		return OutboundTarget{}, false
	})
	assert.False(t, target.IsSession())
	assert.Equal(t, 0, called, "lookup should not be called for messages without an id")

	target = ClassifyOutbound([]byte(`{"jsonrpc":"2.0","id":null,"result":{}}`), func(string) (OutboundTarget, bool) {
		called++
		return OutboundTarget{}, false
	})
	assert.False(t, target.IsSession())
	assert.Equal(t, 0, called, "lookup should not be called for null ids")
}
