package acp

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// SetLogger may be called while the connection's goroutines are already
// emitting diagnostics. Run with -race to detect unsynchronized logger access.
func TestConnection_SetLoggerConcurrentWithDiagnostics(t *testing.T) {
	t.Parallel()

	peerOutputR, peerOutputW := io.Pipe()
	t.Cleanup(func() {
		_ = peerOutputR.Close()
		_ = peerOutputW.Close()
	})
	c := NewConnection(nil, io.Discard, peerOutputR)

	var swapLogs, finalLogs lockedBuffer
	swapLoggers := []*slog.Logger{
		slog.New(slog.NewTextHandler(&swapLogs, nil)),
		slog.New(slog.NewTextHandler(&swapLogs, nil)),
	}
	finalLogger := slog.New(slog.NewTextHandler(&finalLogs, nil))
	c.SetLogger(swapLoggers[0])

	stop := make(chan struct{})
	swapperDone := make(chan struct{})
	go func() {
		defer close(swapperDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				c.SetLogger(swapLoggers[i%len(swapLoggers)])
			}
		}
	}()
	stopSwapping := sync.OnceFunc(func() {
		close(stop)
		<-swapperDone
	})
	t.Cleanup(stopSwapping)

	// Each malformed line makes the receive goroutine log a parse diagnostic.
	for i := 0; i < 200; i++ {
		if _, err := fmt.Fprintf(peerOutputW, "not json %d\n", i); err != nil {
			t.Fatalf("write malformed line: %v", err)
		}
	}
	stopSwapping()

	c.SetLogger(finalLogger)
	const marker = "final-logger-marker"
	if _, err := fmt.Fprintf(peerOutputW, "not json %s\n", marker); err != nil {
		t.Fatalf("write marker line: %v", err)
	}
	_ = peerOutputW.Close()

	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for connection to close")
	}

	if !strings.Contains(finalLogs.String(), marker) {
		t.Fatalf("expected marker diagnostic in final logger, got: %q", finalLogs.String())
	}
	if strings.Contains(swapLogs.String(), marker) {
		t.Fatalf("marker diagnostic went to a replaced logger")
	}
}
