// Package tools defines the capabilities the agent can invoke.
//
// Each tool is a pair: an Anthropic tool definition (the schema the model
// sees) and a Go function that executes the call inside the container.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// bashTimeout bounds how long a single command may run before it is killed.
const bashTimeout = 60 * time.Second

// Bash is the tool definition advertised to the model. It runs a shell
// command inside the agent's own environment — which, in this project, is a
// sandboxed Docker container, so giving the agent a general bash tool is a
// reasonable trade of breadth for blast radius.
var Bash = anthropic.ToolParam{
	Name:        "run_bash",
	Description: anthropic.String("Run a bash command inside the agent's container and return its combined stdout and stderr. Use this to inspect the filesystem, run programs, and accomplish the task. Commands run from the working directory and time out after 60 seconds."),
	InputSchema: anthropic.ToolInputSchemaParam{
		Properties: map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The bash command to execute, e.g. \"ls -la\" or \"cat go.mod\".",
			},
		},
		Required: []string{"command"},
	},
}

// bashInput mirrors the JSON the model produces for a run_bash call.
type bashInput struct {
	Command string `json:"command"`
}

// RunBash executes a run_bash tool call and returns the text to feed back to
// the model. A non-nil error is returned only for malformed input; command
// failures are reported in-band (as text) so the model can read the error and
// adapt rather than aborting the whole loop.
func RunBash(ctx context.Context, rawInput json.RawMessage) (string, bool, error) {
	var in bashInput
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return "", true, fmt.Errorf("invalid run_bash input: %w", err)
	}
	if in.Command == "" {
		return "error: empty command", true, nil
	}

	ctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", in.Command)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()

	combined := out.String()
	if ctx.Err() == context.DeadlineExceeded {
		return combined + "\n[command timed out after 60s]", true, nil
	}
	if err != nil {
		// Surface the failure to the model as a (non-fatal) tool error.
		return fmt.Sprintf("%s\n[exit error: %v]", combined, err), true, nil
	}
	if combined == "" {
		return "[command produced no output]", false, nil
	}
	return combined, false, nil
}
