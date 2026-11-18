package acp

import (
	"encoding/json"
	"testing"
)

// A patch field reads an absent member as unset, null as null, and a value as
// set, and writes each back the same way, so that a receiver can tell
// "unchanged" from "clear". Each input is in the order that the SDK writes
// its members, so the output must equal it byte for byte.
func TestPatch_RoundTripsAbsentNullAndValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		into any
	}{
		{"tool call update without changes", `{"toolCallId":"t"}`, &ToolCallUpdate{}},
		{"tool call update that clears", `{"content":null,"locations":null,"rawInput":null,"title":null,"toolCallId":"t"}`, &ToolCallUpdate{}},
		{"tool call update with empty arrays", `{"content":[],"locations":[],"toolCallId":"t"}`, &ToolCallUpdate{}},
		{"session update tool_call_update", `{"content":[],"sessionUpdate":"tool_call_update","status":null,"toolCallId":"t"}`, &SessionUpdate{}},
		{"session info update", `{"sessionUpdate":"session_info_update","title":null,"updatedAt":"2026-11-18T00:00:00Z"}`, &SessionUpdate{}},
		{"compaction update", `{"_meta":null,"compactionId":"c","error":null,"sessionUpdate":"compaction_update","status":"completed","summary":[]}`, &SessionUpdate{}},
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

func TestPatch_States(t *testing.T) {
	t.Parallel()
	var u ToolCallUpdate
	if err := json.Unmarshal([]byte(`{"toolCallId":"t","title":null,"status":"completed"}`), &u); err != nil {
		t.Fatal(err)
	}
	if !u.Kind.IsUnset() || u.Kind.IsNull() {
		t.Errorf("absent kind: IsUnset %v, IsNull %v, want unset", u.Kind.IsUnset(), u.Kind.IsNull())
	}
	if !u.Title.IsNull() || u.Title.IsUnset() {
		t.Errorf("null title: IsUnset %v, IsNull %v, want null", u.Title.IsUnset(), u.Title.IsNull())
	}
	if _, ok := u.Title.Value(); ok {
		t.Error("null title has a value")
	}
	if status, ok := u.Status.Value(); !ok || status != ToolCallStatusCompleted {
		t.Errorf("status = %q, %v, want completed", status, ok)
	}
}

// A value that does not fit a hinted patch field reads as unset, as the Rust
// types read it as undefined.
func TestPatch_BadValueOfAHintedFieldIsUnset(t *testing.T) {
	t.Parallel()
	var u ToolCallUpdate
	in := `{"toolCallId":"t","title":5,"status":"not-a-status","content":[{"type":"content","content":{"type":"text","text":"x"}},{"type":"bogus"}]}`
	if err := json.Unmarshal([]byte(in), &u); err != nil {
		t.Fatalf("unmarshal %s: %v", in, err)
	}
	if !u.Title.IsUnset() || !u.Status.IsUnset() {
		t.Errorf("title %+v, status %+v, want both unset", u.Title, u.Status)
	}
	if content, ok := u.Content.Value(); !ok || len(content) != 1 {
		t.Errorf("content = %+v, %v, want the one good item", content, ok)
	}
}

func TestPatchSet_WritesANilSliceAsEmpty(t *testing.T) {
	t.Parallel()
	u := ToolCallUpdate{ToolCallId: "t", Content: PatchSet[[]ToolCallContent](nil), Meta: PatchSet[Meta](nil), Kind: PatchNull[ToolKind]()}
	const want = `{"_meta":{},"content":[],"kind":null,"toolCallId":"t"}`
	if out, err := json.Marshal(u); err != nil || string(out) != want {
		t.Errorf("update encodes to %s (error %v), want %s", out, err, want)
	}
}

func TestWithUpdateContent_ClearsWithAnEmptySlice(t *testing.T) {
	t.Parallel()
	update := UpdateToolCall("t", WithUpdateContent(nil), WithUpdateLocations([]ToolCallLocation{}))
	const want = `{"content":[],"locations":[],"sessionUpdate":"tool_call_update","toolCallId":"t"}`
	if out, err := json.Marshal(update); err != nil || string(out) != want {
		t.Errorf("update encodes to %s (error %v), want %s", out, err, want)
	}
}
