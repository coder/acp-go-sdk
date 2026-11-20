package acp

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// childModeEnv makes the test binary run childModes[mode] as a child process
// for the tests below instead of running tests.
const childModeEnv = "ACP_TRANSPORT_TEST_CHILD"

var childModes = map[string]func(){
	// A peer that never reads its stdin.
	"idle": func() { time.Sleep(time.Minute) },
}

func TestMain(m *testing.M) {
	if run, ok := childModes[os.Getenv(childModeEnv)]; ok {
		run()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// bigParams returns params that do not fit a pipe or socket buffer, so
// writing them blocks until the peer reads.
func bigParams() string { return strings.Repeat("x", 4<<20) }

// TestConnection_CloseTransportEndsBlockedWrite checks what the NewConnection
// godoc tells callers: Done does not close while the peer's output is open,
// Done does not end a write to a peer that stopped reading, and closing the
// transport does, for each transport that the godoc names.
func TestConnection_CloseTransportEndsBlockedWrite(t *testing.T) {
	transports := []struct {
		name string
		// open returns the writer that the connection writes to and the
		// closer that the caller closes after Done.
		open func(t *testing.T) (io.Writer, io.Closer)
	}{
		{name: "io.Pipe", open: func(t *testing.T) (io.Writer, io.Closer) {
			_, w := io.Pipe() // nobody reads the peer's input
			return w, w
		}},
		{name: "os.Pipe", open: func(t *testing.T) (io.Writer, io.Closer) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			return w, w
		}},
		{name: "TCP", open: func(t *testing.T) (io.Writer, io.Closer) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, err := ln.Accept()
				if err == nil {
					accepted <- conn
				}
			}()
			w, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			peer := <-accepted
			t.Cleanup(func() { _ = peer.Close() })
			// Small buffers, so that bigParams fills them.
			_ = w.(*net.TCPConn).SetWriteBuffer(64 << 10)
			_ = peer.(*net.TCPConn).SetReadBuffer(64 << 10)
			return w, w
		}},
		{name: "child stdin", open: func(t *testing.T) (io.Writer, io.Closer) {
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), childModeEnv+"=idle")
			w, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
			return w, w
		}},
	}
	for _, tc := range transports {
		t.Run(tc.name, func(t *testing.T) {
			w, closer := tc.open(t)
			inR, inW := io.Pipe()
			c := NewConnection(nil, w, inR)
			c.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))

			sent := make(chan error, 1)
			go func() { sent <- c.SendNotification(context.Background(), "n", bigParams()) }()
			time.Sleep(200 * time.Millisecond) // let the write start and block
			select {
			case <-c.Done():
				t.Fatal("Done closed while the peer's output is open")
			default:
			}

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

			_ = closer.Close()
			select {
			case err := <-sent:
				if err == nil {
					t.Fatal("SendNotification returned nil after its write failed")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("SendNotification still blocks after the transport closed")
			}
		})
	}
}
