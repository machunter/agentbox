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
	"strings"
	"syscall"

	"github.com/burcsahinoglu/agentbox/internal/agent"
)

func main() {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "error: ANTHROPIC_API_KEY is not set")
		os.Exit(2)
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
