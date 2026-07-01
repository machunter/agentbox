// Package journal records the assistant's output to a daily markdown file —
// one file per day (e.g. 2026-06-17.md), appended to as scheduled tasks run.
// It's the assistant's "delivery" channel when there's no SMTP/push: a readable
// digest of what it did and found each day.
package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Journal appends entries to dated markdown files under a directory.
type Journal struct {
	dir string
	loc *time.Location
	mu  sync.Mutex // serializes Append so concurrent tasks don't race the header
}

// New returns a Journal writing to dir, dating entries in loc (nil = UTC).
func New(dir string, loc *time.Location) *Journal {
	if loc == nil {
		loc = time.UTC
	}
	return &Journal{dir: dir, loc: loc}
}

// Append adds a timestamped section (## HH:MM — heading) with body to the
// current day's file, creating the file (with a date header) if needed.
func (j *Journal) Append(when time.Time, heading, body string) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	when = when.In(j.loc)
	if err := os.MkdirAll(j.dir, 0o755); err != nil {
		return fmt.Errorf("journal: create dir: %w", err)
	}
	path := filepath.Join(j.dir, when.Format("2006-01-02")+".md")

	body = strings.TrimSpace(body)
	if body == "" {
		body = "(no output)"
	}

	var b strings.Builder
	if _, err := os.Stat(path); os.IsNotExist(err) {
		b.WriteString("# " + when.Format("Monday, 2006-01-02") + "\n")
	}
	fmt.Fprintf(&b, "\n## %s — %s\n\n%s\n", when.Format("15:04"), heading, body)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("journal: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("journal: write %s: %w", path, err)
	}
	return nil
}
