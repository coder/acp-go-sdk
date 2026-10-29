// example/client-http connects to a remote ACP agent over the Streamable
// HTTP transport (see github.com/coder/acp-go-sdk/acphttp/client) and
// drives a single prompt turn.
//
// Pair it with ./example/agent-http:
//
//	# terminal 1
//	go run ./example/agent-http -listen 127.0.0.1:7777
//
//	# terminal 2
//	go run ./example/client-http -url http://127.0.0.1:7777/acp
//
// You can also point -url at any other server that speaks the ACP
// Streamable HTTP transport.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
	httpclient "github.com/coder/acp-go-sdk/acphttp/client"
)

// demoClient implements the minimum acp.Client surface needed to drive a
// turn that includes a permission round-trip. File-system and terminal
// methods are stubbed out: the demo agent never calls them.
type demoClient struct {
	auto string // if non-empty, automatically pick this option id
}

var _ acp.Client = (*demoClient)(nil)

func (c *demoClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	title := ""
	if params.ToolCall.Title != nil {
		title = *params.ToolCall.Title
	}
	fmt.Printf("\n🔐 Permission requested: %s\n", title)
	for i, opt := range params.Options {
		fmt.Printf("   %d. %s (%s)\n", i+1, opt.Name, opt.Kind)
	}

	// Non-interactive mode: -auto=<optionId>.
	if c.auto != "" {
		for _, opt := range params.Options {
			if string(opt.OptionId) == c.auto {
				fmt.Printf("(auto-selecting %q)\n", c.auto)
				return acp.RequestPermissionResponse{
					Outcome: acp.RequestPermissionOutcome{
						Selected: &acp.RequestPermissionOutcomeSelected{OptionId: opt.OptionId},
					},
				}, nil
			}
		}
		fmt.Printf("(no option matches -auto=%q; falling back to interactive)\n", c.auto)
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\nChoose an option: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return acp.RequestPermissionResponse{}, err
		}
		idx, perr := strconv.Atoi(strings.TrimSpace(line))
		if perr == nil && idx >= 1 && idx <= len(params.Options) {
			return acp.RequestPermissionResponse{
				Outcome: acp.RequestPermissionOutcome{
					Selected: &acp.RequestPermissionOutcomeSelected{
						OptionId: params.Options[idx-1].OptionId,
					},
				},
			}, nil
		}
		fmt.Println("Invalid option. Try again.")
	}
}

func (c *demoClient) SessionUpdate(_ context.Context, params acp.SessionNotification) error {
	switch {
	case params.Update.AgentMessageChunk != nil:
		if t := params.Update.AgentMessageChunk.Content.Text; t != nil {
			fmt.Println(t.Text)
		}
	case params.Update.ToolCall != nil:
		fmt.Printf("🔧 %s (%s)\n", params.Update.ToolCall.Title, params.Update.ToolCall.Status)
	case params.Update.ToolCallUpdate != nil:
		status := "?"
		if params.Update.ToolCallUpdate.Status != nil {
			status = string(*params.Update.ToolCallUpdate.Status)
		}
		fmt.Printf("🔧 tool %s → %s\n", params.Update.ToolCallUpdate.ToolCallId, status)
	}
	return nil
}

// The remaining acp.Client methods are stubs — the demo agent does not
// invoke them. A real client would implement them to expose its
// filesystem and terminal capabilities to the agent.

func (c *demoClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound(acp.ClientMethodFsReadTextFile)
}

func (c *demoClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound(acp.ClientMethodFsWriteTextFile)
}

func (c *demoClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalCreate)
}

func (c *demoClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalOutput)
}

func (c *demoClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalRelease)
}

func (c *demoClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalWaitForExit)
}

func (c *demoClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalKill)
}

func main() {
	url := flag.String("url", "http://127.0.0.1:7777/acp", "ACP endpoint URL (the /acp path is appended automatically if missing)")
	prompt := flag.String("prompt", "Hello, agent!", "user prompt text to send")
	auto := flag.String("auto", "allow", "option id to auto-select on permission requests; set to \"\" for interactive prompts")
	timeout := flag.Duration("timeout", 60*time.Second, "overall timeout for the prompt turn")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Dial does no network I/O — it just constructs a Transport. The
	// initialize POST fires when we Initialize() below.
	tr, err := httpclient.Dial(ctx, httpclient.Config{
		BaseURL: *url,
		Logger:  logger,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial %s: %v\n", *url, err)
		os.Exit(1)
	}
	defer tr.Close()

	cli := &demoClient{auto: *auto}
	conn := acp.NewClientSideConnection(cli, tr, tr)
	conn.SetLogger(logger)

	initResp, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientInfo:      &acp.Implementation{Name: "acp-go-sdk/example/client-http", Version: "0.1.0"},
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapabilities{},
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize: %v\n", err)
		os.Exit(1)
	}
	name := "<unknown>"
	if initResp.AgentInfo != nil {
		name = initResp.AgentInfo.Name
	}
	fmt.Printf("✅ Connected to %s (protocol v%v)\n", name, initResp.ProtocolVersion)

	sess, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        mustCwd(),
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "new session: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("📝 Session: %s\n", sess.SessionId)
	fmt.Printf("💬 User: %s\n\n", *prompt)

	resp, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock(*prompt)},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "prompt: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n✅ Turn complete (stop_reason=%s)\n", resp.StopReason)
}

func mustCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
