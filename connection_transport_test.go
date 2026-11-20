package acp

import (
	"context"
	"io"
	"testing"
	"time"
)

// TestConnection_CloseTransportEndsBlockedWrite checks what the NewConnection
// godoc tells callers: Done does not end a write to a peer that stopped
// reading, and closing the transport after Done does.
func TestConnection_CloseTransportEndsBlockedWrite(t *testing.T) {
	inR, inW := io.Pipe()
	_, outW := io.Pipe() // nobody reads the peer's input
	c := NewConnection(nil, outW, inR)

	sent := make(chan error, 1)
	go func() { sent <- c.SendNotification(context.Background(), "n", nil) }()
	time.Sleep(50 * time.Millisecond) // let the write start and block

	_ = inW.Close()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close after the peer closed its output")
	}
	select {
	case err := <-sent:
		t.Fatalf("SendNotification returned %v before the transport closed", err)
	case <-time.After(100 * time.Millisecond):
	}

	_ = outW.Close()
	select {
	case err := <-sent:
		if err == nil {
			t.Fatal("SendNotification returned nil after its write failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendNotification still blocks after the transport closed")
	}
}
