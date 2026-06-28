// Package schedule turns agentbox into a long-lived process that runs tasks on
// cron schedules. A YAML config lists named tasks (a cron spec + a prompt); the
// scheduler runs each on time as an independent agent run, sharing the
// persistent memory store across runs.
package schedule

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
	yaml "go.yaml.in/yaml/v4"

	"github.com/burcsahinoglu/agentbox/internal/journal"
)

// taskTimeout bounds a single scheduled run so a stuck task can't run forever.
const taskTimeout = 15 * time.Minute

// Task is one scheduled job. How it runs is resolved in priority order: an
// explicit Command, an explicit Prompt, or — when it has neither — a built-in
// keyed by Name (a registered command, else a built-in prompt). The bare-name
// form keeps schedule.yaml approachable: just a name and a cron schedule.
type Task struct {
	Name     string `yaml:"name"`
	Schedule string `yaml:"schedule"` // cron spec ("0 8 * * *") or descriptor ("@daily")
	Prompt   string `yaml:"prompt"`   // optional: overrides the built-in prompt
	Command  string `yaml:"command"`  // optional: explicit built-in command
}

// Config is the parsed schedule file.
type Config struct {
	Tasks []Task `yaml:"tasks"`
}

// Load reads and validates a schedule config from path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schedule: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse schedule: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks that tasks have unique non-empty names, valid cron schedules,
// and non-empty prompts.
func (c *Config) Validate() error {
	if len(c.Tasks) == 0 {
		return fmt.Errorf("schedule has no tasks")
	}
	seen := make(map[string]bool, len(c.Tasks))
	for i, t := range c.Tasks {
		if t.Name == "" {
			return fmt.Errorf("task %d: name is required", i)
		}
		if seen[t.Name] {
			return fmt.Errorf("duplicate task name %q", t.Name)
		}
		seen[t.Name] = true
		if t.Prompt != "" && t.Command != "" {
			return fmt.Errorf("task %q: set at most one of prompt or command (a bare name runs the matching built-in task)", t.Name)
		}
		if _, err := cron.ParseStandard(t.Schedule); err != nil {
			return fmt.Errorf("task %q: invalid schedule %q: %w", t.Name, t.Schedule, err)
		}
	}
	return nil
}

// Task returns the named task.
func (c *Config) Task(name string) (Task, bool) {
	for _, t := range c.Tasks {
		if t.Name == name {
			return t, true
		}
	}
	return Task{}, false
}

// Agent is the subset of the agent used by the scheduler. It's an interface so
// the scheduler can be tested without the real agent (or the API).
type Agent interface {
	Run(ctx context.Context, task string) error
}

// Factory builds a fresh Agent for one run, writing its output to out.
type Factory func(ctx context.Context, out io.Writer) (Agent, error)

// PromptFunc resolves the prompt for a built-in task by name, returning the
// prompt and whether one is defined. It's a function (not a static map) so the
// resolver can consult a live, user-editable override file each run.
type PromptFunc func(name string) (string, bool)

// CommandFunc is a built-in task body (e.g. processing the capture inbox). It
// writes operational detail (full logs) to out, and returns a concise digest
// line for the daily journal — or "" to record nothing, e.g. a no-op run that
// did no work and shouldn't clutter the digest.
type CommandFunc func(ctx context.Context, out io.Writer) (digest string, err error)

// Answerer is implemented by agents that can report their prose output (no tool
// traces), used to record a clean entry in the daily journal.
type Answerer interface{ Answer() string }

// Deliverer sends a task's output to an external channel (e.g. email to self),
// in addition to the journal. nil disables delivery.
type Deliverer interface {
	Deliver(subject, body string) error
}

// Scheduler runs configured tasks on their cron schedules.
type Scheduler struct {
	cfg      *Config
	out      io.Writer
	factory  Factory
	commands map[string]CommandFunc
	prompts  PromptFunc       // resolves a built-in prompt by task name (nil = none)
	journal  *journal.Journal // nil = no daily-output file
	loc      *time.Location   // timezone cron schedules are interpreted in (nil = time.Local)
	runs     *runLog          // per-task last-run dates; gates the startup catch-up
	mailer   Deliverer        // nil = no email delivery
}

// WithMailer enables emailing each task's recorded output (the same digest that
// goes to the journal). Returns the scheduler for chaining.
func (s *Scheduler) WithMailer(d Deliverer) *Scheduler {
	s.mailer = d
	return s
}

// WithRunLog enables the once-a-day startup-catch-up guard, persisting per-task
// last-run dates to path so restarts don't re-fire daily tasks. Returns the
// scheduler for chaining.
func (s *Scheduler) WithRunLog(path string) *Scheduler {
	s.runs = newRunLog(path)
	return s
}

// todayStr is the current date (YYYY-MM-DD) in the scheduler's timezone.
func (s *Scheduler) todayStr() string {
	loc := s.loc
	if loc == nil {
		loc = time.Local
	}
	return time.Now().In(loc).Format("2006-01-02")
}

// WithPrompts registers a resolver for built-in prompts so a task can be
// configured with just a name and schedule — its prompt comes from the binary
// (or a user override file), not the user-facing schedule. Returns the scheduler
// for chaining.
func (s *Scheduler) WithPrompts(p PromptFunc) *Scheduler {
	s.prompts = p
	return s
}

// resolve determines how a task runs: a command, a prompt, or neither (ok=false).
// Priority: explicit Command, explicit Prompt, then a built-in keyed by Name (a
// registered command first, then a built-in prompt).
func (s *Scheduler) resolve(t Task) (cmd CommandFunc, prompt string, ok bool) {
	if t.Command != "" {
		c, found := s.commands[t.Command]
		return c, "", found // unknown explicit command → ok=false
	}
	if t.Prompt != "" {
		return nil, t.Prompt, true
	}
	if c, found := s.commands[t.Name]; found {
		return c, "", true
	}
	if s.prompts != nil {
		if p, found := s.prompts(t.Name); found && p != "" {
			return nil, p, true
		}
	}
	return nil, "", false
}

// New builds a Scheduler from a config, an output sink, an agent factory (for
// prompt tasks), a registry of built-in commands (for command tasks), an
// optional journal that records each task's output to a daily markdown file,
// and the timezone (loc) that cron schedules are interpreted in (nil =
// time.Local).
func New(cfg *Config, out io.Writer, factory Factory, commands map[string]CommandFunc, jnl *journal.Journal, loc *time.Location) *Scheduler {
	return &Scheduler{cfg: cfg, out: out, factory: factory, commands: commands, journal: jnl, loc: loc}
}

// record appends a task's output to the daily journal and, if email delivery is
// configured, sends the same digest to the user. Both are best-effort.
func (s *Scheduler) record(name, body string) {
	if s.journal != nil {
		if err := s.journal.Append(time.Now(), name, body); err != nil {
			fmt.Fprintf(s.out, "[%s] task %q: journal write failed: %v\n", now(), name, err)
		}
	}
	if s.mailer != nil {
		if err := s.mailer.Deliver("agentbox: "+name, body); err != nil {
			fmt.Fprintf(s.out, "[%s] task %q: email delivery failed: %v\n", now(), name, err)
		}
	}
}

// Serve schedules all tasks and runs until ctx is cancelled, then waits for any
// in-flight task to finish.
func (s *Scheduler) Serve(ctx context.Context) error {
	loc := s.loc
	if loc == nil {
		loc = time.Local
	}
	// Fail fast on a task that resolves to nothing (e.g. a misspelled built-in
	// name), rather than letting it error only when its cron time arrives.
	for _, t := range s.cfg.Tasks {
		if _, _, ok := s.resolve(t); !ok {
			return fmt.Errorf("task %q: not runnable — set a prompt, a valid command, or use a known built-in task name", t.Name)
		}
	}

	c := cron.New(cron.WithLocation(loc), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))
	for _, t := range s.cfg.Tasks {
		if _, err := c.AddFunc(t.Schedule, func() { s.runTask(ctx, t) }); err != nil {
			return fmt.Errorf("schedule task %q: %w", t.Name, err)
		}
	}
	c.Start()
	fmt.Fprintf(s.out, "scheduler: %d task(s) scheduled (timezone %s); waiting for their times\n", len(s.cfg.Tasks), loc)

	// Startup catch-up: run every task that fires at least daily once now, so a
	// fresh start (or restart) immediately produces today's briefings/captures
	// instead of waiting for the next scheduled time. Done in the background so
	// cron stays responsive and shutdown isn't blocked.
	go s.runDailyOnce(ctx, loc)

	<-ctx.Done()
	fmt.Fprintln(s.out, "scheduler: shutting down, waiting for in-flight tasks…")
	<-c.Stop().Done()
	return nil
}

// runDailyOnce runs each task that fires at least once a day a single time,
// now — a startup catch-up so restarting the scheduler delivers today's
// briefings/captures without waiting for their next cron time. Weekly/monthly
// tasks are skipped (you don't want a weekly review on every restart).
func (s *Scheduler) runDailyOnce(ctx context.Context, loc *time.Location) {
	from := time.Now().In(loc)
	// Command tasks first, then prompt tasks: built-ins like process-captures
	// prepare state (file todos from photos) that a briefing prompt then reads,
	// so the startup briefing reflects anything just captured.
	var cmds, prompts []Task
	for _, t := range s.cfg.Tasks {
		sched, err := cron.ParseStandard(t.Schedule)
		if err != nil {
			continue // already validated at load; ignore defensively
		}
		if !firesDaily(sched, from) {
			continue
		}
		cmd, _, ok := s.resolve(t)
		if !ok {
			continue // not runnable; runTask logs it at its scheduled time
		}
		if cmd != nil {
			cmds = append(cmds, t)
		} else {
			prompts = append(prompts, t)
		}
	}
	// Skip tasks that already ran today (a prior startup or a cron fire), so
	// repeated restarts don't re-fire the same daily briefing each time.
	today := from.Format("2006-01-02")
	var due []Task
	for _, t := range append(cmds, prompts...) {
		if s.runs.ranOn(t.Name, today) {
			fmt.Fprintf(s.out, "scheduler: %q already ran today; skipping startup run\n", t.Name)
			continue
		}
		due = append(due, t)
	}
	if len(due) == 0 {
		return
	}
	fmt.Fprintf(s.out, "scheduler: running %d daily task(s) once at startup\n", len(due))
	for _, t := range due {
		if ctx.Err() != nil {
			return
		}
		s.runTask(ctx, t)
	}
}

// firesDaily reports whether sched runs at least once every day: every gap
// between consecutive fires is 24h or less. Sampling several consecutive gaps
// distinguishes daily/sub-daily schedules (true) from weekly/monthly ones
// (false), regardless of when "from" falls.
func firesDaily(sched cron.Schedule, from time.Time) bool {
	const day = 24 * time.Hour
	t := sched.Next(from)
	if t.IsZero() {
		return false
	}
	for range 8 {
		next := sched.Next(t)
		if next.IsZero() || next.Sub(t) > day {
			return false
		}
		t = next
	}
	return true
}

// RunOnce runs a single named task immediately (used by `run-task`).
func (s *Scheduler) RunOnce(ctx context.Context, name string) error {
	t, ok := s.cfg.Task(name)
	if !ok {
		return fmt.Errorf("no task named %q", name)
	}
	s.runTask(ctx, t)
	return nil
}

// runTask executes one task as a fresh agent run. Failures are logged, not
// propagated: a scheduler shouldn't die because one run failed.
func (s *Scheduler) runTask(ctx context.Context, t Task) {
	fmt.Fprintf(s.out, "\n[%s] task %q: starting\n", now(), t.Name)

	// Mark the task as run today (cron or startup), so the startup catch-up
	// won't re-fire it on a later restart the same day.
	defer s.runs.record(t.Name, s.todayStr())

	runCtx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()

	cmd, prompt, ok := s.resolve(t)
	if !ok {
		if t.Command != "" {
			fmt.Fprintf(s.out, "[%s] task %q: unknown command %q\n", now(), t.Name, t.Command)
		} else {
			fmt.Fprintf(s.out, "[%s] task %q: not runnable — give it a prompt, a command, or use a known built-in task name\n", now(), t.Name)
		}
		return
	}

	if cmd != nil {
		// Full logs go to s.out; the command returns a concise digest line (or
		// "") so no-op runs don't clutter the daily journal.
		digest, err := cmd(runCtx, s.out)
		if err != nil {
			fmt.Fprintf(s.out, "[%s] task %q: failed: %v\n", now(), t.Name, err)
			s.record(t.Name, "failed: "+err.Error())
			return
		}
		fmt.Fprintf(s.out, "[%s] task %q: done\n", now(), t.Name)
		if d := strings.TrimSpace(digest); d != "" {
			s.record(t.Name, d)
		}
		return
	}

	ag, err := s.factory(runCtx, s.out)
	if err != nil {
		fmt.Fprintf(s.out, "[%s] task %q: setup failed: %v\n", now(), t.Name, err)
		return
	}
	if err := ag.Run(runCtx, prompt); err != nil {
		fmt.Fprintf(s.out, "[%s] task %q: failed: %v\n", now(), t.Name, err)
		s.record(t.Name, "failed: "+err.Error())
		return
	}
	fmt.Fprintf(s.out, "[%s] task %q: done\n", now(), t.Name)
	// Record the assistant's closing summary (clean prose, no tool traces) if
	// available and non-empty.
	if a, ok := ag.(Answerer); ok {
		if ans := strings.TrimSpace(a.Answer()); ans != "" {
			s.record(t.Name, ans)
		}
	}
}

func now() string { return time.Now().Format(time.RFC3339) }
