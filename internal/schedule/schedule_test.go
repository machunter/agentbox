package schedule

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cron "github.com/robfig/cron/v3"

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
		{"bare name (built-in by name) is valid", Config{Tasks: []Task{{Name: "daily-briefing", Schedule: "@daily"}}}, true},
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

func TestFiresDaily(t *testing.T) {
	// A Wednesday afternoon, so weekly tasks land on varied weekdays.
	from := time.Date(2026, 6, 17, 14, 0, 0, 0, time.UTC)
	cases := []struct {
		schedule string
		want     bool
	}{
		{"0 8 * * *", true},        // once a day
		{"0 8,13,18 * * *", true},  // three times a day
		{"50 7,12,17 * * *", true}, // captures
		{"*/30 * * * *", true},     // every 30 min
		{"@daily", true},
		{"@hourly", true},
		{"0 17 * * 5", false}, // Fridays only
		{"@weekly", false},
		{"@monthly", false},
		{"0 9 1 * *", false}, // first of the month
	}
	for _, c := range cases {
		sched, err := cron.ParseStandard(c.schedule)
		if err != nil {
			t.Fatalf("parse %q: %v", c.schedule, err)
		}
		if got := firesDaily(sched, from); got != c.want {
			t.Errorf("firesDaily(%q) = %v, want %v", c.schedule, got, c.want)
		}
	}
}

func TestRunDailyOnceSkipsNonDailyAndRunsCommandsFirst(t *testing.T) {
	// Prompt task listed first, command task last — startup should still run the
	// command first, and skip the weekly task entirely.
	cfg := &Config{Tasks: []Task{
		{Name: "briefing", Schedule: "0 8 * * *", Prompt: "daily brief"},
		{Name: "weekly", Schedule: "0 17 * * 5", Prompt: "weekly review"},
		{Name: "captures", Schedule: "50 7,12,17 * * *", Command: "process-captures"},
	}}
	var order []string
	commands := map[string]CommandFunc{
		"process-captures": func(_ context.Context, _ io.Writer) (string, error) {
			order = append(order, "captures")
			return "", nil
		},
	}
	s := New(cfg, io.Discard, fakeFactory(&order), commands, nil, time.UTC)

	s.runDailyOnce(context.Background(), time.UTC)

	want := []string{"captures", "daily brief"} // command before prompt; weekly skipped
	if !slices.Equal(order, want) {
		t.Errorf("startup run order = %v, want %v", order, want)
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
	// Synchronized writer: Serve's background runDailyOnce goroutine writes here
	// concurrently with the assertion below.
	var out syncBuf
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

// syncBuf is a goroutine-safe io.Writer for tests that read output while a
// background goroutine may still be writing.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestBuiltinPromptByName(t *testing.T) {
	// A bare-name task (no prompt/command) runs the registered built-in prompt.
	cfg := &Config{Tasks: []Task{{Name: "daily-briefing", Schedule: "@daily"}}}
	var prompts []string
	s := New(cfg, io.Discard, fakeFactory(&prompts), nil, nil, time.UTC).
		WithPrompts(func(name string) (string, bool) {
			if name == "daily-briefing" {
				return "BRIEF NOW", true
			}
			return "", false
		})
	if err := s.RunOnce(context.Background(), "daily-briefing"); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || prompts[0] != "BRIEF NOW" {
		t.Fatalf("bare-name task didn't run the built-in prompt: %v", prompts)
	}

	// An explicit prompt overrides the built-in.
	cfg2 := &Config{Tasks: []Task{{Name: "daily-briefing", Schedule: "@daily", Prompt: "CUSTOM"}}}
	var p2 []string
	s2 := New(cfg2, io.Discard, fakeFactory(&p2), nil, nil, time.UTC).
		WithPrompts(func(name string) (string, bool) {
			if name == "daily-briefing" {
				return "BRIEF NOW", true
			}
			return "", false
		})
	if err := s2.RunOnce(context.Background(), "daily-briefing"); err != nil {
		t.Fatal(err)
	}
	if len(p2) != 1 || p2[0] != "CUSTOM" {
		t.Fatalf("explicit prompt should override built-in: %v", p2)
	}
}

func TestServeRejectsUnrunnableTask(t *testing.T) {
	// A bare name that matches no built-in command or prompt is rejected up front.
	cfg := &Config{Tasks: []Task{{Name: "mystery", Schedule: "@daily"}}}
	var prompts []string
	s := New(cfg, io.Discard, fakeFactory(&prompts), nil, nil, time.UTC)
	if err := s.Serve(context.Background()); err == nil {
		t.Error("Serve should reject a task that resolves to nothing")
	}
}

func TestStartupCatchUpRunsOncePerDay(t *testing.T) {
	cfg := &Config{Tasks: []Task{{Name: "brief", Schedule: "0 8 * * *", Prompt: "do it"}}}
	path := filepath.Join(t.TempDir(), "runs.json")

	var prompts []string
	s := New(cfg, io.Discard, fakeFactory(&prompts), nil, nil, time.UTC).WithRunLog(path)
	s.runDailyOnce(context.Background(), time.UTC) // first startup: runs
	s.runDailyOnce(context.Background(), time.UTC) // restart same day: skips
	if len(prompts) != 1 {
		t.Fatalf("daily task ran %d times across two startups, want 1", len(prompts))
	}

	// A fresh scheduler reading the persisted run-log also skips (survives restart).
	var p2 []string
	s2 := New(cfg, io.Discard, fakeFactory(&p2), nil, nil, time.UTC).WithRunLog(path)
	s2.runDailyOnce(context.Background(), time.UTC)
	if len(p2) != 0 {
		t.Fatalf("after restart the catch-up ran %d times, want 0 (already ran today)", len(p2))
	}
}

func TestRunLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	r := newRunLog(path)
	if r.ranOn("brief", "2026-06-27") {
		t.Error("fresh run-log should report not-run")
	}
	r.record("brief", "2026-06-27")
	if !r.ranOn("brief", "2026-06-27") {
		t.Error("should report run after record")
	}
	if r.ranOn("brief", "2026-06-28") {
		t.Error("different date should report not-run")
	}
	if !newRunLog(path).ranOn("brief", "2026-06-27") { // persisted
		t.Error("run-log should persist across instances")
	}
	// nil/empty are safe no-ops.
	var nr *runLog
	nr.record("x", "2026-06-27")
	if nr.ranOn("x", "2026-06-27") {
		t.Error("nil run-log should report not-run")
	}
}

type fakeMailer struct {
	subjects []string
	bodies   []string
}

func (f *fakeMailer) Deliver(subject, body string) error {
	f.subjects = append(f.subjects, subject)
	f.bodies = append(f.bodies, body)
	return nil
}

func TestRecordDeliversByEmail(t *testing.T) {
	cfg := &Config{Tasks: []Task{{Name: "brief", Schedule: "@daily", Prompt: "p"}}}
	factory := func(context.Context, io.Writer) (Agent, error) {
		return answeringAgent{answer: "Your summary."}, nil
	}
	fm := &fakeMailer{}
	s := New(cfg, io.Discard, factory, nil, nil, time.UTC).WithMailer(fm)

	if err := s.RunOnce(context.Background(), "brief"); err != nil {
		t.Fatal(err)
	}
	if len(fm.bodies) != 1 || fm.bodies[0] != "Your summary." {
		t.Fatalf("mailer should get the answer body: %v", fm.bodies)
	}
	if len(fm.subjects) != 1 || !strings.Contains(fm.subjects[0], "brief") {
		t.Errorf("subject should mention the task: %v", fm.subjects)
	}
}
