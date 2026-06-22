package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/burcsahinoglu/agentbox/internal/capture"
)

func TestApplyTimezoneAffectsProcessAndChildren(t *testing.T) {
	t.Setenv("AGENTBOX_TIMEZONE", "America/Los_Angeles")
	applyTimezone()

	if got := os.Getenv("TZ"); got != "America/Los_Angeles" {
		t.Errorf("TZ = %q, want America/Los_Angeles", got)
	}
	if !strings.Contains(time.Local.String(), "America/Los_Angeles") {
		t.Errorf("time.Local = %q, want America/Los_Angeles", time.Local)
	}

	// The real bug: a child process (like run_bash's `date`) must inherit the
	// timezone, not run in UTC. `date +%Z` should report a Pacific zone.
	out, err := exec.Command("date", "+%Z").Output()
	if err != nil {
		t.Skipf("date unavailable: %v", err)
	}
	if z := strings.TrimSpace(string(out)); z != "PDT" && z != "PST" {
		t.Errorf("child `date` zone = %q, want PDT/PST (UTC means TZ didn't propagate)", z)
	}
}

func TestPromptResolverOverride(t *testing.T) {
	// No override dir: falls back to the binary default; unknown name -> not found.
	t.Setenv("AGENTBOX_PROMPTS_DIR", "")
	r := promptResolver()
	if p, ok := r("daily-briefing"); !ok || p != dailyBriefingPrompt {
		t.Errorf("default daily-briefing not returned (ok=%v)", ok)
	}
	if _, ok := r("nope"); ok {
		t.Error("unknown task should not resolve without an override")
	}

	// With an override file: it wins over the binary default, and can define a
	// brand-new task's prompt too.
	dir := t.TempDir()
	t.Setenv("AGENTBOX_PROMPTS_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "daily-briefing.md"), []byte("MY CUSTOM BRIEFING"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "custom-task.md"), []byte("DO THE CUSTOM THING"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = promptResolver()
	if p, ok := r("daily-briefing"); !ok || p != "MY CUSTOM BRIEFING" {
		t.Errorf("override not applied: %q ok=%v", p, ok)
	}
	if p, ok := r("custom-task"); !ok || p != "DO THE CUSTOM THING" {
		t.Errorf("new task from override file not resolved: %q ok=%v", p, ok)
	}
}

func TestCaptureExtractPromptOverride(t *testing.T) {
	t.Setenv("AGENTBOX_PROMPTS_DIR", "")
	if captureExtractPrompt() != capture.DefaultExtractPrompt {
		t.Error("without an override, the default capture prompt should be used")
	}
	dir := t.TempDir()
	t.Setenv("AGENTBOX_PROMPTS_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "process-captures.md"), []byte("EXTRACT DIFFERENTLY"), 0o644); err != nil {
		t.Fatal(err)
	}
	if captureExtractPrompt() != "EXTRACT DIFFERENTLY" {
		t.Error("config/prompts/process-captures.md should override the default")
	}
}
