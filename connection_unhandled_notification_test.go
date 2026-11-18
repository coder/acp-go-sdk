package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"
)

// logRecords keeps the level of each SDK log record per notification method.
type logRecords struct {
	mu     sync.Mutex
	levels map[string][]slog.Level
}

func (r *logRecords) Enabled(context.Context, slog.Level) bool { return true }
func (r *logRecords) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *logRecords) WithGroup(string) slog.Handler            { return r }

func (r *logRecords) Handle(_ context.Context, rec slog.Record) error {
	method := ""
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == "method" {
			method = a.Value.String()
		}
		return true
	})
	if method == "" {
		return nil // such as "connection closed"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.levels[method] = append(r.levels[method], rec.Level)
	return nil
}

func (r *logRecords) snapshot() map[string][]slog.Level {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string][]slog.Level{}
	for k, v := range r.levels {
		out[k] = slices.Clone(v)
	}
	return out
}

// TestClientSideConnection_UnhandledOptionalNotification checks the log level
// of each notification outcome. A client that does not implement the optional
// auth/status_update notification gets a DEBUG record, not an ERROR one, while
// a params decode failure and a handler error stay at ERROR. An optional
// request that the client does not implement still answers -32601.
func TestClientSideConnection_UnhandledOptionalNotification(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outW.Close()
	})

	client := &clientFuncs{SessionUpdateFunc: func(context.Context, SessionNotification) error {
		return errors.New("handler failed")
	}}
	if _, ok := any(client).(ClientUnstableAuthStatusUpdate); ok {
		t.Fatal("test client must not implement ClientUnstableAuthStatusUpdate")
	}
	logs := &logRecords{levels: map[string][]slog.Level{}}
	conn := NewClientSideConnection(client, outW, inR)
	conn.SetLogger(slog.New(logs))

	replies := make(chan anyMessage, 1)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var msg anyMessage
			if json.Unmarshal(sc.Bytes(), &msg) == nil {
				replies <- msg
			}
		}
	}()

	for _, line := range []string{
		`{"jsonrpc":"2.0","method":"auth/status_update","params":{"state":"authenticated"}}`,
		`{"jsonrpc":"2.0","method":"_vendor/status","params":{}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":5}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"mcp/message","params":{"serverId":"srv","requestId":"r1","method":"tools/list"}}`,
	} {
		if _, err := io.WriteString(inW, line+"\n"); err != nil {
			t.Fatalf("write %s: %v", line, err)
		}
	}

	select {
	case msg := <-replies:
		if msg.Error == nil || msg.Error.Code != -32601 {
			t.Fatalf("mcp/message reply = %+v, want error -32601", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply to mcp/message")
	}

	// The connection handles notifications in order, so once both
	// session/update records are in, the earlier notifications are done.
	want := map[string][]slog.Level{
		ClientMethodAuthStatusUpdate: {slog.LevelDebug},
		ClientMethodSessionUpdate:    {slog.LevelError, slog.LevelError},
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(logs.snapshot()[ClientMethodSessionUpdate]) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := logs.snapshot()
	if len(got) != len(want) {
		t.Fatalf("log levels by method = %v, want %v", got, want)
	}
	for method, levels := range want {
		if !slices.Equal(got[method], levels) {
			t.Errorf("log levels for %s = %v, want %v", method, got[method], levels)
		}
	}
}
