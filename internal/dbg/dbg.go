// Package dbg provides opt-in verbose debug logging. When AGENTBOX_DEBUG is set
// (1/true/yes/on), loggers write structured trace lines to stderr; otherwise
// they discard everything, so normal runs are unaffected and stdout stays clean
// for the agent's actual output.
//
// Each process reads AGENTBOX_DEBUG independently, so setting it turns on debug
// logging in the main agent and in the connector subprocesses alike (their
// stderr is surfaced by the parent).
package dbg

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

var enabled = truthy(os.Getenv("AGENTBOX_DEBUG"))

// Enabled reports whether debug logging is on.
func Enabled() bool { return enabled }

// New returns a logger tagged with a component name. When debug is off it
// discards everything.
func New(component string) *slog.Logger {
	if !enabled {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h).With("comp", component)
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
