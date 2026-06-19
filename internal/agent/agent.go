// Package agent runs the core perceive -> think -> act loop on top of Google's
// Agent Development Kit (ADK). It builds an ADK agent backed by Claude (via the
// Anthropic model adapter), gives it the run_bash tool plus local long-term
// memory, and drives the runner's event stream until the agent produces a final
// answer.
//
// Memory is local and optional: it is backed by an embedded vector store and a
// local Ollama embedder (see internal/memory). At startup the agent probes the
// embedder; if it is unreachable, the agent logs a notice and runs without
// memory rather than failing.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/loadmemorytool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/adk/tool/preloadmemorytool"
	"google.golang.org/genai"

	"github.com/burcsahinoglu/agentbox/internal/dbg"
	"github.com/burcsahinoglu/agentbox/internal/llm"
	"github.com/burcsahinoglu/agentbox/internal/mcpcal"
	"github.com/burcsahinoglu/agentbox/internal/mcpmail"
	"github.com/burcsahinoglu/agentbox/internal/memory"
	"github.com/burcsahinoglu/agentbox/internal/tools"
)

// Default tuning. The model is chosen by AGENTBOX_MODEL (see internal/llm),
// defaulting to Claude Opus 4.8; thinking config is left unset (each provider
// applies sensible defaults).
const (
	maxTurns  = 25 // safety stop (counted in tool-call rounds) so a loop can't run forever
	appName   = "agentbox"
	userID    = "local"
	sessionID = "main"

	probeTimeout = 3 * time.Second

	systemPrompt = "You are agentbox, an autonomous agent running inside a sandboxed Docker container. " +
		"You accomplish the user's task by reasoning and by running bash commands with the run_bash tool. " +
		"You also have structured filesystem tools (list_directory, read_file, search_files) scoped to the " +
		"workspace; prefer them for inspecting files, and use run_bash for everything else. " +
		"When email is configured, you have read-only email tools (list_mailboxes, list_recent_emails, search_emails, read_email); " +
		"mailbox names like \"Sent\", \"Drafts\", or \"Trash\" resolve to the provider's real folder automatically, so you can scan sent mail to check whether you already replied — e.g. to find a reply that resolves an open todo and then complete_todo it. " +
		"When a calendar is configured, you have read-only calendar tools (list_upcoming_events, events_on_day, search_events). " +
		"You have notes/todo tools (add_todo, list_todos, complete_todo, add_note, search_notes) for capturing and managing the user's todos and notes. " +
		"Work in small, verifiable steps: inspect before you act, and check your work. " +
		"You have a long-term memory of past sessions; relevant memories are provided automatically, " +
		"and you can search them with the load_memory tool when useful. " +
		"When the task is complete, stop calling tools and give a short, plain summary of what you did and what you found."
)

// Agent holds the dependencies for a run.
type Agent struct {
	runner   *runner.Runner
	sessions session.Service
	mem      *memory.Service // nil when memory is disabled/unavailable
	out      io.Writer
	log      *slog.Logger
	answer   strings.Builder // assistant prose from the current run (for journaling)
}

// Answer returns the assistant's closing summary from the most recent run: the
// text it produced after its last tool call, with no step-by-step narration or
// tool-call traces — suitable for a digest/journal.
func (a *Agent) Answer() string { return strings.TrimSpace(a.answer.String()) }

// config holds deployment-level settings, read from the environment.
type config struct {
	namespace  string // isolates memory per deployment, e.g. "personal" vs "work"
	memoryDir  string // persistent vector-store directory
	ollamaURL  string // Ollama base URL; empty uses chromem-go's default
	embedModel string // embedding model name
}

func configFromEnv() config {
	c := config{
		namespace:  envOr("AGENTBOX_NAMESPACE", "default"),
		memoryDir:  os.Getenv("AGENTBOX_MEMORY_DIR"),
		ollamaURL:  os.Getenv("AGENTBOX_OLLAMA_URL"),
		embedModel: envOr("AGENTBOX_EMBED_MODEL", memory.DefaultOllamaModel),
	}
	if c.memoryDir == "" {
		base, err := os.UserHomeDir()
		if err != nil {
			base = "."
		}
		c.memoryDir = filepath.Join(base, ".agentbox", "memory")
	}
	return c
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// New builds an Agent: an ADK agent (model chosen by AGENTBOX_MODEL — Claude or
// Gemini) with the run_bash tool and, when a local embedder is reachable,
// long-term memory. The model's API key is read from the environment.
func New(ctx context.Context, out io.Writer) (*Agent, error) {
	cfg := configFromEnv()
	log := dbg.New("agent")

	modelName := llm.ConfiguredModel()
	model, err := llm.New(ctx, modelName)
	if err != nil {
		return nil, fmt.Errorf("init model: %w", err)
	}

	bash, err := tools.NewBash()
	if err != nil {
		return nil, fmt.Errorf("init run_bash tool: %w", err)
	}
	toolset := []tool.Tool{bash}

	// Try to bring up local memory. It is an enhancement, not a hard
	// dependency: if the embedder can't be reached, run without it.
	mem := initMemory(ctx, cfg, out)
	if mem != nil {
		toolset = append(toolset, preloadmemorytool.New(), loadmemorytool.New())
	}

	// Connectors served by local MCP servers (agentbox launching itself in a
	// subcommand). Best-effort: skip any that fail to set up.
	var toolsets []tool.Toolset
	if fs := initFileTools(out); fs != nil {
		toolsets = append(toolsets, fs)
	}
	if nt := selfMCPToolset(out, "notes tools", "mcp-notes"); nt != nil {
		toolsets = append(toolsets, nt)
	}
	if mail := initMailTools(out); mail != nil {
		toolsets = append(toolsets, mail)
	}
	if cal := initCalendarTools(out); cal != nil {
		toolsets = append(toolsets, cal)
	}

	log.Debug("agent configured",
		"model", modelName,
		"provider", llm.Provider(modelName),
		"memory", mem != nil,
		"toolsets", len(toolsets),
		"namespace", cfg.namespace)

	llm, err := llmagent.New(llmagent.Config{
		Name:        appName,
		Description: "An autonomous agent that runs bash commands in a sandboxed container to accomplish a task.",
		Model:       model,
		Instruction: systemPrompt,
		Tools:       toolset,
		Toolsets:    toolsets,
	})
	if err != nil {
		return nil, fmt.Errorf("init agent: %w", err)
	}

	sessions := session.InMemoryService()
	runnerCfg := runner.Config{
		AppName:           appName,
		Agent:             llm,
		SessionService:    sessions,
		AutoCreateSession: true,
	}
	if mem != nil {
		runnerCfg.MemoryService = mem
	}

	r, err := runner.New(runnerCfg)
	if err != nil {
		return nil, fmt.Errorf("init runner: %w", err)
	}

	return &Agent{runner: r, sessions: sessions, mem: mem, out: out, log: log}, nil
}

// selfMCPToolset launches agentbox in a subcommand as a local MCP server and
// returns a toolset connected to it over stdio. Best-effort — returns nil (with
// a notice) on failure so the agent still runs with its other tools.
func selfMCPToolset(out io.Writer, label string, args ...string) tool.Toolset {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, args...)
	cmd.Stderr = os.Stderr // surface server diagnostics

	ts, err := mcptoolset.New(mcptoolset.Config{
		Transport: &mcp.CommandTransport{Command: cmd},
	})
	if err != nil {
		fmt.Fprintf(out, "[%s: disabled — %v]\n", label, err)
		return nil
	}
	return ts
}

// initFileTools wires in the local filesystem MCP server, jailed to the working
// directory.
func initFileTools(out io.Writer) tool.Toolset {
	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	return selfMCPToolset(out, "file tools", "mcp-fs", root)
}

// initMailTools wires in the read-only email (IMAP) MCP server, but only when
// IMAP credentials are configured — otherwise email is silently skipped.
func initMailTools(out io.Writer) tool.Toolset {
	if !mcpmail.Configured() {
		return nil
	}
	return selfMCPToolset(out, "email tools", "mcp-mail")
}

// initCalendarTools wires in the read-only calendar (ICS) MCP server, but only
// when at least one calendar feed is configured.
func initCalendarTools(out io.Writer) tool.Toolset {
	if !mcpcal.Configured() {
		return nil
	}
	return selfMCPToolset(out, "calendar tools", "mcp-cal")
}

// initMemory builds the local memory service and probes the embedder. It
// returns nil (and prints a one-line notice) when memory can't be brought up,
// so the agent runs without it rather than failing.
func initMemory(ctx context.Context, cfg config, out io.Writer) *memory.Service {
	embedder := memory.NewOllamaEmbedder(cfg.embedModel, cfg.ollamaURL)

	mem, err := memory.New(memory.Config{
		Namespace: cfg.namespace,
		DBPath:    cfg.memoryDir,
		Embedder:  embedder,
	})
	if err != nil {
		fmt.Fprintf(out, "[memory: disabled — %v]\n", err)
		return nil
	}

	// Probe the embedder so a non-empty store doesn't fail mid-run when Ollama
	// is down. A tiny embed validates both reachability and that the model is
	// pulled.
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if _, err := embedder.EmbedQuery(pctx, "ping"); err != nil {
		fmt.Fprintf(out, "[memory: disabled — embedder %q unreachable: %v]\n", cfg.embedModel, err)
		return nil
	}
	return mem
}

// Run drives the agentic loop for a single text task.
func (a *Agent) Run(ctx context.Context, task string) error {
	return a.runContent(ctx, genai.NewContentFromText(task, genai.RoleUser))
}

// RunWithImage drives the loop for a prompt plus an image — used for vision
// capture (e.g. a photo of handwritten notes). The Anthropic model adapter
// converts the inline image to a vision block.
func (a *Agent) RunWithImage(ctx context.Context, prompt string, image []byte, mimeType string) error {
	parts := []*genai.Part{
		genai.NewPartFromText(prompt),
		genai.NewPartFromBytes(image, mimeType),
	}
	return a.runContent(ctx, genai.NewContentFromParts(parts, genai.RoleUser))
}

// runContent runs the agentic loop on a prepared message. It returns when the
// agent stops calling tools (or the maxTurns safety cap is hit), then persists
// the session to long-term memory.
func (a *Agent) runContent(ctx context.Context, msg *genai.Content) error {
	a.answer.Reset()
	a.log.Debug("run start", "input", describeContent(msg))

	turns := 0
	for ev, err := range a.runner.Run(ctx, userID, sessionID, msg, agent.RunConfig{}) {
		if err != nil {
			a.log.Debug("run error", "turn", turns, "err", err)
			return fmt.Errorf("run: %w", err)
		}
		if a.printEvent(ev) {
			turns++
			if turns >= maxTurns {
				fmt.Fprintf(a.out, "\n[stopped after %d tool calls]\n", maxTurns)
				a.log.Debug("run stopped at turn cap", "maxTurns", maxTurns)
				break
			}
		}
	}

	a.log.Debug("run complete", "tool_call_rounds", turns)
	a.persistMemory()
	return nil
}

// describeContent summarizes a message for a trace: its text and any non-text
// parts (e.g. an inline image and its MIME type).
func describeContent(msg *genai.Content) string {
	if msg == nil {
		return "(nil)"
	}
	var bits []string
	for _, p := range msg.Parts {
		switch {
		case p == nil:
			continue
		case p.Text != "":
			bits = append(bits, fmt.Sprintf("text(%d chars)", len(p.Text)))
		case p.InlineData != nil:
			bits = append(bits, fmt.Sprintf("image(%s, %d bytes)", p.InlineData.MIMEType, len(p.InlineData.Data)))
		default:
			bits = append(bits, "part")
		}
	}
	return strings.Join(bits, ", ")
}

// persistMemory stores the just-finished session in long-term memory. Failures
// are non-fatal: memory is best-effort, and a run shouldn't fail because it
// couldn't be remembered. It uses a detached context so persistence still runs
// if the original was cancelled (e.g. Ctrl-C ended the loop).
func (a *Agent) persistMemory() {
	if a.mem == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	getResp, err := a.sessions.Get(ctx, &session.GetRequest{AppName: appName, UserID: userID, SessionID: sessionID})
	if err != nil || getResp.Session == nil {
		return
	}
	if err := a.mem.AddSessionToMemory(ctx, getResp.Session); err != nil {
		fmt.Fprintf(a.out, "\n[memory: could not persist session: %v]\n", err)
	}
}

// printEvent writes an event's assistant text and any tool calls to the output,
// mirroring the original CLI: thinking is not shown, tool results are fed back
// to the model rather than printed. With debug logging on, it also traces tool
// calls AND their results (otherwise invisible), thinking, and text to stderr.
// It returns true if the event contained at least one tool call (used to count
// turns against the safety cap).
func (a *Agent) printEvent(ev *session.Event) bool {
	if ev == nil || ev.Content == nil {
		return false
	}
	hasToolCall := false
	for _, part := range ev.Content.Parts {
		switch {
		case part.Thought:
			if part.Text != "" {
				a.log.Debug("model thinking", "author", ev.Author, "text", clip(part.Text))
			}
		case part.FunctionCall != nil:
			hasToolCall = true
			// Discard any prose accumulated so far: it was step-by-step
			// narration before a tool call. Only text after the *last* tool
			// call (the closing summary) should survive in Answer().
			a.answer.Reset()
			fmt.Fprintf(a.out, "\n› %s %s\n", part.FunctionCall.Name, argsJSON(part.FunctionCall.Args))
			a.log.Debug("tool call", "name", part.FunctionCall.Name, "args", argsJSON(part.FunctionCall.Args))
		case part.FunctionResponse != nil:
			// Tool output — fed back to the model, not printed to stdout, but
			// invaluable in a debug trace.
			a.log.Debug("tool result", "name", part.FunctionResponse.Name, "response", clip(argsJSON(part.FunctionResponse.Response)))
		case part.Text != "":
			fmt.Fprintln(a.out, part.Text)
			a.answer.WriteString(part.Text)
			a.answer.WriteByte('\n')
			a.log.Debug("model text", "author", ev.Author, "text", clip(part.Text))
		}
	}
	return hasToolCall
}

// argsJSON renders a map compactly as JSON for traces.
func argsJSON(args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// clip bounds very long values so debug traces stay readable.
func clip(s string) string {
	const max = 4000
	if len(s) > max {
		return s[:max] + fmt.Sprintf("…(+%d chars)", len(s)-max)
	}
	return s
}
