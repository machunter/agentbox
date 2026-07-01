// Package tools defines the capabilities the agent can invoke.
//
// Each tool is built with ADK's functiontool helper: a typed handler plus a
// JSON schema describing its arguments to the model. The handler runs the call
// inside the agent's container.
package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// bashTimeout bounds how long a single command may run before it is killed.
const bashTimeout = 60 * time.Second

// maxOutputBytes caps the combined stdout+stderr a single command may buffer.
// A runaway or verbose command (e.g. `yes`) would otherwise grow unbounded for
// the full timeout and risk exhausting container memory.
const maxOutputBytes = 1 << 20 // 1 MiB

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
	cmd.Env = safeEnv() // don't hand the model our API keys / OAuth tokens
	out := &cappedBuffer{max: maxOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = out
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

// secretEnvMarkers name the substrings that flag an environment variable as
// carrying a credential. run_bash executes model-authored commands over
// untrusted input (emails, Slack, calendar invites), so a prompt-injected run
// could otherwise exfiltrate every secret via `env`/`curl`. We strip these
// rather than pass an allowlist so the agent's self-built tools keep whatever
// benign config they rely on.
var secretEnvMarkers = []string{"KEY", "TOKEN", "SECRET", "PASS", "PASSWORD", "CREDENTIAL"}

// safeEnv returns the process environment with credential-bearing variables
// removed, so run_bash can't leak them.
func safeEnv() []string {
	env := os.Environ()
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		upper := strings.ToUpper(name)
		secret := false
		for _, m := range secretEnvMarkers {
			if strings.Contains(upper, m) {
				secret = true
				break
			}
		}
		if !secret {
			kept = append(kept, kv)
		}
	}
	return kept
}

// cappedBuffer is an io.Writer that accumulates up to max bytes, then discards
// the rest (recording that truncation happened). It bounds command output so a
// verbose command can't exhaust memory.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	// Report the full length as written so the command isn't killed with a
	// short-write error; we're intentionally dropping the overflow.
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	if c.truncated {
		return c.buf.String() + "\n[output truncated at 1 MiB]"
	}
	return c.buf.String()
}
