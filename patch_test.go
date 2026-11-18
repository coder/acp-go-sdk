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

// A nil slice or map in an interface is still written as an empty one, and a
// type that writes itself, such as json.RawMessage, is left to do so.
func TestPatchSet_WritesANilValueAsItsTypeAsks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		patch json.Marshaler
		want  string
	}{
		{"nil slice in an interface", PatchSet[any]([]string(nil)), `[]`},
		{"nil map in an interface", PatchSet[any](map[string]int(nil)), `{}`},
		{"nil interface", PatchSet[any](nil), `null`},
		{"nil raw JSON", PatchSet[json.RawMessage](nil), `null`},
		{"raw JSON", PatchSet(json.RawMessage(`[1]`)), `[1]`},
	}
	for _, tt := range tests {
		tt := tt // the module is Go 1.21, where each iteration shares tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if out, err := json.Marshal(tt.patch); err != nil || string(out) != tt.want {
				t.Errorf("patch encodes to %s (error %v), want %s", out, err, tt.want)
			}
		})
	}
}

// WithUpdateContent and WithUpdateLocations leave their field unset for a nil
// slice, as v0.19.0 left it out, and send [] for an empty non-nil one.
func TestWithUpdateContent_NilIsUnsetAndEmptyClears(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts []ToolCallUpdateOpt
		want string
	}{
		{"nil", []ToolCallUpdateOpt{WithUpdateContent(nil), WithUpdateLocations(nil)}, `{"sessionUpdate":"tool_call_update","toolCallId":"t"}`},
		{"empty", []ToolCallUpdateOpt{WithUpdateContent([]ToolCallContent{}), WithUpdateLocations([]ToolCallLocation{})}, `{"content":[],"locations":[],"sessionUpdate":"tool_call_update","toolCallId":"t"}`},
		{"nil after a value", []ToolCallUpdateOpt{WithUpdateLocations([]ToolCallLocation{{Path: "/a"}}), WithUpdateLocations(nil)}, `{"sessionUpdate":"tool_call_update","toolCallId":"t"}`},
	}
	for _, tt := range tests {
		tt := tt // the module is Go 1.21, where each iteration shares tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if out, err := json.Marshal(UpdateToolCall("t", tt.opts...)); err != nil || string(out) != tt.want {
				t.Errorf("update encodes to %s (error %v), want %s", out, err, tt.want)
			}
		})
	}
}

func TestPatchFromPtr_NilIsUnset(t *testing.T) {
	t.Parallel()
	if p := PatchFromPtr[string](nil); !p.IsUnset() {
		t.Errorf("PatchFromPtr(nil) = %+v, want unset", p)
	}
	title := "x"
	if v, ok := PatchFromPtr(&title).Value(); !ok || v != "x" {
		t.Errorf("PatchFromPtr(&title).Value() = %q, %v, want x, true", v, ok)
	}
}

func TestPatch_PtrIsNilUnlessSet(t *testing.T) {
	t.Parallel()
	if PatchNull[string]().Ptr() != nil || (Patch[string]{}).Ptr() != nil {
		t.Error("Ptr of a null or unset patch is not nil")
	}
	p := PatchSet("x")
	if v := p.Ptr(); v == nil || *v != "x" {
		t.Errorf("Ptr of a set patch = %v, want a pointer to x", v)
	} else if *v = "y"; p.Ptr() == nil || *p.Ptr() != "x" {
		t.Error("writing through Ptr changed the patch")
	}
}

// A set patch whose value encodes as null is null on the wire, so it reads
// back as a null patch.
func TestPatchSet_OfANullValueReadsBackAsNull(t *testing.T) {
	t.Parallel()
	out, err := json.Marshal(ToolCallUpdate{ToolCallId: "t", RawInput: PatchSet[json.RawMessage](nil)})
	if err != nil || string(out) != `{"rawInput":null,"toolCallId":"t"}` {
		t.Fatalf("update encodes to %s (error %v)", out, err)
	}
	var u ToolCallUpdate
	if err := json.Unmarshal(out, &u); err != nil || !u.RawInput.IsNull() {
		t.Errorf("rawInput reads back as %+v (error %v), want null", u.RawInput, err)
	}
}
