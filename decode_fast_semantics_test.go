package acp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The tests in this file compare the fast decoders with the exact ones on
// the rules that the exact decoders follow besides those of encoding/json:
// a patch field without a clear reads null as unset (the nine Patch fields
// of ToolCallUpdate and SessionToolCallUpdate, and _meta of SessionInfoUpdate
// and SessionSessionInfoUpdate), each integer format has the Go type of its
// Rust type, a union alternative takes only its consts (ErrorCode), and an
// object holds each property once. cmd/generate/internal/emit/fast_test.go
// compares them on the rules that no ACP schema reaches: members that hold
// their own type through a pointer, aliases, and scalar alternatives of each
// kind.

// semanticValues are the values that the mutations put in place of each
// member and item: null, the bounds of each integer format and one past
// them, integers that the Go integer types do not take (-0 into an unsigned
// one, 1e0, 1.5), the codes of ErrorCode, and values of the other kinds.
var semanticValues = []string{
	"null", "0", "-0", "1", "-1", "1e0", "1.5", "-0.0",
	"65535", "65536", "2147483647", "2147483648", "-2147483648", "-2147483649",
	"4294967295", "4294967296", "9223372036854775807", "9223372036854775808",
	"-9223372036854775808", "-9223372036854775809", "18446744073709551615", "18446744073709551616",
	"-32700", "-32602", "-32800", "-32000", "-32002", "-32603.0",
	`""`, `"x"`, `"pending"`, "true", "[]", "[null]", "{}",
}

// toolCallFields holds a value for each field of ToolCallUpdate.
const toolCallFields = `"toolCallId":"t","title":"x","kind":"edit","status":"completed","name":"n",` +
	`"content":[{"type":"content","content":{"type":"text","text":"a"}}],"locations":[{"path":"/a","line":3}],` +
	`"rawInput":{"a":1},"rawOutput":"o","_meta":{"k":1}`

// semanticSeeds are values of the types that hold the fields of these
// rules, which the exact decoders read.
var semanticSeeds = []struct {
	typ  reflect.Type
	data string
}{
	{typeOf[ToolCallUpdate](), `{` + toolCallFields + `}`},
	{typeOf[SessionNotification](), `{"sessionId":"s","update":{"sessionUpdate":"tool_call_update",` + toolCallFields + `}}`},
	{typeOf[SessionNotification](), `{"sessionId":"s","update":{"sessionUpdate":"session_info_update","title":"t","updatedAt":"2026-01-01T00:00:00Z","_meta":{"a":1}}}`},
	{typeOf[SessionInfoUpdate](), `{"title":"t","updatedAt":"u","_meta":{"a":1}}`},
	{typeOf[RequestPermissionRequest](), `{"sessionId":"s","toolCall":{` + toolCallFields + `},"options":[{"optionId":"o","name":"Allow","kind":"allow_once"}]}`},
	{typeOf[InitializeRequest](), `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true}}}`},
	{typeOf[ReadTextFileRequest](), `{"sessionId":"s","path":"/a","line":1,"limit":2}`},
	{typeOf[CreateTerminalRequest](), `{"sessionId":"s","command":"ls","outputByteLimit":10}`},
	{typeOf[WaitForTerminalExitResponse](), `{"exitCode":1,"signal":null}`},
	{typeOf[ContentBlock](), `{"type":"resource_link","name":"n","uri":"u","size":1}`},
	{typeOf[PromptResponse](), `{"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3,"thoughtTokens":4,"cachedReadTokens":5,"cachedWriteTokens":6}}`},
	{typeOf[Error](), `{"code":-32602,"message":"m","data":null}`},
	{typeOf[ErrorCode](), `-32700`},
}

// spans returns, for b at offset at of the input, the span of each member
// value and array item at any depth and, for a member, the offset of its
// key, or -1 for an item.
func spans(input, b []byte, out [][3]int) [][3]int {
	at := func(sub []byte) int { return cap(input) - cap(sub) }
	if it, ok := objectMembers(b); ok {
		for it.next() {
			start := at(it.value)
			out = append(out, [3]int{start, start + len(it.value), at(it.key) - 1})
			out = spans(input, it.value, out)
		}
	} else if it, ok := arrayItems(b); ok {
		for it.next() {
			start := at(it.item)
			out = append(out, [3]int{start, start + len(it.item), -1})
			out = spans(input, it.item, out)
		}
	}
	return out
}

// mutations returns the changes of data: each member value and array item,
// at any depth, replaced with each of semanticValues, and each member
// repeated, once with its own value and once after null.
func mutations(data []byte) [][]byte {
	data = data[:len(data):len(data)]
	var out [][]byte
	splice := func(parts ...[]byte) {
		var b []byte
		for _, p := range parts {
			b = append(b, p...)
		}
		out = append(out, b)
	}
	for _, s := range spans(data, data, nil) {
		start, end, key := s[0], s[1], s[2]
		for _, v := range semanticValues {
			splice(data[:start], []byte(v), data[end:])
		}
		if key < 0 {
			continue
		}
		member := data[key:end]
		name := data[key : key+skipString(data[key:], 0)]
		splice(data[:end], []byte(","), member, data[end:])
		splice(data[:key], name, []byte(":null,"), data[key:])
	}
	return out
}

// takesFast reports whether the fast decoder of typ reads data itself.
func takesFast(typ reflect.Type, data []byte) bool {
	return reflect.New(typ).Interface().(interface{ decodeFast([]byte) bool }).decodeFast(data)
}

// TestFastDecode_Semantics compares the fast decoders with the exact ones on
// each mutation of semanticSeeds, into the seed's type and every union.
func TestFastDecode_Semantics(t *testing.T) {
	for _, seed := range semanticSeeds {
		data := []byte(seed.data)
		if err := json.Unmarshal(data, reflect.New(seed.typ).Interface()); err != nil {
			t.Fatalf("seed %v %s: %v", seed.typ, data, err)
		}
		if !takesFast(seed.typ, data) {
			t.Errorf("%v.decodeFast declines the seed %s", seed.typ, data)
		}
		for _, m := range mutations(data) {
			compareType(t, seed.typ, m)
		}
	}
}

// TestFastDecode_ReadsNullAsUnset checks that the fast decoders read null in
// each patch field without a clear themselves, as unset, as the exact
// decoders do.
func TestFastDecode_ReadsNullAsUnset(t *testing.T) {
	for _, c := range []struct {
		typ    reflect.Type
		prefix string
		fields []string
	}{
		{typeOf[ToolCallUpdate](), `{"toolCallId":"t",`, []string{"_meta", "content", "kind", "locations", "name", "rawInput", "rawOutput", "status", "title"}},
		{typeOf[SessionToolCallUpdate](), `{"sessionUpdate":"tool_call_update","toolCallId":"t",`, []string{"_meta", "content", "kind", "locations", "name", "rawInput", "rawOutput", "status", "title"}},
		{typeOf[SessionInfoUpdate](), `{`, []string{"_meta"}},
		{typeOf[SessionSessionInfoUpdate](), `{"sessionUpdate":"session_info_update",`, []string{"_meta"}},
	} {
		for _, field := range c.fields {
			data := []byte(c.prefix + `"` + field + `":null}`)
			compareType(t, c.typ, data)
			if !takesFast(c.typ, data) {
				t.Errorf("%v.decodeFast declines %s", c.typ, data)
			}
			// null reads as absent does.
			absent := []byte(strings.TrimSuffix(c.prefix, ",") + "}")
			got, want := reflect.New(c.typ), reflect.New(c.typ)
			if err := json.Unmarshal(data, got.Interface()); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(absent, want.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Elem().Interface(), want.Elem().Interface()) {
				t.Errorf("%v of %s reads %+v, want %+v as of %s", c.typ, data, got.Elem().Interface(), want.Elem().Interface(), absent)
			}
		}
	}
}

// FuzzFastDecode_Semantics compares the fast decoders with the exact ones on
// a value of a type with a fast decoder, and on its mutation number mut.
func FuzzFastDecode_Semantics(f *testing.F) {
	index := map[reflect.Type]uint16{}
	for i, typ := range fastTypes {
		index[typ] = uint16(i)
	}
	for _, seed := range semanticSeeds {
		f.Add([]byte(seed.data), index[seed.typ], uint16(0))
	}
	f.Fuzz(func(t *testing.T, data []byte, pick, mut uint16) {
		typ := fastTypes[int(pick)%len(fastTypes)]
		compareType(t, typ, data)
		if !json.Valid(data) {
			return
		}
		if ms := mutations(data); len(ms) > 0 {
			compareType(t, typ, ms[int(mut)%len(ms)])
		}
	})
}
