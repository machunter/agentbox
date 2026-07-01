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
	"strconv"
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
	"github.com/burcsahinoglu/agentbox/internal/mcpgdrive"
	"github.com/burcsahinoglu/agentbox/internal/mcpmail"
	"github.com/burcsahinoglu/agentbox/internal/mcpslack"
	"github.com/burcsahinoglu/agentbox/internal/memory"
	"github.com/burcsahinoglu/agentbox/internal/tools"
)

// Default tuning. The model is chosen by AGENTBOX_MODEL (see internal/llm),
// defaulting to Claude Opus 4.8; thinking config is left unset (each provider
// applies sensible defaults).
const (
	// defaultMaxTurns is the safety stop (counted in tool-call rounds) so a loop
	// can't run forever. Overridable via AGENTBOX_MAX_TOOL_CALLS — busy work
	// mailboxes can legitimately need more rounds than a personal one.
	defaultMaxTurns = 50
	appName         = "agentbox"
	userID          = "local"
	sessionID       = "main"

	probeTimeout = 3 * time.Second

	systemPrompt = "You are agentbox, an autonomous agent running inside a sandboxed Docker container. " +
		"You accomplish the user's task by reasoning and by running bash commands with the run_bash tool. " +
		"You also have structured filesystem tools (list_directory, read_file, search_files) scoped to the " +
		"workspace; prefer them for inspecting files, and use run_bash for everything else. " +
		"When email is configured, you have read-only email tools (list_new_emails, list_recent_emails, search_emails, read_email, and list_mailboxes). " +
		"For recurring briefings prefer list_new_emails: it returns only mail you haven't processed before and remembers what it returned, so you don't reprocess the same messages or create duplicate todos. Use list_recent_emails only when you specifically need a time window regardless of what's new. " +
		"Stick to the inbox and the Sent folder: pass mailbox \"Sent\" directly to scan sent mail (it resolves to the provider's real Sent folder automatically) — you do NOT need list_mailboxes to find it. " +
		"Do not browse, list, or read other folders/labels unless the task explicitly requires it; some accounts have many and reading them wastes time. " +
		"Scanning Sent is for checking whether you already replied — e.g. to find a reply that resolves an open todo and then complete_todo it. " +
		"When a calendar is configured, you have read-only calendar tools (list_upcoming_events, events_on_day, search_events); " +
		"when the feed includes your RSVP, declined events are omitted and unconfirmed ones flagged ('not yet accepted'/'tentative'); but some feeds (e.g. Google's secret-iCal export) carry no RSVP at all, so a listed event is NOT proof you accepted it — don't assert attendance, and if it matters, note the calendar may not reflect declines. " +
		"When Slack is configured, you have read-only Slack tools (list_channels, read_channel, read_thread, search_messages); find a channel with list_channels, then read it by name or ID. " +
		"When Google Drive is configured, you have read-only Drive tools (search_drive, read_drive_file, list_recent_files); search for a file, then read it by ID — native Google Docs/Sheets/Slides come back as Markdown/CSV/text. " +
		"Always use these dedicated email and calendar tools for those sources — do NOT fetch mailboxes or calendar/ICS feeds over the network with run_bash (curl, python, perl, etc.). " +
		"If a connector you need isn't available (because it isn't configured), say so plainly and move on; do not improvise with bash or scripting languages. " +
		"You have notes/todo tools (add_todo, list_todos, complete_todo, add_note, search_notes) for capturing and managing the user's todos and notes. " +
		"File a todo only for something that genuinely needs the OWNER's own action. Before adding, list_todos and skip anything already there — if a new item overlaps an existing todo (same person and topic, e.g. an email update and a Slack mention of the same thread), merge it into that one rather than adding a near-duplicate; add_todo also skips near-duplicates and tells you when it did. " +
		"Use the owner's role and priorities (from memory) to judge what belongs: do NOT file spam, cold outreach, newsletters, automated notifications, FYIs that need no reply, or routine administrative/operational tasks clearly below the owner's level (those should be delegated) — record such things as a note if useful, or drop them. When unsure whether something rises to the owner's attention, prefer a note over a todo. " +
		"Work in small, verifiable steps: inspect before you act, and check your work. " +
		"For scripting or computation, use python3 — it's installed; do not probe for or fall back across other languages. " +
		"Use your dedicated tools for their jobs (email, calendar, notes) rather than reimplementing them, and never call a model/LLM API yourself, install system packages, or invoke the agentbox binary. If a task needs a capability you genuinely don't have, say so and stop. " +
		"You have a long-term memory of past sessions; relevant memories are provided automatically, " +
		"and you can search them with the load_memory tool when useful. " +
		"When the task is complete, stop calling tools and give a short, plain summary of what you did and what you found."

	// capturePrompt is the system instruction for the locked-down capture agent
	// (ForCapture): it has only the notes tools and must not attempt anything else.
	capturePrompt = "You are agentbox's capture processor. You are given an image (often a photo of handwritten notes). " +
		"Read all its text and file it: call add_todo for each actionable item and add_note for anything that's a note, idea, or reference. " +
		"You have ONLY the notes tools — no shell, files, email, calendar, or network. Do not attempt anything else. " +
		"If the image has no usable text, do nothing and say so. When done, give a one-line summary of what you filed."

	// notesPrompt is the system instruction for the todo CLI (ForNotes): match
	// the user's request to one open todo and complete it, conservatively.
	notesPrompt = "You manage the user's todos with the notes tools only (no shell, files, email, calendar, or network). " +
		"The user names a todo to mark done, possibly loosely or paraphrased. Call list_todos, then choose the SINGLE open todo that best matches their request and call complete_todo with text that uniquely identifies that todo. " +
		"Be conservative: if nothing clearly matches, or two or more are plausible, complete NOTHING — instead list the close candidates and ask the user to be more specific. Never guess between ambiguous matches. " +
		"End with a one-line confirmation of what you completed, or why you didn't."
)

// Agent holds the dependencies for a run.
type Agent struct {
	runner   *runner.Runner
	sessions session.Service
	mem      *memory.Service // nil when memory is disabled/unavailable
	out      io.Writer
	log      *slog.Logger
	maxTurns int             // tool-call rounds before the safety stop
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

// envInt reads a positive integer from the environment, falling back to def when
// unset, unparseable, or non-positive.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// Option configures how New builds the agent.
type Option func(*options)

type options struct {
	capture bool // restricted profile for processing capture images
	notes   bool // restricted profile for todo/notes CLI operations
}

// notesOnly reports whether a restricted, notes-tools-only agent was requested
// (capture or the todo CLI): no shell, filesystem, email, calendar, memory, or
// tool library.
func (o options) notesOnly() bool { return o.capture || o.notes }

// ToolsDir resolves the persistent tool-library directory: AGENTBOX_TOOLS_DIR
// if set, else ~/.agentbox/tools. Scripts the agent builds are saved here and
// survive across runs.
func ToolsDir() string {
	if d := os.Getenv("AGENTBOX_TOOLS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".agentbox", "tools")
}

// readToolIndex returns the tool library's INDEX.md contents, or "" if absent.
func readToolIndex(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "INDEX.md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// toolsSection is appended to the general agent's instruction: the tool-library
// protocol plus the current index (clipped to bound context). It teaches the
// agent to reuse and grow a persistent set of scripts so it re-derives less.
func toolsSection(dir string) string {
	idx := readToolIndex(dir)
	if idx == "" {
		idx = "(empty — no tools saved yet)"
	}
	return "\n\nYou have a persistent tool library at " + dir + " (also on your PATH), shared across runs. Build it up so you re-derive less over time:\n" +
		"- Before writing code for a task, check the index below and prefer running an existing tool.\n" +
		"- When you write something reusable, save it as a small, parameterized, executable script in that directory and add one line to " + dir + "/INDEX.md (name — purpose — usage).\n" +
		"- Keep tools small, general, and well-named so future runs can reuse them.\n" +
		"Tool library index:\n" + clip(idx)
}

// ForCapture builds a locked-down agent for the capture pipeline: only the
// notes tools (add_todo/add_note/…), no run_bash, filesystem, email, calendar,
// or memory. Capturing should only read an image and file its items; giving it
// a shell let weak models wander (run agentbox recursively, probe the network,
// attempt package installs). This removes that whole surface.
func ForCapture() Option { return func(o *options) { o.capture = true } }

// ForNotes builds the same locked-down, notes-only agent for todo/notes CLI
// operations (e.g. `agentbox done`): the model matches the user's request
// against the open todos and completes the right one, with no other surface.
func ForNotes() Option { return func(o *options) { o.notes = true } }

// New builds an Agent: an ADK agent (model chosen by AGENTBOX_MODEL — Claude or
// Gemini) with the run_bash tool and, when a local embedder is reachable,
// long-term memory. The model's API key is read from the environment. Pass
// ForCapture or ForNotes to build a restricted, notes-only profile.
func New(ctx context.Context, out io.Writer, opts ...Option) (*Agent, error) {
	var o options
	for _, fn := range opts {
		fn(&o)
	}

	cfg := configFromEnv()
	log := dbg.New("agent")

	modelName := llm.ConfiguredModel()
	model, err := llm.New(ctx, modelName)
	if err != nil {
		return nil, fmt.Errorf("init model: %w", err)
	}

	var toolset []tool.Tool
	var toolsets []tool.Toolset
	instruction := systemPrompt
	var mem *memory.Service

	if o.notesOnly() {
		// Notes-only: nothing that lets the model act on the host or network.
		instruction = capturePrompt
		if o.notes {
			instruction = notesPrompt
		}
		if nt := selfMCPToolset(out, "notes tools", "mcp-notes"); nt != nil {
			toolsets = append(toolsets, nt)
		}
	} else {
		bash, err := tools.NewBash()
		if err != nil {
			return nil, fmt.Errorf("init run_bash tool: %w", err)
		}
		toolset = []tool.Tool{bash}

		// Try to bring up local memory. It is an enhancement, not a hard
		// dependency: if the embedder can't be reached, run without it.
		mem = initMemory(ctx, cfg, out)
		if mem != nil {
			toolset = append(toolset, preloadmemorytool.New(), loadmemorytool.New())
		}

		// Connectors served by local MCP servers (agentbox launching itself in a
		// subcommand). Best-effort: skip any that fail to set up.
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
		slackNote := ""
		if slack := initSlackTools(out); slack != nil {
			toolsets = append(toolsets, slack)
			slackNote = slackUserNote() // tell the agent the user's handle, if set
		}
		if gdrive := initGDriveTools(out); gdrive != nil {
			toolsets = append(toolsets, gdrive)
		}

		// Give the general agent its persistent, self-built tool library: append
		// the protocol and the current index so it reuses past work and grows the
		// library over time (the LLM increasingly orchestrates rather than
		// re-derives). Capture stays locked down and does not get this.
		instruction = systemPrompt + slackNote + toolsSection(ToolsDir())
	}

	log.Debug("agent configured",
		"model", modelName,
		"provider", llm.Provider(modelName),
		"capture", o.capture,
		"memory", mem != nil,
		"toolsets", len(toolsets),
		"namespace", cfg.namespace)

	llm, err := llmagent.New(llmagent.Config{
		Name:        appName,
		Description: "An autonomous agent that runs bash commands in a sandboxed container to accomplish a task.",
		Model:       model,
		Instruction: instruction,
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

	return &Agent{
		runner:   r,
		sessions: sessions,
		mem:      mem,
		out:      out,
		log:      log,
		maxTurns: envInt("AGENTBOX_MAX_TOOL_CALLS", defaultMaxTurns),
	}, nil
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

// slackUserNote tells the agent the user's Slack handle (AGENTBOX_SLACK_USER) so
// it can search for messages directed at them precisely. Empty when unset.
func slackUserNote() string {
	u := strings.TrimSpace(os.Getenv("AGENTBOX_SLACK_USER"))
	if u == "" {
		return ""
	}
	return " On Slack, the user is \"" + u + "\" — to find messages directed at them, search for that name/handle (e.g. search_messages \"" + u + "\")."
}

// initSlackTools wires in the read-only Slack MCP server, but only when a Slack
// token is configured — otherwise Slack is silently skipped.
func initSlackTools(out io.Writer) tool.Toolset {
	if !mcpslack.Configured() {
		return nil
	}
	return selfMCPToolset(out, "slack tools", "mcp-slack")
}

// initGDriveTools wires in the read-only Google Drive MCP server, but only when
// Drive OAuth credentials are configured — otherwise Drive is silently skipped.
func initGDriveTools(out io.Writer) tool.Toolset {
	if !mcpgdrive.Configured() {
		return nil
	}
	return selfMCPToolset(out, "google drive tools", "mcp-gdrive")
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
	capped := false
	for ev, err := range a.runner.Run(ctx, userID, sessionID, msg, agent.RunConfig{}) {
		if err != nil {
			a.log.Debug("run error", "turn", turns, "err", err)
			return fmt.Errorf("run: %w", err)
		}
		if a.printEvent(ev) {
			turns++
			if turns >= a.maxTurns {
				fmt.Fprintf(a.out, "\n[reached the %d tool-call limit; wrapping up]\n", a.maxTurns)
				a.log.Debug("run stopped at turn cap", "maxTurns", a.maxTurns)
				capped = true
				break
			}
		}
	}

	// If we stopped because of the cap, the model never got to write its closing
	// summary, so a digest/journal would be empty. Ask it to summarize now with
	// what it already has, so a capped run still produces output.
	if capped {
		a.wrapUp(ctx)
	}

	a.log.Debug("run complete", "tool_call_rounds", turns, "capped", capped)
	a.persistMemory()
	return nil
}

// wrapUp requests a final summary after the tool-call cap was hit, so a capped
// run still yields prose (its Answer) instead of nothing. Tools are discouraged
// and the extra turns are tightly bounded.
func (a *Agent) wrapUp(ctx context.Context) {
	a.log.Debug("wrap-up: requesting final summary after cap")
	msg := genai.NewContentFromText(
		"You've reached the tool-call limit, so stop here. Do NOT call any more tools. "+
			"Give me your summary now based on what you've already gathered.", genai.RoleUser)
	a.answer.Reset()
	bounded := 0
	for ev, err := range a.runner.Run(ctx, userID, sessionID, msg, agent.RunConfig{}) {
		if err != nil {
			// Surface at warn: a failed wrap-up (e.g. cancelled ctx) otherwise
			// looks identical to a clean capped summary in the journal.
			a.log.Warn("wrap-up failed; summary may be incomplete", "err", err)
			return
		}
		a.printEvent(ev)
		if bounded++; bounded >= 4 {
			break
		}
	}
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
