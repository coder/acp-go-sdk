package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// An integer of format int64, uint32, or uint64 is an int64, so that a
// 32-bit target reads every value that a peer sends: the exit code of a
// Windows process that crashed, and a token count above 2^31.
func TestIntegers_HoldEveryValueOnA32BitTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		into any
	}{
		{`{"exitCode":3221225477}`, &WaitForTerminalExitResponse{}},
		{`{"exitCode":3221225477}`, &TerminalExitStatus{}},
		{`{"inputTokens":3000000000,"outputTokens":1,"totalTokens":3000000001}`, &Usage{}},
		{`{"line":4294967295,"path":"/a"}`, &ToolCallLocation{}},
		{`{"name":"doc","size":9007199254740993,"uri":"file:///doc"}`, &ResourceLink{}},
	}
	for _, tt := range tests {
		tt := tt // the module is Go 1.21, where each iteration shares tt
		t.Run(tt.in, func(t *testing.T) {
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

// Each integer reads the values of the Rust type that its format names (i32,
// i64, u16, u32, u64) and no other, as the Rust SDK does. The messages are
// those of a recorded Claude session (claude-g5-1.jsonl of the G2
// transcripts, lines 10 and 13), as they are and with one integer moved to an
// end of its format's range. want is what the crate
// agent-client-protocol-schema 1.18.0 reads, as the rust-diff probe recorded
// it: the cases named control/integer-* in
// .agents/skills/verify-schema-bump/roundtrip/testdata/rust/cases.jsonl hold
// the same values.
func TestIntegers_ReadTheRangeOfTheirFormat(t *testing.T) {
	t.Parallel()
	usageUpdate := func(used string) string {
		return `{"sessionId":"c090a3c6-f962-49ae-bd60-7c52c9c05650","update":{"sessionUpdate":"usage_update","used":` + used + `,"size":1000000}}`
	}
	prompt := func(total, more string) string {
		return `{"stopReason":"end_turn","usage":{"inputTokens":2,"outputTokens":4,"cachedReadTokens":0,"cachedWriteTokens":20116,"totalTokens":` + total + more + `}}`
	}
	const rejected = "rejected"
	tests := []struct {
		name string
		in   string
		into func() any
		// want is the JSON that the value encodes to after the read: "" for
		// in itself, and rejected when the read fails.
		want string
	}{
		{"recorded usage_update", usageUpdate("20122"), func() any { return new(SessionNotification) }, ""},
		{"used 2^63", usageUpdate("9223372036854775808"), func() any { return new(SessionNotification) }, ""},
		{"used 2^64-1", usageUpdate("18446744073709551615"), func() any { return new(SessionNotification) }, ""},
		{"used 2^64", usageUpdate("18446744073709551616"), func() any { return new(SessionNotification) }, rejected},
		{"used -1", usageUpdate("-1"), func() any { return new(SessionNotification) }, rejected},
		{"used -0", usageUpdate("-0"), func() any { return new(SessionNotification) }, rejected},
		{"used 1.0", usageUpdate("1.0"), func() any { return new(SessionNotification) }, rejected},

		{"recorded prompt usage", prompt("20122", ""), func() any { return new(PromptResponse) }, ""},
		{"totalTokens 2^64-1", prompt("18446744073709551615", ""), func() any { return new(PromptResponse) }, ""},
		{"thoughtTokens 2^64-1", prompt("20122", `,"thoughtTokens":18446744073709551615`), func() any { return new(PromptResponse) }, ""},
		{"thoughtTokens 2^64", prompt("20122", `,"thoughtTokens":18446744073709551616`), func() any { return new(PromptResponse) }, prompt("20122", "")},
		{"totalTokens 2^64", prompt("18446744073709551616", ""), func() any { return new(PromptResponse) }, `{"stopReason":"end_turn"}`},

		{"outputByteLimit 2^64-1", `{"sessionId":"s","command":"c","outputByteLimit":18446744073709551615}`, func() any { return new(CreateTerminalRequest) }, ""},
		{"read line -0", `{"sessionId":"s","path":"/a","line":-0}`, func() any { return new(ReadTextFileRequest) }, rejected},
		{"location line 2^32-1", `{"path":"/p","line":4294967295}`, func() any { return new(ToolCallLocation) }, ""},
		{"location line 2^32", `{"path":"/p","line":4294967296}`, func() any { return new(ToolCallLocation) }, `{"path":"/p"}`},
		{"maxLength 2^32-1", `{"type":"string","maxLength":4294967295}`, func() any { return new(StringPropertySchema) }, `{"maxLength":4294967295}`},
		{"maxLength 2^32", `{"type":"string","maxLength":4294967296}`, func() any { return new(StringPropertySchema) }, rejected},
		{"maxLength -1", `{"type":"string","maxLength":-1}`, func() any { return new(StringPropertySchema) }, rejected},
		{"protocolVersion 65536 in a result", `{"protocolVersion":65536}`, func() any { return new(InitializeResponse) }, rejected},

		{"error code 2^31-1", `2147483647`, func() any { return new(ErrorCode) }, ""},
		{"error code -2^31", `-2147483648`, func() any { return new(ErrorCode) }, ""},
		{"error code 2^31", `2147483648`, func() any { return new(ErrorCode) }, rejected},
		{"error code -2^31-1", `-2147483649`, func() any { return new(ErrorCode) }, rejected},
		{"error object code 2^31", `{"code":2147483648,"message":"m"}`, func() any { return new(Error) }, rejected},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := tt.into()
			err := json.Unmarshal([]byte(tt.in), v)
			// The connection validates the requests and notifications that it
			// reads.
			name := fmt.Sprintf("%T", v)
			if m, ok := v.(interface{ Validate() error }); ok && err == nil && (strings.HasSuffix(name, "Request") || strings.HasSuffix(name, "Notification")) {
				err = m.Validate()
			}
			if tt.want == rejected {
				if err == nil {
					out, _ := json.Marshal(v)
					t.Fatalf("%s reads as %s, want it rejected", tt.in, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("read %s: %v", tt.in, err)
			}
			want := tt.want
			if want == "" {
				want = tt.in
			}
			out, err := json.Marshal(v)
			if err != nil || !sameJSON(t, out, want) {
				t.Errorf("%s reads as %s (error %v), want %s", tt.in, out, err, want)
			}
		})
	}
}

// sameJSON reports whether a and b hold the same JSON value, with each number
// compared by its text, so that no digit of a large integer is rounded away.
func sameJSON(t *testing.T, a []byte, b string) bool {
	t.Helper()
	parse := func(s []byte) any {
		dec := json.NewDecoder(bytes.NewReader(s))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		return v
	}
	return reflect.DeepEqual(parse(a), parse([]byte(b)))
}

// The ReadTextFile of example_client_test.go clamps line and limit to the
// file, so that a request with the largest int64 values reads the end of the
// file instead of panicking.
func TestClientExample_ReadTextFileClampsLineAndLimit(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc"), 0o644); err != nil {
		t.Fatal(err)
	}
	const huge = int64(math.MaxInt64)
	tests := []struct {
		line, limit *int64
		want        string
	}{
		{Ptr[int64](2), Ptr[int64](1), "b"},
		{Ptr[int64](2), Ptr(huge), "b\nc"},
		{Ptr(huge), Ptr(huge), ""},
		{Ptr[int64](-1), nil, "a\nb\nc"},
	}
	for _, tt := range tests {
		got, err := clientExample{}.ReadTextFile(context.Background(), ReadTextFileRequest{Path: path, Line: tt.line, Limit: tt.limit})
		if err != nil || got.Content != tt.want {
			t.Errorf("line %v, limit %v: content %q (error %v), want %q", *tt.line, tt.limit, got.Content, err, tt.want)
		}
	}
}
