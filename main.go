// Command agentbox is an autonomous AI agent that runs inside a Docker
// container. It takes a task (from CLI args or stdin), then loops with Claude —
// reasoning and running bash commands — until the task is done.
//
//	echo "list the files here and summarize what this project is" | agentbox
//	agentbox "create hello.txt containing the current date"
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/burcsahinoglu/agentbox/internal/agent"
	"github.com/burcsahinoglu/agentbox/internal/capture"
	"github.com/burcsahinoglu/agentbox/internal/journal"
	"github.com/burcsahinoglu/agentbox/internal/llm"
	"github.com/burcsahinoglu/agentbox/internal/mcpcal"
	"github.com/burcsahinoglu/agentbox/internal/mcpfs"
	"github.com/burcsahinoglu/agentbox/internal/mcpmail"
	"github.com/burcsahinoglu/agentbox/internal/mcpnotes"
	"github.com/burcsahinoglu/agentbox/internal/schedule"
)

func main() {
	// Align the process clock — and child processes like run_bash's `date`,
	// which inherit $TZ — with the configured timezone. Without this the
	// container runs in UTC, so the agent's sense of "now" disagrees with the
	// scheduler and it misjudges the time of day (e.g. morning read as evening).
	applyTimezone()

	// Make the persistent tool library available: ensure the directory exists
	// and is on PATH so scripts the agent saves there are runnable by name in
	// later runs (run_bash children inherit this environment).
	ensureToolsDir()

	// Internal subcommand: run as the filesystem MCP server over stdio. agentbox
	// launches itself this way (see internal/agent); it needs no API key, so this
	// dispatch comes first.
	//   agentbox mcp-fs [root]   (root defaults to the working directory)
	if len(os.Args) > 1 && os.Args[1] == "mcp-fs" {
		root := "."
		if len(os.Args) > 2 {
			root = os.Args[2]
		}
		if err := mcpfs.Serve(context.Background(), root); err != nil {
			fmt.Fprintln(os.Stderr, "mcp-fs:", err)
			os.Exit(1)
		}
		return
	}

	// Internal subcommand: run as the read-only email (IMAP) MCP server over
	// stdio. Reads IMAP credentials from the environment; no API key needed.
	if len(os.Args) > 1 && os.Args[1] == "mcp-mail" {
		if err := mcpmail.Serve(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, "mcp-mail:", err)
			os.Exit(1)
		}
		return
	}

	// Diagnostic subcommand: test IMAP reachability (no MCP/agent layers).
	if len(os.Args) > 1 && os.Args[1] == "mail-check" {
		out, err := mcpmail.CheckConnection(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, "mail-check:", err)
			os.Exit(1)
		}
		fmt.Println(out)
		return
	}

	// Internal subcommand: run as the notes/todo MCP server over stdio. Stores
	// local markdown files; no API key needed.
	if len(os.Args) > 1 && os.Args[1] == "mcp-notes" {
		dir := mcpnotes.DefaultDir()
		if len(os.Args) > 2 {
			dir = os.Args[2]
		}
		if err := mcpnotes.Serve(context.Background(), dir); err != nil {
			fmt.Fprintln(os.Stderr, "mcp-notes:", err)
			os.Exit(1)
		}
		return
	}

	// Internal subcommand: run as the read-only calendar (ICS) MCP server over
	// stdio. Reads ICS feed URLs from the environment; no API key needed.
	if len(os.Args) > 1 && os.Args[1] == "mcp-cal" {
		if err := mcpcal.Serve(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, "mcp-cal:", err)
			os.Exit(1)
		}
		return
	}

	// Diagnostic subcommand: test calendar feed reachability (no MCP/agent).
	if len(os.Args) > 1 && os.Args[1] == "cal-check" {
		out, err := mcpcal.CheckConnection(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, "cal-check:", err)
			os.Exit(1)
		}
		fmt.Println(out)
		return
	}

	// Preflight: the chosen model (AGENTBOX_MODEL) must have its API key set.
	if err := llm.RequireKey(llm.ConfiguredModel()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	// Process the capture inbox: read dropped photos and file their todos/notes.
	if len(os.Args) > 1 && os.Args[1] == "process-captures" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if _, err := processCaptures(ctx, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	// Long-lived scheduler: run configured tasks on their cron schedules.
	//   agentbox serve              (daemon)
	//   agentbox run-task <name>    (run one configured task now)
	if len(os.Args) > 1 && (os.Args[1] == "serve" || os.Args[1] == "run-task") {
		runScheduler(os.Args[1], os.Args[2:])
		return
	}

	task, err := readTask()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if task == "" {
		fmt.Fprintln(os.Stderr, "usage: agentbox \"<task>\"   (or pipe the task on stdin)")
		os.Exit(2)
	}

	// Cancel the run cleanly on Ctrl-C / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ag, err := agent.New(ctx, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := ag.Run(ctx, task); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		os.Exit(1)
	}
}

// runScheduler loads the schedule config and either runs the daemon (`serve`)
// or runs a single named task once (`run-task <name>`).
func runScheduler(mode string, args []string) {
	path := os.Getenv("AGENTBOX_SCHEDULE")
	if path == "" {
		fmt.Fprintln(os.Stderr, "error: AGENTBOX_SCHEDULE is not set (path to the schedule YAML)")
		os.Exit(2)
	}
	cfg, err := schedule.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Each scheduled run gets a fresh agent (and so a fresh session).
	factory := func(ctx context.Context, out io.Writer) (schedule.Agent, error) {
		return agent.New(ctx, out)
	}
	// Built-in commands a task can run instead of an agent prompt.
	commands := map[string]schedule.CommandFunc{
		"process-captures": processCaptures,
	}
	// Daily-output journal: each task's result is appended to a dated markdown
	// file (the assistant's delivery channel without SMTP).
	tz := agentTimezone()
	jnl := journal.New(journalDir(), tz)
	sched := schedule.New(cfg, os.Stdout, factory, commands, jnl, tz)

	switch mode {
	case "serve":
		if err := sched.Serve(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "run-task":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "usage: agentbox run-task <name>")
			os.Exit(2)
		}
		if err := sched.RunOnce(ctx, args[0]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}
}

// captureDir returns the capture-inbox directory: AGENTBOX_CAPTURE_DIR if set,
// otherwise "captures" under the working directory (the mounted workspace).
func captureDir() string {
	if d := os.Getenv("AGENTBOX_CAPTURE_DIR"); d != "" {
		return d
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return filepath.Join(wd, "captures")
}

// journalDir returns where the daily output files live: AGENTBOX_JOURNAL_DIR if
// set, else "journal" under the working directory.
func journalDir() string {
	if d := os.Getenv("AGENTBOX_JOURNAL_DIR"); d != "" {
		return d
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return filepath.Join(wd, "journal")
}

// agentTimezone resolves AGENTBOX_TIMEZONE (default UTC).
func agentTimezone() *time.Location {
	if tz := os.Getenv("AGENTBOX_TIMEZONE"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			return l
		}
	}
	return time.UTC
}

// ensureToolsDir creates the persistent tool-library directory and appends it
// to PATH, so the agent can save scripts there and invoke them by name in later
// runs. Child processes (run_bash) inherit the modified PATH.
func ensureToolsDir() {
	dir := agent.ToolsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return // best-effort; the agent can still create it when it saves a tool
	}
	os.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+dir)
}

// applyTimezone makes the configured timezone the process default, so log
// timestamps, time.Now(), and child processes (run_bash's `date` inherits $TZ)
// all agree with the scheduler. No-op when AGENTBOX_TIMEZONE is unset or
// invalid (the container's default — typically UTC — then applies).
func applyTimezone() {
	tz := os.Getenv("AGENTBOX_TIMEZONE")
	if tz == "" {
		return
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return
	}
	time.Local = loc
	_ = os.Setenv("TZ", tz)
}

// processCaptures runs the agent over each image in the capture inbox. Full
// per-image output goes to out; it returns a one-line digest for the journal
// only when it actually filed something (so the common no-op run is silent).
func processCaptures(ctx context.Context, out io.Writer) (string, error) {
	factory := func(ctx context.Context, out io.Writer) (capture.Agent, error) {
		return agent.New(ctx, out, agent.ForCapture()) // notes-only, no shell/network
	}
	n, err := capture.Process(ctx, captureDir(), out, factory)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(out, "\ncapture: processed %d image(s)\n", n)
	if n == 0 {
		return "", nil
	}
	return fmt.Sprintf("Filed todos/notes from %d capture photo(s).", n), nil
}

// readTask takes the task from CLI args if present, otherwise from stdin.
func readTask() (string, error) {
	if len(os.Args) > 1 {
		return strings.TrimSpace(strings.Join(os.Args[1:], " ")), nil
	}
	// Only read stdin when it is piped, so an interactive run with no args
	// falls through to the usage message instead of blocking.
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
