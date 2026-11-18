package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/coder/acp-go-sdk"
)

type exampleClient struct {
	// input carries the lines of os.Stdin. Every permission and elicitation
	// prompt reads from it, so no prompt buffers lines that a later prompt
	// needs.
	input <-chan string
}

var _ acp.Client = (*exampleClient)(nil)

func (e *exampleClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	title := ""
	if params.ToolCall.Title != nil {
		title = *params.ToolCall.Title
	}
	fmt.Printf("\n🔐 Permission requested: %s\n", title)
	if params.ToolCall.RawInput != nil {
		if b, err := json.Marshal(params.ToolCall.RawInput); err == nil {
			fmt.Printf("   %s\n", b)
		}
	}
	fmt.Println("\nOptions:")
	for i, opt := range params.Options {
		fmt.Printf("   %d. %s (%s)\n", i+1, opt.Name, opt.Kind)
	}
	for {
		fmt.Printf("\nChoose an option: ")
		line, ok := <-e.input
		line = strings.TrimSpace(line)
		if line == "" {
			if !ok {
				return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
			}
			continue
		}
		idx := -1
		_, _ = fmt.Sscanf(line, "%d", &idx)
		idx = idx - 1
		if idx >= 0 && idx < len(params.Options) {
			return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: params.Options[idx].OptionId}}}, nil
		}
		fmt.Println("Invalid option. Please try again.")
	}
}

func (e *exampleClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	u := params.Update
	switch {
	case u.AgentMessageChunk != nil:
		c := u.AgentMessageChunk.Content
		if c.Text != nil {
			fmt.Println(c.Text.Text)
		}
	case u.ToolCall != nil:
		fmt.Printf("\n🔧 %s (%s)\n", u.ToolCall.Title, u.ToolCall.Status)
	case u.ToolCallUpdate != nil:
		status := ""
		if u.ToolCallUpdate.Status != nil {
			status = string(*u.ToolCallUpdate.Status)
		}
		fmt.Printf("\n🔧 Tool call `%s` updated: %s\n\n", u.ToolCallUpdate.ToolCallId, status)
	case u.Plan != nil || u.AgentThoughtChunk != nil || u.UserMessageChunk != nil:
		// Keep output compact for other updates
		fmt.Println("[", displayUpdateKind(u), "]")
	}
	return nil
}

func displayUpdateKind(u acp.SessionUpdate) string {
	switch {
	case u.UserMessageChunk != nil:
		return "user_message_chunk"
	case u.AgentMessageChunk != nil:
		return "agent_message_chunk"
	case u.AgentThoughtChunk != nil:
		return "agent_thought_chunk"
	case u.ToolCall != nil:
		return "tool_call"
	case u.ToolCallUpdate != nil:
		return "tool_call_update"
	case u.Plan != nil:
		return "plan"
	default:
		return "unknown"
	}
}

func (e *exampleClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	if !filepath.IsAbs(params.Path) {
		return acp.WriteTextFileResponse{}, fmt.Errorf("path must be absolute: %s", params.Path)
	}
	dir := filepath.Dir(params.Path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return acp.WriteTextFileResponse{}, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(params.Path, []byte(params.Content), 0o644); err != nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("write %s: %w", params.Path, err)
	}
	fmt.Printf("[Client] Wrote %d bytes to %s\n", len(params.Content), params.Path)
	return acp.WriteTextFileResponse{}, nil
}

func (e *exampleClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if !filepath.IsAbs(params.Path) {
		return acp.ReadTextFileResponse{}, fmt.Errorf("path must be absolute: %s", params.Path)
	}
	b, err := os.ReadFile(params.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("read %s: %w", params.Path, err)
	}
	content := string(b)
	// Apply optional line/limit (1-based line index)
	if params.Line != nil || params.Limit != nil {
		lines := strings.Split(content, "\n")
		start := 0
		if params.Line != nil && *params.Line > 0 {
			start = min(max(*params.Line-1, 0), len(lines))
		}
		end := len(lines)
		if params.Limit != nil && *params.Limit > 0 {
			if start+*params.Limit < end {
				end = start + *params.Limit
			}
		}
		content = strings.Join(lines[start:end], "\n")
	}
	fmt.Printf("[Client] ReadTextFile: %s (%d bytes)\n", params.Path, len(content))
	return acp.ReadTextFileResponse{Content: content}, nil
}

// Optional/UNSTABLE terminal methods: implement as no-ops for example
func (e *exampleClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	fmt.Printf("[Client] CreateTerminal: %v\n", params)
	return acp.CreateTerminalResponse{TerminalId: "term-1"}, nil
}

func (e *exampleClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	fmt.Printf("[Client] TerminalOutput: %v\n", params)
	return acp.TerminalOutputResponse{Output: "", Truncated: false}, nil
}

func (e *exampleClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	fmt.Printf("[Client] ReleaseTerminal: %v\n", params)
	return acp.ReleaseTerminalResponse{}, nil
}

func (e *exampleClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	fmt.Printf("[Client] WaitForTerminalExit: %v\n", params)
	return acp.WaitForTerminalExitResponse{}, nil
}

// KillTerminal implements acp.Client.
func (c *exampleClient) KillTerminal(ctx context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	fmt.Printf("[Client] KillTerminal: %v\n", params)
	return acp.KillTerminalResponse{}, nil
}

// CreateElicitation asks the agent's question on the terminal and reads the
// answer from stdin. A form asks for each string field in turn, and Enter
// takes the field's default. A url elicitation asks whether to open the URL.
// End of input cancels. A form with a field that is not a string is declined,
// because this client cannot show it.
func (e *exampleClient) CreateElicitation(ctx context.Context, params acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error) {
	switch {
	case params.Form != nil:
		return e.fillForm(params.Form)
	case params.Url != nil:
		return e.openURL(params.Url)
	default:
		return acp.CreateElicitationResponse{}, acp.NewInvalidParams(map[string]any{"error": "unsupported elicitation mode"})
	}
}

var (
	elicitationCancel  = acp.CreateElicitationResponse{Cancel: &acp.CreateElicitationCancel{Action: "cancel"}}
	elicitationDecline = acp.CreateElicitationResponse{Decline: &acp.CreateElicitationDecline{Action: "decline"}}
)

func (e *exampleClient) fillForm(form *acp.CreateElicitationForm) (acp.CreateElicitationResponse, error) {
	fmt.Printf("\n📝 %s\n", form.Message)
	names := make([]string, 0, len(form.RequestedSchema.Properties))
	for name := range form.RequestedSchema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	content := map[string]any{}
	for _, name := range names {
		// Properties holds each field's schema as decoded JSON. Read it
		// into the SDK's union to see the field's type.
		var field acp.ElicitationPropertySchema
		raw, err := json.Marshal(form.RequestedSchema.Properties[name])
		if err == nil {
			err = json.Unmarshal(raw, &field)
		}
		if err != nil || field.String == nil {
			fmt.Printf("Cannot show the field %q. Declining.\n", name)
			return elicitationDecline, nil
		}
		label, def := name, ""
		if field.String.Title != nil {
			label = *field.String.Title
		}
		if field.String.Default != nil {
			def = *field.String.Default
		}
		for {
			fmt.Printf("%s [%s]: ", label, def)
			line, ok := <-e.input
			if !ok {
				return elicitationCancel, nil
			}
			value := strings.TrimSpace(line)
			if value == "" {
				value = def
			}
			if value == "" && slices.Contains(form.RequestedSchema.Required, name) {
				fmt.Println("A value is required.")
				continue
			}
			if value != "" {
				content[name] = value
			}
			break
		}
	}
	return acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{Action: "accept", Content: content}}, nil
}

func (e *exampleClient) openURL(u *acp.CreateElicitationUrl) (acp.CreateElicitationResponse, error) {
	fmt.Printf("\n🔗 %s\n   %s\n", u.Message, u.Url)
	fmt.Print("Open this URL? [y/N]: ")
	line, ok := <-e.input
	if !ok {
		return elicitationCancel, nil
	}
	if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
		return elicitationDecline, nil
	}
	// A real client opens the URL in a browser here.
	fmt.Printf("[Client] Opening %s\n", u.Url)
	return acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{Action: "accept"}}, nil
}

// CompleteElicitation reports that the agent finished a url elicitation.
func (e *exampleClient) CompleteElicitation(ctx context.Context, params acp.CompleteElicitationNotification) error {
	fmt.Printf("\n✅ Elicitation %s complete\n", params.ElicitationId)
	return nil
}

func main() {
	// If args provided, treat them as agent program + args. Otherwise run the Go agent example.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var cmd *exec.Cmd
	if len(os.Args) > 1 {
		cmd = exec.CommandContext(ctx, os.Args[1], os.Args[2:]...)
	} else {
		// Default: run the Go example agent. Detect relative to this client's location.
		_, filename, _, ok := runtime.Caller(0)
		if !ok {
			fmt.Fprintf(os.Stderr, "failed to determine current file location\n")
			os.Exit(1)
		}

		// Get directory of this client file and find sibling agent directory
		clientDir := filepath.Dir(filename)
		agentPath := filepath.Join(clientDir, "..", "agent")

		if _, err := os.Stat(agentPath); err != nil {
			fmt.Fprintf(os.Stderr, "failed to find agent directory at %s: %v\n", agentPath, err)
			os.Exit(1)
		}

		cmd = exec.CommandContext(ctx, "go", "run", agentPath)
	}
	cmd.Stderr = os.Stderr
	// Set up pipes for stdio
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start agent: %v\n", err)
		os.Exit(1)
	}

	client := &exampleClient{input: readLines(os.Stdin)}
	conn := acp.NewClientSideConnection(client, stdin, stdout)
	conn.SetLogger(slog.Default())

	// Initialize
	initResp, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
			// Accept boolean config options, so the agent may offer them.
			Session: &acp.ClientSessionCapabilities{ConfigOptions: &acp.SessionConfigOptionsCapabilities{
				Boolean: &acp.BooleanConfigOptionCapabilities{},
			}},
			// Answer elicitation/create in both modes, so the agent may ask.
			Elicitation: &acp.ElicitationCapabilities{
				Form: &acp.ElicitationFormCapabilities{},
				Url:  &acp.ElicitationUrlCapabilities{},
			},
		},
	})
	if err != nil {
		if re, ok := err.(*acp.RequestError); ok {
			if b, mErr := json.MarshalIndent(re, "", "  "); mErr == nil {
				fmt.Fprintf(os.Stderr, "[Client] Error: %s\n", string(b))
			} else {
				fmt.Fprintf(os.Stderr, "initialize error (%d): %s\n", re.Code, re.Message)
			}
		} else {
			fmt.Fprintf(os.Stderr, "initialize error: %v\n", err)
		}
		_ = cmd.Process.Kill()
		os.Exit(1)
	}
	fmt.Printf("✅ Connected to agent (protocol v%v)\n", initResp.ProtocolVersion)

	// New session
	newSess, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: mustCwd(), McpServers: []acp.McpServer{}})
	if err != nil {
		if re, ok := err.(*acp.RequestError); ok {
			if b, mErr := json.MarshalIndent(re, "", "  "); mErr == nil {
				fmt.Fprintf(os.Stderr, "[Client] Error: %s\n", string(b))
			} else {
				fmt.Fprintf(os.Stderr, "newSession error (%d): %s\n", re.Code, re.Message)
			}
		} else {
			fmt.Fprintf(os.Stderr, "newSession error: %v\n", err)
		}
		_ = cmd.Process.Kill()
		os.Exit(1)
	}
	fmt.Printf("📝 Created session: %s\n", newSess.SessionId)
	for _, o := range newSess.ConfigOptions {
		switch {
		case o.Select != nil:
			fmt.Printf("⚙️  %s: %s\n", o.Select.Name, o.Select.CurrentValue)
		case o.Boolean != nil:
			fmt.Printf("⚙️  %s: %v\n", o.Boolean.Name, o.Boolean.CurrentValue)
		}
	}
	fmt.Printf("💬 User: Hello, agent!\n\n")
	fmt.Print(" ")

	// Send prompt
	if _, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: newSess.SessionId,
		Prompt:    []acp.ContentBlock{acp.TextBlock("Hello, agent!")},
	}); err != nil {
		if re, ok := err.(*acp.RequestError); ok {
			if b, mErr := json.MarshalIndent(re, "", "  "); mErr == nil {
				fmt.Fprintf(os.Stderr, "[Client] Error: %s\n", string(b))
			} else {
				fmt.Fprintf(os.Stderr, "prompt error (%d): %s\n", re.Code, re.Message)
			}
		} else {
			fmt.Fprintf(os.Stderr, "prompt error: %v\n", err)
		}
	} else {
		fmt.Printf("\n\n✅ Agent completed\n")
	}

	_ = cmd.Process.Kill()
}

// readLines reads r line by line on one goroutine and sends each line on the
// returned channel. It closes the channel at EOF or on a read error.
func readLines(r io.Reader) <-chan string {
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	return lines
}

func mustCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
