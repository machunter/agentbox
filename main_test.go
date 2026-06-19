package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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
