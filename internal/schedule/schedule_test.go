package schedule

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/burcsahinoglu/agentbox/internal/journal"
)

type answeringAgent struct{ answer string }

func (a answeringAgent) Run(context.Context, string) error { return nil }
func (a answeringAgent) Answer() string                    { return a.answer }

type fakeAgent struct{ prompts *[]string }

func (f fakeAgent) Run(_ context.Context, task string) error {
	*f.prompts = append(*f.prompts, task)
	return nil
}

func fakeFactory(prompts *[]string) Factory {
	return func(_ context.Context, _ io.Writer) (Agent, error) {
		return fakeAgent{prompts}, nil
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"valid", Config{Tasks: []Task{{Name: "a", Schedule: "0 8 * * *", Prompt: "p"}}}, true},
		{"descriptor", Config{Tasks: []Task{{Name: "a", Schedule: "@daily", Prompt: "p"}}}, true},
		{"command", Config{Tasks: []Task{{Name: "a", Schedule: "@hourly", Command: "process-captures"}}}, true},
		{"empty", Config{}, false},
		{"no name", Config{Tasks: []Task{{Schedule: "@daily", Prompt: "p"}}}, false},
		{"dup name", Config{Tasks: []Task{{Name: "a", Schedule: "@daily", Prompt: "p"}, {Name: "a", Schedule: "@daily", Prompt: "q"}}}, false},
		{"neither prompt nor command", Config{Tasks: []Task{{Name: "a", Schedule: "@daily"}}}, false},
		{"both prompt and command", Config{Tasks: []Task{{Name: "a", Schedule: "@daily", Prompt: "p", Command: "c"}}}, false},
		{"bad cron", Config{Tasks: []Task{{Name: "a", Schedule: "not a cron", Prompt: "p"}}}, false},
	}
	for _, c := range cases {
		err := c.cfg.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.yaml")
	yaml := `tasks:
  - name: morning-briefing
    schedule: "0 8 * * *"
    prompt: "Summarize my unread emails and today's calendar."
  - name: standup
    schedule: "@weekly"
    prompt: "What did I do last week?"
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Tasks) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(cfg.Tasks))
	}
	if tk, ok := cfg.Task("morning-briefing"); !ok || tk.Schedule != "0 8 * * *" {
		t.Errorf("task lookup wrong: %+v ok=%v", tk, ok)
	}
	if _, ok := cfg.Task("missing"); ok {
		t.Error("lookup of missing task should fail")
	}
}

func TestLoadBadFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("loading a missing file should error")
	}
}

func TestRunOnce(t *testing.T) {
	cfg := &Config{Tasks: []Task{{Name: "brief", Schedule: "@daily", Prompt: "do the briefing"}}}
	var prompts []string
	s := New(cfg, io.Discard, fakeFactory(&prompts), nil, nil, time.UTC)

	if err := s.RunOnce(context.Background(), "brief"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(prompts) != 1 || prompts[0] != "do the briefing" {
		t.Fatalf("agent not run with the task prompt: %v", prompts)
	}

	if err := s.RunOnce(context.Background(), "unknown"); err == nil {
		t.Error("RunOnce on an unknown task should error")
	}
}

func TestRunOnceCommand(t *testing.T) {
	cfg := &Config{Tasks: []Task{{Name: "captures", Schedule: "@hourly", Command: "process-captures"}}}
	ran := false
	commands := map[string]CommandFunc{
		"process-captures": func(_ context.Context, _ io.Writer) (string, error) { ran = true; return "", nil },
	}
	s := New(cfg, io.Discard, nil, commands, nil, time.UTC)

	if err := s.RunOnce(context.Background(), "captures"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !ran {
		t.Error("command task did not invoke the registered command")
	}
}

func TestCommandJournalsOnlyNonEmptyDigest(t *testing.T) {
	cases := []struct {
		name      string
		digest    string
		wantFiles int
	}{
		{"no-op run is not journaled", "", 0},
		{"meaningful run is journaled", "Filed todos/notes from 2 capture photo(s).", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			jnl := journal.New(dir, time.UTC)
			cfg := &Config{Tasks: []Task{{Name: "captures", Schedule: "@hourly", Command: "process-captures"}}}
			commands := map[string]CommandFunc{
				"process-captures": func(_ context.Context, _ io.Writer) (string, error) { return c.digest, nil },
			}
			s := New(cfg, io.Discard, nil, commands, jnl, time.UTC)
			if err := s.RunOnce(context.Background(), "captures"); err != nil {
				t.Fatal(err)
			}
			files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
			if len(files) != c.wantFiles {
				t.Fatalf("want %d journal file(s), got %d", c.wantFiles, len(files))
			}
			if c.wantFiles > 0 {
				data, _ := os.ReadFile(files[0])
				if !strings.Contains(string(data), c.digest) {
					t.Errorf("journal missing digest %q:\n%s", c.digest, data)
				}
			}
		})
	}
}

func TestRunOnceJournalsAnswer(t *testing.T) {
	dir := t.TempDir()
	jnl := journal.New(dir, time.UTC)
	cfg := &Config{Tasks: []Task{{Name: "brief", Schedule: "@daily", Prompt: "p"}}}
	factory := func(context.Context, io.Writer) (Agent, error) {
		return answeringAgent{answer: "All clear today."}, nil
	}
	s := New(cfg, io.Discard, factory, nil, jnl, time.UTC)

	if err := s.RunOnce(context.Background(), "brief"); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	if len(files) != 1 {
		t.Fatalf("want 1 journal file, got %d", len(files))
	}
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), "All clear today.") || !strings.Contains(string(data), "brief") {
		t.Errorf("journal missing answer/heading:\n%s", data)
	}
}

func TestServeStopsOnCancel(t *testing.T) {
	cfg := &Config{Tasks: []Task{{Name: "a", Schedule: "@daily", Prompt: "p"}}}
	var prompts []string
	s := New(cfg, io.Discard, fakeFactory(&prompts), nil, nil, time.UTC)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: Serve should schedule, then return promptly

	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

func TestServeUsesConfiguredTimezone(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	cfg := &Config{Tasks: []Task{{Name: "a", Schedule: "0 8 * * *", Prompt: "p"}}}
	var out strings.Builder
	s := New(cfg, &out, fakeFactory(&[]string{}), nil, nil, loc)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Serve(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "America/Los_Angeles") {
		t.Errorf("startup line should report the configured timezone:\n%s", out.String())
	}
}
