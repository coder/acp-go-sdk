package acp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestConnectionSetLogger_ConcurrentWithReceive guards the logger field against
// a data race: NewConnection spawns receive/processNotifications before the
// caller has any chance to install a logger, so a later SetLogger writes the
// field while those goroutines read it through loggerOrDefault.
//
// Unparseable input makes receive log, which is the read side of the race.
// Run under -race; without synchronization on the field this fails.
func TestConnectionSetLogger_ConcurrentWithReceive(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outW.Close()
		_ = inR.Close()
		_ = outR.Close()
	})

	// Drain peer output so the connection never blocks on a write.
	go func() { _, _ = io.Copy(io.Discard, outR) }()

	c := NewConnection(func(ctx context.Context, method string, params json.RawMessage) (any, *RequestError) {
		return nil, nil
	}, outW, inR)

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Install before any input arrives so the parse errors below stay off stderr.
	c.SetLogger(discard)

	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if _, err := inW.Write([]byte("not json\n")); err != nil {
				return
			}
		}
	}()

	for i := 0; i < iterations; i++ {
		c.SetLogger(discard)
	}
	wg.Wait()
}

// TestConnection_LogsBoundedPeerText checks that a value the peer sends
// reaches the log only as a bounded prefix. Each line below puts 1 MiB in a
// place that a log record names: the raw line, an id, a method or the error of
// a handler.
func TestConnection_LogsBoundedPeerText(t *testing.T) {
	big := strings.Repeat("A", 1<<20)
	handler := func(_ context.Context, method string, params json.RawMessage) (any, *RequestError) {
		switch {
		case method == "fail":
			return nil, NewInvalidParams(map[string]string{"params": string(params)})
		case method == "ok":
			return nil, nil
		case strings.HasPrefix(method, "badError"):
			return nil, &RequestError{Code: -32000, Message: "bad", Data: json.RawMessage("{")}
		default:
			return nil, NewMethodNotFound(method)
		}
	}
	cases := []struct {
		name string
		line string
		opts []ConnectionOption
		msg  string
		// also is a part of the record besides msg, such as the length of
		// the value it cut.
		also string
	}{
		{name: "invalid JSON", line: big, msg: "failed to parse incoming message", also: `"len":1048576`},
		{name: "neither id nor method", line: `{"jsonrpc":"2.0","result":"` + big + `"}`, msg: "received message with neither id nor method", also: `"len":1048605`},
		{name: "multibyte line", line: "\"" + strings.Repeat("é", 1<<19), msg: "failed to parse incoming message", also: `"raw":"\"` + strings.Repeat("é", 511) + `... (1048577 bytes)"`},
		{name: "request id", line: `{"jsonrpc":"2.0","id":{"k":"` + big + `"},"method":"ok"}`, msg: "failed to canonicalize inbound request id"},
		{name: "response id", line: `{"jsonrpc":"2.0","id":{"k":"` + big + `"},"result":null}`, msg: "failed to canonicalize response id"},
		{name: "cancel request id", line: `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":{"k":"` + big + `"}}}`, msg: "failed to canonicalize $/cancel_request requestId"},
		{name: "unhandled notification", line: `{"jsonrpc":"2.0","method":"u` + big + `"}`, msg: "ignored unhandled notification"},
		{name: "failed notification", line: `{"jsonrpc":"2.0","method":"fail","params":{"x":"` + big + `"}}`, msg: "failed to handle notification"},
		{name: "unencodable handler error", line: `{"jsonrpc":"2.0","id":1,"method":"badError` + big + `"}`, msg: "failed to encode the error of a request handler"},
		{name: "rejected request", line: `{"jsonrpc":"2.0","id":1,"method":"r` + big + `"}`, opts: []ConnectionOption{WithByteBudget(1 << 20)}, msg: "rejected request from the peer"},
		{name: "notification over budget", line: `{"jsonrpc":"2.0","method":"n` + big + `"}`, opts: []ConnectionOption{WithByteBudget(1 << 20)}, msg: "notification does not fit the byte budget; closing connection"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			t.Cleanup(func() {
				_ = inW.Close()
				_ = outR.Close()
			})
			go func() { _, _ = io.Copy(io.Discard, outR) }()

			var logs syncBuffer
			c := NewConnection(handler, outW, inR, tc.opts...)
			c.SetLogger(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			go func() { _, _ = inW.Write([]byte(tc.line + "\n")) }()

			want := `"msg":"` + tc.msg + `"`
			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(logs.String(), want) {
				if time.Now().After(deadline) {
					t.Fatalf("no %s record; logs: %.300s", want, logs.String())
				}
				time.Sleep(time.Millisecond)
			}
			if !strings.Contains(logs.String(), tc.also) {
				t.Errorf("no %s in the logs: %.300s", tc.also, logs.String())
			}
			for _, rec := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				if len(rec) > 4096 {
					t.Errorf("a log record has %d bytes: %.300s...", len(rec), rec)
				}
			}
		})
	}
}
