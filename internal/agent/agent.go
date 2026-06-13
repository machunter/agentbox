// Package agent runs the core perceive -> think -> act loop on top of Google's
// Agent Development Kit (ADK). It builds an ADK agent backed by Claude (via the
// Anthropic model adapter), gives it the run_bash tool, and drives the runner's
// event stream until the agent produces a final answer.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	adkanthropic "github.com/Alcova-AI/adk-anthropic-go"
	"github.com/anthropics/anthropic-sdk-go"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/burcsahinoglu/agentbox/internal/tools"
)

// Default tuning. Opus 4.8 uses adaptive thinking; the adapter defaults to
// adaptive on adaptive-capable models when no thinking config is set, so we
// leave it unset.
const (
	modelName = anthropic.ModelClaudeOpus4_8
	maxTurns  = 25 // safety stop (counted in tool-call rounds) so a loop can't run forever
	appName   = "agentbox"
	userID    = "local"
	sessionID = "main"

	systemPrompt = "You are agentbox, an autonomous agent running inside a sandboxed Docker container. " +
		"You accomplish the user's task by reasoning and by running bash commands with the run_bash tool. " +
		"Work in small, verifiable steps: inspect before you act, and check your work. " +
		"When the task is complete, stop calling tools and give a short, plain summary of what you did and what you found."
)

// Agent holds the dependencies for a run.
type Agent struct {
	runner *runner.Runner
	out    io.Writer
}

// New builds an Agent: a Claude-backed ADK agent with the run_bash tool, driven
// by an ADK runner with an in-memory session store. The model reads
// ANTHROPIC_API_KEY from the environment.
func New(ctx context.Context, out io.Writer) (*Agent, error) {
	model, err := adkanthropic.NewModel(ctx, modelName, &adkanthropic.Config{
		APIKey: os.Getenv("ANTHROPIC_API_KEY"),
	})
	if err != nil {
		return nil, fmt.Errorf("init model: %w", err)
	}

	bash, err := tools.NewBash()
	if err != nil {
		return nil, fmt.Errorf("init run_bash tool: %w", err)
	}

	llm, err := llmagent.New(llmagent.Config{
		Name:        appName,
		Description: "An autonomous agent that runs bash commands in a sandboxed container to accomplish a task.",
		Model:       model,
		Instruction: systemPrompt,
		Tools:       []tool.Tool{bash},
	})
	if err != nil {
		return nil, fmt.Errorf("init agent: %w", err)
	}

	r, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             llm,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("init runner: %w", err)
	}

	return &Agent{runner: r, out: out}, nil
}

// Run drives the agentic loop for a single task. It returns when the agent
// stops calling tools (or the maxTurns safety cap is hit).
func (a *Agent) Run(ctx context.Context, task string) error {
	msg := genai.NewContentFromText(task, genai.RoleUser)

	turns := 0
	for ev, err := range a.runner.Run(ctx, userID, sessionID, msg, agent.RunConfig{}) {
		if err != nil {
			return fmt.Errorf("run: %w", err)
		}
		if a.printEvent(ev) {
			turns++
			if turns >= maxTurns {
				fmt.Fprintf(a.out, "\n[stopped after %d tool calls]\n", maxTurns)
				return nil
			}
		}
	}
	return nil
}

// printEvent writes an event's assistant text and any tool calls to the output,
// mirroring the original CLI: thinking is not shown, tool results are fed back
// to the model rather than printed. It returns true if the event contained at
// least one tool call (used to count turns against the safety cap).
func (a *Agent) printEvent(ev *session.Event) bool {
	if ev == nil || ev.Content == nil {
		return false
	}
	hasToolCall := false
	for _, part := range ev.Content.Parts {
		switch {
		case part.Thought:
			// Don't surface the model's thinking.
		case part.FunctionCall != nil:
			hasToolCall = true
			fmt.Fprintf(a.out, "\n› %s %s\n", part.FunctionCall.Name, argsJSON(part.FunctionCall.Args))
		case part.Text != "":
			fmt.Fprintln(a.out, part.Text)
		}
	}
	return hasToolCall
}

// argsJSON renders tool-call arguments compactly for the trace line.
func argsJSON(args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(b)
}
