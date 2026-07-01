package mcpmail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// watermark records how far a mailbox has been processed. IMAP UIDs are only
// meaningful within a (mailbox, UIDVALIDITY) pair, so we store both: when the
// server reports a different UIDValidity, the stored LastUID is stale and is
// ignored (re-baseline rather than skip or reprocess blindly).
type watermark struct {
	UIDValidity uint32 `json:"uidvalidity"`
	LastUID     uint32 `json:"last_uid"`
}

// stateStore persists per-mailbox watermarks to a JSON file so recurring
// briefings only process genuinely new mail across separate runs/processes.
// Writes are atomic (temp file + rename); a zero-value/nil store is a no-op
// (persistence disabled), which simply makes list_new_emails non-incremental.
type stateStore struct {
	path string
	mu   sync.Mutex
}

func newStateStore(path string) *stateStore { return &stateStore{path: path} }

// stateFilePath resolves where watermarks are stored: AGENTBOX_MAIL_STATE_DIR if
// set, else the persisted memory dir (AGENTBOX_MEMORY_DIR). Empty when neither
// is set, which disables persistence.
func stateFilePath() string {
	dir := os.Getenv("AGENTBOX_MAIL_STATE_DIR")
	if dir == "" {
		dir = os.Getenv("AGENTBOX_MEMORY_DIR")
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "mail-watermarks.json")
}

// get returns the last processed UID for a mailbox, but only when it was
// recorded under the same UIDValidity; otherwise 0 (treat all as new).
func (s *stateStore) get(mailbox string, uidValidity uint32) uint32 {
	if s == nil || s.path == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.load()[mailbox]
	if !ok || w.UIDValidity != uidValidity {
		return 0
	}
	return w.LastUID
}

// set records the watermark for a mailbox. It never moves backwards within the
// same UIDValidity, but a changed UIDValidity replaces the entry.
func (s *stateStore) set(mailbox string, uidValidity, lastUID uint32) error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.load()
	if w, ok := m[mailbox]; ok && w.UIDValidity == uidValidity && w.LastUID >= lastUID {
		return nil
	}
	m[mailbox] = watermark{UIDValidity: uidValidity, LastUID: lastUID}
	return s.save(m)
}

func (s *stateStore) load() map[string]watermark {
	m := map[string]watermark{}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func (s *stateStore) save(m map[string]watermark) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	// 0o600: mail watermarks are derived from the user's private mailbox.
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path) // atomic replace
}
