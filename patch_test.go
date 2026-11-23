package acp

import (
	"encoding/json"
	"testing"
)

// A patch field reads an absent member as unset, null as null where the schema
// defines a clear, and a value as set, and writes each back the same way, so
// that a receiver can tell "unchanged" from "clear". Each input is in the
// order that the SDK writes its members, so the output must equal it byte for
// byte.
func TestPatch_RoundTripsAbsentNullAndValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		into any
	}{
		{"tool call update without changes", `{"toolCallId":"t"}`, &ToolCallUpdate{}},
		{"tool call update with empty arrays", `{"content":[],"locations":[],"toolCallId":"t"}`, &ToolCallUpdate{}},
		{"session update tool_call_update", `{"content":[],"sessionUpdate":"tool_call_update","status":"completed","toolCallId":"t"}`, &SessionUpdate{}},
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
	var u SessionInfoUpdate
	if err := json.Unmarshal([]byte(`{"title":null,"updatedAt":"2026-11-18T07:20:01.837Z"}`), &u); err != nil {
		t.Fatal(err)
	}
	if !u.Meta.IsUnset() || u.Meta.IsNull() {
		t.Errorf("absent _meta: IsUnset %v, IsNull %v, want unset", u.Meta.IsUnset(), u.Meta.IsNull())
	}
	if !u.Title.IsNull() || u.Title.IsUnset() {
		t.Errorf("null title: IsUnset %v, IsNull %v, want null", u.Title.IsUnset(), u.Title.IsNull())
	}
	if _, ok := u.Title.Value(); ok {
		t.Error("null title has a value")
	}
	if at, ok := u.UpdatedAt.Value(); !ok || at != "2026-11-18T07:20:01.837Z" {
		t.Errorf("updatedAt = %q, %v, want the timestamp", at, ok)
	}
}

// ACP v1 defines no clear for the fields of a tool_call_update or for _meta
// of a session_info_update: null there leaves the stored value as it is, as
// an absent member does. The protocol docs say so for name, rawInput and
// rawOutput, the v2 migration guide says that v1 treats null like omission,
// and the Rust crate reads null in each of these fields as absent. So the SDK
// reads such a null as unset and writes the value back as the crate does.
// Each input but the cursor_update one is a real message of the G2
// transcripts (codex-g5-1.jsonl line 133 and claude-g5-1.jsonl line 17) with
// null members added, and each want is what agent-client-protocol-schema
// writes for it: 1.18.0 for the 141/ cases of the Rust probe, 1.21.0 for its
// cursor/ case. The anchor of a cursor_update reads null as unset because its
// own description says that omitted and null are equivalent, while null
// clears capabilities and _meta.
func TestPatch_NullWhereTheSchemaDefinesNoClearIsUnset(t *testing.T) {
	t.Parallel()
	const (
		update  = `"sessionId":"01a1ed8d-ea5e-7ec3-b9a5-0eb4d83755fc","update":{"sessionUpdate":"tool_call_update","toolCallId":"exec-c4e2eb8a-c857-490e-a87f-e77e95f50787","status":"completed"`
		info    = `"sessionId":"c090a3c6-f962-49ae-bd60-7c52c9c05650","update":{"sessionUpdate":"session_info_update","title":"Pong reply test","updatedAt":"2026-11-18T07:20:01.837Z"`
		allNull = `{"toolCallId":"exec-c4e2eb8a-c857-490e-a87f-e77e95f50787","kind":null,"status":null,"title":null,"name":null,"content":null,"locations":null,"rawInput":null,"rawOutput":null,"_meta":null}`
	)
	tests := []struct {
		name, in, want string
		into           any
	}{
		{"title null in a tool_call_update", `{` + update + `,"title":null}}`, `{` + update + `}}`, &SessionNotification{}},
		{"content null in a tool_call_update", `{` + update + `,"content":null}}`, `{` + update + `}}`, &SessionNotification{}},
		{"every field null in a tool call update", allNull, `{"toolCallId":"exec-c4e2eb8a-c857-490e-a87f-e77e95f50787"}`, &ToolCallUpdate{}},
		{"_meta null in a session_info_update", `{` + info + `,"_meta":null}}`, `{` + info + `}}`, &SessionNotification{}},
		{"anchor null in a cursor_update", `{"sessionId":"s","update":{"sessionUpdate":"cursor_update","cursorId":"c1","anchor":null,"capabilities":null}}`, `{"sessionId":"s","update":{"sessionUpdate":"cursor_update","cursorId":"c1","capabilities":null}}`, &SessionNotification{}},
	}
	for _, tt := range tests {
		tt := tt // the module is Go 1.21, where each iteration shares tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := json.Unmarshal([]byte(tt.in), tt.into); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.in, err)
			}
			out, err := json.Marshal(tt.into)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if same, got, want := equalJSON(out, []byte(tt.want)); !same {
				t.Errorf("%s decodes and encodes to %s, want %s as the Rust crate writes it", tt.in, got, want)
			}
		})
	}
	var u ToolCallUpdate
	if err := json.Unmarshal([]byte(allNull), &u); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]interface{ IsUnset() bool }{
		"kind": u.Kind, "status": u.Status, "title": u.Title, "name": u.Name, "content": u.Content,
		"locations": u.Locations, "rawInput": u.RawInput, "rawOutput": u.RawOutput, "_meta": u.Meta,
	} {
		if !p.IsUnset() {
			t.Errorf("null %s reads as %+v, want unset", name, p)
		}
	}
}

// A null Patch in a field where null does not clear is left out, as the Rust
// crate leaves out None, so that no peer can read it as a clear. Where null
// clears, it is written as null.
func TestPatchNull_IsLeftOutWhereNullDoesNotClear(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"ToolCallUpdate", ToolCallUpdate{ToolCallId: "t", Title: PatchNull[string](), Content: PatchNull[[]ToolCallContent](), Meta: PatchNull[Meta]()}, `{"toolCallId":"t"}`},
		{"SessionUpdate tool_call_update", UpdateToolCall("t", func(u *SessionToolCallUpdate) { u.Name = PatchNull[string]() }), `{"sessionUpdate":"tool_call_update","toolCallId":"t"}`},
		{"SessionInfoUpdate", SessionInfoUpdate{Title: PatchNull[string](), Meta: PatchNull[Meta]()}, `{"title":null}`},
		{"CursorUpdate", CursorUpdate{CursorId: "c", Anchor: PatchNull[CursorAnchor](), Capabilities: PatchNull[CursorCapabilities]()}, `{"capabilities":null,"cursorId":"c"}`},
	}
	for _, tt := range tests {
		tt := tt // the module is Go 1.21, where each iteration shares tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if out, err := json.Marshal(tt.v); err != nil || string(out) != tt.want {
				t.Errorf("encodes to %s (error %v), want %s", out, err, tt.want)
			}
		})
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
	u := ToolCallUpdate{ToolCallId: "t", Content: PatchSet[[]ToolCallContent](nil), Meta: PatchSet[Meta](nil)}
	const want = `{"_meta":{},"content":[],"toolCallId":"t"}`
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

// A set patch whose value encodes as null is null on the wire. In rawInput
// null leaves the stored value as it is, so it reads back as unset, as the
// Rust crate reads it as None.
func TestPatchSet_OfANullRawInputReadsBackAsUnset(t *testing.T) {
	t.Parallel()
	out, err := json.Marshal(ToolCallUpdate{ToolCallId: "t", RawInput: PatchSet[json.RawMessage](nil)})
	if err != nil || string(out) != `{"rawInput":null,"toolCallId":"t"}` {
		t.Fatalf("update encodes to %s (error %v)", out, err)
	}
	var u ToolCallUpdate
	if err := json.Unmarshal(out, &u); err != nil || !u.RawInput.IsUnset() {
		t.Errorf("rawInput reads back as %+v (error %v), want unset", u.RawInput, err)
	}
}
