package schedule

import (
	"encoding/json"
	"os"
	"sync"
)

// runLog persists, per task, the date (YYYY-MM-DD) it last ran. It lets the
// startup catch-up skip tasks that already ran today, so repeated container
// restarts don't re-fire a daily briefing each time. A nil/empty-path runLog is
// a no-op (no persistence), which simply disables the once-a-day guard.
type runLog struct {
	path string
	mu   sync.Mutex
	days map[string]string
}

func newRunLog(path string) *runLog {
	r := &runLog{path: path, days: map[string]string{}}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &r.days)
		}
	}
	return r
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
		return
	}
	tmp := r.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, r.path)
	}
}
