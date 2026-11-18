package acp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A free-form member keeps the JSON it was read as, so a number keeps every
// digit, as in the Rust SDK. Each input is in the order that the SDK writes
// its members, so the output must equal it byte for byte.
func TestOpaquePayloads_RoundTripEveryDigit(t *testing.T) {
	t.Parallel()
	const big = `9007199254740993` // 2^53 + 1
	tests := []struct {
		name string
		in   string
		into any
	}{
		{"text content _meta", `{"_meta":{"big":` + big + `},"text":"x"}`, &TextContent{}},
		{"content block _meta", `{"_meta":{"big":` + big + `},"text":"x","type":"text"}`, &ContentBlock{}},
		{"nested _meta", `{"_meta":{"a":[1.5,18446744073709551617],"b":{"c":` + big + `}},"text":"x"}`, &TextContent{}},
		{"tool call rawInput", `{"rawInput":{"big":` + big + `},"title":"x","toolCallId":"t"}`, &ToolCall{}},
		{"tool call rawOutput", `{"rawOutput":[` + big + `],"title":"x","toolCallId":"t"}`, &ToolCall{}},
		{"session update tool_call", `{"rawInput":{"big":` + big + `},"sessionUpdate":"tool_call","title":"x","toolCallId":"t"}`, &SessionUpdate{}},
		{"tool call update rawOutput", `{"rawOutput":` + big + `,"toolCallId":"t"}`, &ToolCallUpdate{}},
		{"error data", `{"code":1,"data":{"big":` + big + `},"message":"m"}`, &Error{}},
		{"mcp error data", `{"code":1,"data":` + big + `,"message":"m"}`, &UnstableMcpError{}},
		{"mcp error explicit null data", `{"code":1,"data":null,"message":"m"}`, &UnstableMcpError{}},
		{"mcp request params", `{"method":"m","params":{"big":` + big + `},"requestId":"1","serverId":"s"}`, &UnstableMessageMcpRequest{}},
		{"mcp result", `{"result":{"big":` + big + `}}`, &UnstableMessageMcpResponse{}},
	}
	for _, tt := range tests {
		tt := tt // the module is Go 1.21, where each iteration shares tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := json.Unmarshal([]byte(tt.in), tt.into); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.in, err)
			}
			if out, err := json.Marshal(tt.into); err != nil || string(out) != tt.in {
				t.Errorf("%s decodes and encodes to %s (error %v), want it unchanged", tt.in, out, err)
			}
		})
	}
}

func TestMeta_SetThenGet(t *testing.T) {
	t.Parallel()
	var m Meta
	if err := m.Set("traceparent", "00-abc-01"); err != nil {
		t.Fatalf("Set on a nil Meta: %v", err)
	}
	if err := m.Set("big", json.RawMessage("9007199254740993")); err != nil {
		t.Fatalf("Set a json.RawMessage: %v", err)
	}
	var trace string
	if err := m.Get("traceparent", &trace); err != nil || trace != "00-abc-01" {
		t.Fatalf("Get traceparent = %q (error %v), want 00-abc-01", trace, err)
	}
	var n uint64
	if err := m.Get("big", &n); err != nil || n != 9007199254740993 {
		t.Fatalf("Get big = %d (error %v), want 9007199254740993", n, err)
	}
	if out, err := json.Marshal(m); err != nil || string(out) != `{"big":9007199254740993,"traceparent":"00-abc-01"}` {
		t.Fatalf("Meta encodes to %s (error %v)", out, err)
	}
}

func TestMeta_GetFailsForAMissingKeyOrAnotherType(t *testing.T) {
	t.Parallel()
	m := Meta{"n": json.RawMessage("1")}
	var s string
	if err := m.Get("missing", &s); !errors.Is(err, ErrMetaKeyNotFound) || !strings.Contains(err.Error(), `"missing"`) {
		t.Errorf("Get of a missing key: error %v, want ErrMetaKeyNotFound with the key", err)
	}
	if err := m.Get("n", &s); err == nil || errors.Is(err, ErrMetaKeyNotFound) {
		t.Errorf("Get of a number into a string: error %v, want a decode error", err)
	}
	var none Meta
	if err := none.Get("n", &s); !errors.Is(err, ErrMetaKeyNotFound) {
		t.Errorf("Get on a nil Meta: error %v, want ErrMetaKeyNotFound", err)
	}
}

func TestMeta_SetFailsForAValueThatDoesNotEncode(t *testing.T) {
	t.Parallel()
	var m Meta
	if err := m.Set("ch", make(chan int)); err == nil {
		t.Fatal("Set of a channel: no error")
	}
	if m != nil {
		t.Fatalf("a failed Set made the map: %v", m)
	}
}

func TestNewMeta_EncodesEachValue(t *testing.T) {
	t.Parallel()
	m, err := NewMeta(map[string]any{"s": "x", "n": 2, "o": map[string]bool{"ok": true}})
	if err != nil {
		t.Fatalf("NewMeta: %v", err)
	}
	if out, err := json.Marshal(m); err != nil || string(out) != `{"n":2,"o":{"ok":true},"s":"x"}` {
		t.Fatalf("NewMeta encodes to %s (error %v)", out, err)
	}
	if m, err := NewMeta(nil); m != nil || err != nil {
		t.Fatalf("NewMeta(nil) = %v, %v, want nil, nil", m, err)
	}
	if _, err := NewMeta(map[string]any{"f": func() {}}); err == nil {
		t.Fatal("NewMeta of a func: no error")
	}
}

func TestMeta_EmptyIsLeftOut(t *testing.T) {
	t.Parallel()
	for _, meta := range []Meta{nil, {}} {
		out, err := json.Marshal(TextContent{Meta: meta, Text: "x"})
		if err != nil || string(out) != `{"text":"x"}` {
			t.Errorf("TextContent with _meta %#v encodes to %s (error %v), want no _meta", meta, out, err)
		}
	}
}

func TestWithStartLocations_SetsThePathOfAnObjectRawInput(t *testing.T) {
	t.Parallel()
	loc := WithStartLocations([]ToolCallLocation{{Path: "/p"}})
	tests := []struct{ rawInput, want string }{
		{"", `{"path":"/p"}`},
		{`{"cmd":"ls"}`, `{"cmd":"ls","path":"/p"}`},
		{`{"path":"/q"}`, `{"path":"/q"}`},
		{`["ls"]`, `["ls"]`},
	}
	for _, tt := range tests {
		var tc SessionUpdateToolCall
		if tt.rawInput != "" {
			tc.RawInput = json.RawMessage(tt.rawInput)
		}
		loc(&tc)
		if string(tc.RawInput) != tt.want {
			t.Errorf("rawInput %s with one location = %s, want %s", tt.rawInput, tc.RawInput, tt.want)
		}
	}
}

func TestWithRawInput_EncodesTheValue(t *testing.T) {
	t.Parallel()
	update := UpdateToolCall("t", WithUpdateRawInput(map[string]int{"n": 1}), WithUpdateRawOutput(json.RawMessage(`[9007199254740993]`)))
	if got, _ := update.ToolCallUpdate.RawInput.Value(); string(got) != `{"n":1}` {
		t.Errorf("rawInput = %s, want {\"n\":1}", got)
	}
	if got, _ := update.ToolCallUpdate.RawOutput.Value(); string(got) != `[9007199254740993]` {
		t.Errorf("rawOutput = %s, want [9007199254740993]", got)
	}
	if got := StartToolCall("t", "x", WithStartRawInput(func() {})).ToolCall.RawInput; got != nil {
		t.Errorf("rawInput of a func = %s, want unset", got)
	}
}
