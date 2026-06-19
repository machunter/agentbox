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

// Task is one scheduled job. It runs exactly one of Prompt (an agent task) or
// Command (a built-in, e.g. "process-captures").
type Task struct {
	Name     string `yaml:"name"`
	Schedule string `yaml:"schedule"` // cron spec ("0 8 * * *") or descriptor ("@daily")
	Prompt   string `yaml:"prompt"`
	Command  string `yaml:"command"`
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
		if (t.Prompt == "") == (t.Command == "") {
			return fmt.Errorf("task %q: set exactly one of prompt or command", t.Name)
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

// CommandFunc is a built-in task body (e.g. processing the capture inbox). It
// writes operational detail (full logs) to out, and returns a concise digest
// line for the daily journal — or "" to record nothing, e.g. a no-op run that
// did no work and shouldn't clutter the digest.
type CommandFunc func(ctx context.Context, out io.Writer) (digest string, err error)

// Answerer is implemented by agents that can report their prose output (no tool
// traces), used to record a clean entry in the daily journal.
type Answerer interface{ Answer() string }

// Scheduler runs configured tasks on their cron schedules.
type Scheduler struct {
	cfg      *Config
	out      io.Writer
	factory  Factory
	commands map[string]CommandFunc
	journal  *journal.Journal // nil = no daily-output file
	loc      *time.Location   // timezone cron schedules are interpreted in (nil = time.Local)
}

// New builds a Scheduler from a config, an output sink, an agent factory (for
// prompt tasks), a registry of built-in commands (for command tasks), an
// optional journal that records each task's output to a daily markdown file,
// and the timezone (loc) that cron schedules are interpreted in (nil =
// time.Local).
func New(cfg *Config, out io.Writer, factory Factory, commands map[string]CommandFunc, jnl *journal.Journal, loc *time.Location) *Scheduler {
	return &Scheduler{cfg: cfg, out: out, factory: factory, commands: commands, journal: jnl, loc: loc}
}

// record appends a task's output to the daily journal, if one is configured.
func (s *Scheduler) record(name, body string) {
	if s.journal == nil {
		return
	}
	if err := s.journal.Append(time.Now(), name, body); err != nil {
		fmt.Fprintf(s.out, "[%s] task %q: journal write failed: %v\n", now(), name, err)
	}
}

// Serve schedules all tasks and runs until ctx is cancelled, then waits for any
// in-flight task to finish.
func (s *Scheduler) Serve(ctx context.Context) error {
	loc := s.loc
	if loc == nil {
		loc = time.Local
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
	var daily []Task
	for _, t := range s.cfg.Tasks {
		sched, err := cron.ParseStandard(t.Schedule)
		if err != nil {
			continue // already validated at load; ignore defensively
		}
		if firesDaily(sched, from) {
			daily = append(daily, t)
		}
	}
	if len(daily) == 0 {
		return
	}
	fmt.Fprintf(s.out, "scheduler: running %d daily task(s) once at startup\n", len(daily))
	for _, t := range daily {
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

	runCtx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()

	if t.Command != "" {
		cmd, ok := s.commands[t.Command]
		if !ok {
			fmt.Fprintf(s.out, "[%s] task %q: unknown command %q\n", now(), t.Name, t.Command)
			return
		}
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
	if err := ag.Run(runCtx, t.Prompt); err != nil {
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
