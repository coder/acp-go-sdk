// Command proxy is an ACP proxy that adds "[proxy] " in front of the text of
// each agent message chunk and passes every other message through unchanged.
// A conductor runs it between a client and an agent, for example:
//
//	agent-client-protocol-conductor agent "go run ./example/proxy" "go run ./example/agent"
//
// It speaks ACP to the conductor on stdin and stdout and logs to stderr.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/coder/acp-go-sdk"
)

func main() {
	conn := acp.NewUnstableProxySideConnection(func(c *acp.UnstableProxySideConnection) acp.UnstableProxyHandlers {
		return acp.UnstableProxyHandlers{FromSuccessor: acp.UnstableProxyFromSuccessor{
			SessionUpdate: func(ctx context.Context, n acp.SessionNotification) error {
				if chunk := n.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
					chunk.Content.Text.Text = "[proxy] " + chunk.Content.Text.Text
				}
				return c.Client().SessionUpdate(ctx, n)
			},
		}}
	}, os.Stdout, os.Stdin)
	conn.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	<-conn.Done()
}
