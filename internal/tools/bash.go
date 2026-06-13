// Package tools defines the capabilities the agent can invoke.
//
// Each tool is built with ADK's functiontool helper: a typed handler plus a
// JSON schema describing its arguments to the model. The handler runs the call
// inside the agent's container.
package tools

import (
	"bytes"
	"context"
	"os/exec"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// bashTimeout bounds how long a single command may run before it is killed.
const bashTimeout = 60 * time.Second

// BashArgs is the input the model produces for a run_bash call.
type BashArgs struct {
	Command string `json:"command"`
}

// BashResult is fed back to the model. Command failures are reported in-band
// (via ExitError) rather than as a Go error, so the model can read the failure
// and adapt instead of aborting the whole run.
type BashResult struct {
	Output    string `json:"output"`
	ExitError string `json:"exit_error,omitempty"`
}

// NewBash builds the run_bash tool. It runs a shell command inside the agent's
// own environment — a sandboxed Docker container — so a general bash tool is a
// reasonable trade of breadth for blast radius.
func NewBash() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "run_bash",
		Description: "Run a bash command inside the agent's container and return its combined stdout and stderr. Use this to inspect the filesystem, run programs, and accomplish the task. Commands run from the working directory and time out after 60 seconds.",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"command": {
					Type:        "string",
					Description: `The bash command to execute, e.g. "ls -la" or "cat go.mod".`,
				},
			},
			Required: []string{"command"},
		},
	}, runBash)
}

// runBash executes a run_bash tool call. The tool context is itself a
// context.Context, so it carries cancellation from the outer run.
func runBash(tc agent.ToolContext, args BashArgs) (BashResult, error) {
	if args.Command == "" {
		return BashResult{ExitError: "empty command"}, nil
	}

	ctx, cancel := context.WithTimeout(tc, bashTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", args.Command)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()

	res := BashResult{Output: out.String()}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.ExitError = "command timed out after 60s"
	case err != nil:
		res.ExitError = err.Error()
	case res.Output == "":
		res.Output = "[command produced no output]"
	}
	return res, nil
}
