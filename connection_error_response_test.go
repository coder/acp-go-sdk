package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

// An error response whose error object does not decode as a RequestError,
// such as a code that is not an integer or does not fit an int, fails the
// request that waits for it with a -32603 that carries the error as it was
// read. It does not leave the request waiting until its ctx ends, and the
// connection goes on reading.
func TestConnection_SendRequest_FailsOnAnErrorObjectThatDoesNotDecode(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, errJSON string }{
		{"fractional code", `{"code":1.5,"message":"m"}`},
		{"string code", `{"code":"x","message":"m"}`},
		{"exponent code", `{"code":1e20,"message":"m"}`},
		{"code above int64", `{"code":9223372036854775808,"message":"m"}`},
		{"code below int64", `{"code":-9223372036854775809,"message":"m"}`},
		{"message not a string", `{"code":1,"message":5}`},
		{"error not an object", `"oops"`},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			peerR, connW := io.Pipe()
			connR, peerW := io.Pipe()
			t.Cleanup(func() { _ = peerW.Close(); _ = peerR.Close() })
			c := NewConnection(func(context.Context, string, json.RawMessage) (any, *RequestError) {
				return nil, nil
			}, connW, connR)
			c.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
			// The peer answers each request with the error, then with a
			// result.
			go func() {
				sc := bufio.NewScanner(peerR)
				for i := 0; sc.Scan(); i++ {
					var req struct{ ID json.RawMessage }
					_ = json.Unmarshal(sc.Bytes(), &req)
					reply := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":%s}`, req.ID, tt.errJSON)
					if i%2 == 1 {
						reply = fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"ok":true}}`, req.ID)
					}
					if _, err := io.WriteString(peerW, reply+"\n"); err != nil {
						return
					}
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			start := time.Now()
			_, err := SendRequest[json.RawMessage](c, ctx, "ping", map[string]any{})
			if time.Since(start) > 2*time.Second {
				t.Fatalf("SendRequest took %v, want it to fail without waiting for its ctx", time.Since(start))
			}
			var re *RequestError
			if !errors.As(err, &re) || re.Code != -32603 {
				t.Fatalf("SendRequest = %v, want a -32603 error", err)
			}
			var data struct {
				Error    string          `json:"error"`
				Response json.RawMessage `json:"response"`
			}
			if err := json.Unmarshal(re.Data, &data); err != nil || data.Error == "" || compact(t, data.Response) != compact(t, json.RawMessage(tt.errJSON)) {
				t.Errorf("SendRequest error data = %s, want the decode error and the error object %s", re.Data, tt.errJSON)
			}
			if got, err := SendRequest[json.RawMessage](c, ctx, "ping", map[string]any{}); err != nil || string(got) != `{"ok":true}` {
				t.Errorf("the next SendRequest = %s, %v; want the result", got, err)
			}
			if err := c.SendRequestNoResult(ctx, "ping", map[string]any{}); !errors.As(err, &re) || re.Code != -32603 {
				t.Errorf("SendRequestNoResult = %v, want a -32603 error", err)
			}
		})
	}
}

func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return "invalid: " + string(raw)
	}
	return b.String()
}
