package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendCreatesAndAppends(t *testing.T) {
	dir := t.TempDir()
	j := New(dir, time.UTC)
	day := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)

	if err := j.Append(day, "morning-briefing", "Two emails need a reply."); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(day.Add(4*time.Hour), "process-captures", "processed 2 image(s)"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "2026-06-17.md"))
	if err != nil {
		t.Fatalf("daily file not created: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"# Wednesday, 2026-06-17", // date header, once
		"## 08:00 — morning-briefing",
		"Two emails need a reply.",
		"## 12:00 — process-captures",
		"processed 2 image(s)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("daily file missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "# Wednesday") != 1 {
		t.Errorf("date header should appear exactly once:\n%s", got)
	}
}

func TestAppendSeparateDays(t *testing.T) {
	dir := t.TempDir()
	j := New(dir, time.UTC)
	_ = j.Append(time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC), "t", "day one")
	_ = j.Append(time.Date(2026, 6, 18, 9, 0, 0, 0, time.UTC), "t", "day two")

	for _, name := range []string{"2026-06-17.md", "2026-06-18.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
}

func TestAppendEmptyBody(t *testing.T) {
	dir := t.TempDir()
	j := New(dir, time.UTC)
	if err := j.Append(time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC), "t", "   "); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "2026-06-17.md"))
	if !strings.Contains(string(data), "(no output)") {
		t.Errorf("empty body should render (no output):\n%s", data)
	}
}
