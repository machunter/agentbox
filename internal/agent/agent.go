// Package agent runs the core perceive -> think -> act loop: it sends the
// conversation to Claude, executes any tools Claude asks for, feeds the
// results back, and repeats until Claude produces a final answer.
package agent

import (
	"context"
	"fmt"
	"io"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/burcsahinoglu/agentbox/internal/tools"
)

// Default tuning. Opus 4.8 uses adaptive thinking (Claude decides how much to
// think per turn); effort defaults to "high" so it is left unset.
const (
	model        = anthropic.ModelClaudeOpus4_8
	maxTokens    = 16000
	maxTurns     = 25 // safety stop so a misbehaving loop can't run forever
	systemPrompt = "You are agentbox, an autonomous agent running inside a sandboxed Docker container. " +
		"You accomplish the user's task by reasoning and by running bash commands with the run_bash tool. " +
		"Work in small, verifiable steps: inspect before you act, and check your work. " +
		"When the task is complete, stop calling tools and give a short, plain summary of what you did and what you found."
)

// Agent holds the dependencies for a run.
type Agent struct {
	client anthropic.Client
	out    io.Writer
}

// New builds an Agent. The client reads ANTHROPIC_API_KEY from the
// environment by default.
func New(out io.Writer) *Agent {
	return &Agent{client: anthropic.NewClient(), out: out}
}

// Run drives the agentic loop for a single task and returns when Claude stops
// requesting tools (or maxTurns is hit).
func (a *Agent) Run(ctx context.Context, task string) error {
	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(task)),
	}
	toolset := []anthropic.ToolUnionParam{{OfTool: &tools.Bash}}

	adaptive := anthropic.ThinkingConfigAdaptiveParam{}

	for turn := 1; turn <= maxTurns; turn++ {
		resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     model,
			MaxTokens: maxTokens,
			System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
			Thinking:  anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive},
			Messages:  messages,
			Tools:     toolset,
		})
		if err != nil {
			return fmt.Errorf("turn %d: messages.new: %w", turn, err)
		}

		// Record the assistant turn before acting on its tool calls.
		messages = append(messages, resp.ToParam())

		toolResults := a.handleContent(ctx, resp)

		// No tool calls -> Claude is done.
		if resp.StopReason != anthropic.StopReasonToolUse {
			return nil
		}
		messages = append(messages, anthropic.NewUserMessage(toolResults...))
	}

	fmt.Fprintf(a.out, "\n[stopped after %d turns]\n", maxTurns)
	return nil
}

// handleContent prints assistant text and executes each tool call, returning
// the tool_result blocks to send back on the next turn.
func (a *Agent) handleContent(ctx context.Context, resp *anthropic.Message) []anthropic.ContentBlockParamUnion {
	var results []anthropic.ContentBlockParamUnion
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			fmt.Fprintln(a.out, v.Text)
		case anthropic.ToolUseBlock:
			fmt.Fprintf(a.out, "\n› run_bash %s\n", string(v.JSON.Input.Raw()))
			output, isErr, err := tools.RunBash(ctx, []byte(v.JSON.Input.Raw()))
			if err != nil {
				output, isErr = err.Error(), true
			}
			results = append(results, anthropic.NewToolResultBlock(block.ID, output, isErr))
		}
	}
	return results
}
