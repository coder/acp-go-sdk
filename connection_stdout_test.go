//go:build linux || darwin

package acp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func init() {
	childModes["stdout"] = func() { blockedStdoutChild(os.Stdout) }
	childModes["stdout-nonblock"] = func() {
		// A process whose stdout starts in non-blocking mode: os.NewFile
		// then hands the descriptor to the runtime poller.
		if err := syscall.SetNonblock(1, true); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		blockedStdoutChild(os.NewFile(1, "/dev/stdout"))
	}
}

// TestConnection_CloseStdoutEndsBlockedWriteOnlyIfNonBlocking checks what the
// NewConnection godoc says about os.Stdout. A child process serves a
// connection on its stdout, a pipe that nobody reads, and closes stdout after
// Done. In blocking mode, which os/exec gives a child, Close returns nil and
// the write stays blocked until the process exits. In non-blocking mode Close
// ends the write.
func TestConnection_CloseStdoutEndsBlockedWriteOnlyIfNonBlocking(t *testing.T) {
	for _, mode := range []string{"stdout", "stdout-nonblock"} {
		t.Run(mode, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close() // open and never read, so the child's write blocks
			var stderr bytes.Buffer
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), childModeEnv+"="+mode)
			cmd.Stdout = w
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()

			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case err := <-exited:
				if err != nil {
					t.Fatalf("child: %v; stderr: %s", err, stderr.String())
				}
			case <-time.After(30 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("child did not exit; stderr: %s", stderr.String())
			}

			got := stderr.String()
			nonblock := strings.HasPrefix(got, "O_NONBLOCK=true\n")
			if mode == "stdout-nonblock" && !nonblock {
				t.Fatalf("stdout is not in non-blocking mode:\n%s", got)
			}
			after := "blocked after close\n"
			if nonblock {
				after = "write returned after close\n"
			}
			want := fmt.Sprintf("O_NONBLOCK=%t\nblocked before close\nclose: <nil>\n%s", nonblock, after)
			if got != want {
				t.Fatalf("child stderr:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// blockedStdoutChild serves a connection on stdout, blocks a write, closes
// stdout after Done, and reports on stderr the mode of stdout and whether the
// write returned.
func blockedStdoutChild(stdout *os.File) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, 1, syscall.F_GETFL, 0)
	if errno != 0 {
		fmt.Fprintln(os.Stderr, errno)
		return
	}
	fmt.Fprintf(os.Stderr, "O_NONBLOCK=%t\n", flags&syscall.O_NONBLOCK != 0)

	inR, inW := io.Pipe()
	c := NewConnection(nil, stdout, inR)
	c.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	sent := make(chan error, 1)
	go func() { sent <- c.SendNotification(context.Background(), "n", bigParams()) }()
	report := func(stage string) bool {
		select {
		case <-sent:
			fmt.Fprintf(os.Stderr, "write returned %s\n", stage)
			return true
		case <-time.After(time.Second):
			fmt.Fprintf(os.Stderr, "blocked %s\n", stage)
			return false
		}
	}
	if report("before close") {
		return
	}
	_ = inW.Close()
	<-c.Done()
	fmt.Fprintf(os.Stderr, "close: %v\n", stdout.Close())
	report("after close")
}
