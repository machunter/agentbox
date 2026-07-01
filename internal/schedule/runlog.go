package schedule

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// runLog persists, per task, the date (YYYY-MM-DD) it last ran. It lets the
// startup catch-up skip tasks that already ran today, so repeated container
// restarts don't re-fire a daily briefing each time. A nil/empty-path runLog is
// a no-op (no persistence), which simply disables the once-a-day guard.
type runLog struct {
	path string
	out  io.Writer // diagnostics sink for persistence failures (nil = silent)
	mu   sync.Mutex
	days map[string]string
}

func newRunLog(path string, out io.Writer) *runLog {
	r := &runLog{path: path, out: out, days: map[string]string{}}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(b, &r.days); err != nil {
				r.logf("run log: ignoring corrupt %s: %v", path, err)
				r.days = map[string]string{}
			}
		} else if !os.IsNotExist(err) {
			r.logf("run log: cannot read %s: %v", path, err)
		}
	}
	return r
}

// logf reports a persistence problem to out, if one is configured. These
// failures are non-fatal but can cause duplicate daily runs, so they shouldn't
// vanish silently.
func (r *runLog) logf(format string, args ...any) {
	if r.out != nil {
		fmt.Fprintf(r.out, format+"\n", args...)
	}
}

// ranOn reports whether the task's recorded last-run date equals date.
func (r *runLog) ranOn(name, date string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.days[name] == date
}

// record stores date as the task's last-run date and persists the log (atomic
// write). Best-effort: persistence failures are ignored.
func (r *runLog) record(name, date string) {
	if r == nil || r.path == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.days[name] = date
	b, err := json.Marshal(r.days)
	if err != nil {
		r.logf("run log: marshal failed: %v", err)
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		r.logf("run log: write %s failed: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		r.logf("run log: persist %s failed: %v", r.path, err)
	}
}
