package schedule

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	s := New(cfg, io.Discard, fakeFactory(&prompts), nil)

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
		"process-captures": func(_ context.Context, _ io.Writer) error { ran = true; return nil },
	}
	s := New(cfg, io.Discard, nil, commands)

	if err := s.RunOnce(context.Background(), "captures"); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !ran {
		t.Error("command task did not invoke the registered command")
	}
}

func TestServeStopsOnCancel(t *testing.T) {
	cfg := &Config{Tasks: []Task{{Name: "a", Schedule: "@daily", Prompt: "p"}}}
	var prompts []string
	s := New(cfg, io.Discard, fakeFactory(&prompts), nil)

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
