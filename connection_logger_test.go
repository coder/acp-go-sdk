package acp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
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
