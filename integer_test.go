package acp

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
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
