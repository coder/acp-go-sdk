package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/coder/acp-go-sdk"
)

// mcpProtocolVersion is the MCP revision the agent asks a server for.
const mcpProtocolVersion = "2025-06-18"

// startMCP starts the session's stdio MCP servers and connects to each, as
// a session with MCP servers asks. It does not wait for the handshakes, so a
// slow server never holds up the session. The agent advertises no http, sse
// or acp transport, so it skips those servers.
func (a *agent) startMCP(s *session, servers []acp.McpServer) {
	a.mu.Lock()
	cwd := s.cwd
	a.mu.Unlock()
	var started []*mcpServer
	for _, srv := range servers {
		if srv.Stdio == nil {
			continue
		}
		m, err := startStdio(*srv.Stdio, cwd)
		if err != nil {
			// Name the server, never its env: values can be secrets.
			fmt.Fprintf(os.Stderr, "tck agent: mcp server %q: %v\n", srv.Stdio.Name, err)
			continue
		}
		started = append(started, m)
	}
	a.mu.Lock()
	s.stopMCP()
	s.mcp = started
	a.mu.Unlock()
}

// mcpStopGrace is how long a stopped MCP server has to exit after its stdin
// closes before the agent kills it.
const mcpStopGrace = 2 * time.Second

// mcpServer is a running stdio MCP server.
type mcpServer struct {
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	exited chan struct{} // closed once cmd.Wait returns
}

// stop closes the server's stdin, which ends a stdio server, and kills the
// server if it has not exited within mcpStopGrace. It returns a channel that
// closes once the server has exited.
func (m *mcpServer) stop() <-chan struct{} {
	_ = m.stdin.Close()
	go func() {
		select {
		case <-m.exited:
		case <-time.After(mcpStopGrace):
			_ = m.cmd.Process.Kill()
		}
	}()
	return m.exited
}

// startStdio launches a stdio MCP server with its env as environment
// variables, never as arguments, and starts the MCP handshake.
func startStdio(srv acp.McpServerStdio, cwd string) (*mcpServer, error) {
	cmd := exec.Command(srv.Command, srv.Args...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	for _, e := range srv.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	m := &mcpServer{stdin: stdin, cmd: cmd, exited: make(chan struct{})}
	go func() {
		handshake(stdin, stdout, srv.Name)
		_ = cmd.Wait()
		close(m.exited)
	}()
	return m, nil
}

// handshake sends MCP initialize, and notifications/initialized once the
// server answers, then reads the server's output until it exits.
func handshake(stdin io.Writer, stdout io.Reader, name string) {
	enc := json.NewEncoder(stdin)
	err := enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "acp-go-sdk-tck-agent", "version": "0.0.0"},
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tck agent: mcp server %q: send initialize: %v\n", name, err)
		return
	}
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for lines.Scan() {
		var msg struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(lines.Bytes(), &msg) == nil && string(msg.ID) == "1" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
			break
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
}

// stopMCP stops each of the session's MCP servers and returns channels that
// close once each has exited. The caller holds a.mu.
func (s *session) stopMCP() []<-chan struct{} {
	var exited []<-chan struct{}
	for _, m := range s.mcp {
		exited = append(exited, m.stop())
	}
	s.mcp = nil
	return exited
}
