// Command agent is the ACP agent that the verify-tck gate runs the ACP Test
// Compatibility Kit (acp-tck) against. It is built on the SDK and advertises
// every v1 capability the SDK supports at its schema version, so that the
// TCK's CAPABILITY rows run instead of skipping. It keeps its sessions in
// memory, and the TCK starts a fresh process for each test.
//
// The TCK drives cancellation and subagent rows with prompt texts that
// run.sh passes to the TCK and to this agent alike:
//
//	agent -cancel-prompt A -subagent-prompt B -subagent-cancel-prompt C
//
// A answers with a slow stream that runs until the turn is cancelled, B
// delegates to a subagent that finishes, and C to a cancellable subagent
// that runs until the TCK cancels it. behaviors in turns.go lists every
// prompt the agent knows; any other prompt gets an echo.
package main

import (
	"flag"
	"os"

	"github.com/coder/acp-go-sdk"
)

func main() {
	var p prompts
	flag.StringVar(&p.cancel, "cancel-prompt", "", "prompt text that starts a slow turn which runs until it is cancelled")
	flag.StringVar(&p.subagent, "subagent-prompt", "", "prompt text that delegates to a subagent which finishes")
	flag.StringVar(&p.subagentCancel, "subagent-cancel-prompt", "", "prompt text that delegates to a cancellable subagent which runs until it is cancelled")
	flag.Parse()

	a := newAgent(p)
	conn := acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	a.connect(conn)
	<-conn.Done()
	a.shutdown()
}
