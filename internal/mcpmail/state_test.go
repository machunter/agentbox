package mcpmail

import (
	"path/filepath"
	"testing"
)

func TestStateStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wm.json")
	s := newStateStore(path)

	// Unknown mailbox -> 0.
	if got := s.get("INBOX", 1); got != 0 {
		t.Errorf("empty get = %d, want 0", got)
	}

	if err := s.set("INBOX", 1, 100); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := s.get("INBOX", 1); got != 100 {
		t.Errorf("get = %d, want 100", got)
	}

	// Persists across a fresh store reading the same file.
	if got := newStateStore(path).get("INBOX", 1); got != 100 {
		t.Errorf("reload get = %d, want 100", got)
	}
}

func TestStateStoreUIDValidityReset(t *testing.T) {
	s := newStateStore(filepath.Join(t.TempDir(), "wm.json"))
	if err := s.set("INBOX", 1, 100); err != nil {
		t.Fatal(err)
	}
	// A different UIDVALIDITY makes the stored UID meaningless -> 0 (re-baseline).
	if got := s.get("INBOX", 2); got != 0 {
		t.Errorf("get with new uidvalidity = %d, want 0", got)
	}
	// Recording under the new validity replaces the old entry.
	if err := s.set("INBOX", 2, 5); err != nil {
		t.Fatal(err)
	}
	if got := s.get("INBOX", 2); got != 5 {
		t.Errorf("get = %d, want 5", got)
	}
}

func TestStateStoreNeverMovesBackward(t *testing.T) {
	s := newStateStore(filepath.Join(t.TempDir(), "wm.json"))
	if err := s.set("INBOX", 1, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.set("INBOX", 1, 50); err != nil { // lower, same validity
		t.Fatal(err)
	}
	if got := s.get("INBOX", 1); got != 100 {
		t.Errorf("watermark moved backward to %d, want 100", got)
	}
}

func TestStateStoreDisabled(t *testing.T) {
	// Empty path => persistence disabled: get is always 0, set is a no-op.
	s := newStateStore("")
	if err := s.set("INBOX", 1, 100); err != nil {
		t.Errorf("set on disabled store: %v", err)
	}
	if got := s.get("INBOX", 1); got != 0 {
		t.Errorf("disabled get = %d, want 0", got)
	}
	// A nil store must not panic.
	var ns *stateStore
	if got := ns.get("INBOX", 1); got != 0 {
		t.Errorf("nil get = %d, want 0", got)
	}
	if err := ns.set("INBOX", 1, 1); err != nil {
		t.Errorf("nil set: %v", err)
	}
}
