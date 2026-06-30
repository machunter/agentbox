package mcpnotes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreConcurrentAdds(t *testing.T) {
	s := NewStore(t.TempDir(), t.TempDir(), time.UTC)
	const n = 25
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, _, err := s.AddTodo(fmt.Sprintf("todo number %d", i)); err != nil {
				t.Errorf("AddTodo: %v", err)
			}
		}(i)
	}
	wg.Wait()

	list, err := s.ListTodos(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(list, "- [ ] "); got != n {
		t.Fatalf("want %d todos after %d concurrent adds, got %d (lost writes)", n, n, got)
	}
}

func TestParseTodos(t *testing.T) {
	content := `# Todos
- [ ] call dentist
- [x] buy milk
- [X] pay rent
not a todo
  - [ ] indented item
`
	todos := parseTodos(content)
	if len(todos) != 4 {
		t.Fatalf("want 4 todos, got %d: %+v", len(todos), todos)
	}
	open, done := 0, 0
	for _, td := range todos {
		if td.Done {
			done++
		} else {
			open++
		}
	}
	if open != 2 || done != 2 {
		t.Errorf("open=%d done=%d, want 2/2", open, done)
	}
}

func TestAddTodo(t *testing.T) {
	out := addTodo("", "call dentist", "2026-06-14")
	if !strings.Contains(out, "- [ ] call dentist") || !strings.Contains(out, "2026-06-14") {
		t.Fatalf("addTodo wrong: %q", out)
	}
	out = addTodo(out, "buy milk", "")
	todos := parseTodos(out)
	if len(todos) != 2 {
		t.Fatalf("want 2 todos after second add, got %d", len(todos))
	}
}

func TestSimilarTodo(t *testing.T) {
	open := []Todo{
		{Text: "Send finalized data labeling deck to Casey  <!-- 2026-06-26 -->"},
		{Text: "Connect with Karley Nakamura about travel and hotel for the July offsite"},
		{Text: "Pay SP SEEKING HEALTH and submit receipt", Done: true}, // completed: ignored
	}

	// A re-worded restatement of an existing open todo is caught (the date
	// marker and filler words are ignored).
	if got := similarTodo(open, "send the finalized data-labeling deck to Casey"); got == "" {
		t.Error("expected the Casey deck restatement to match an existing todo")
	}
	// A distinct todo is not flagged.
	if got := similarTodo(open, "review Asim's profile and sign an NDA"); got != "" {
		t.Errorf("distinct todo wrongly matched %q", got)
	}
	// Overlap with a completed todo doesn't count (only open todos dedup).
	if got := similarTodo(open, "Pay SP SEEKING HEALTH and submit receipt"); got != "" {
		t.Errorf("should not match a completed todo, got %q", got)
	}
}

func TestAddTodoSkipsNearDuplicate(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, dir, time.UTC)

	added, _, err := s.AddTodo("Confirm ground truth dataset with Gimou for the accuracy report")
	if err != nil || !added {
		t.Fatalf("first add: added=%v err=%v", added, err)
	}
	// A near-duplicate (reworded) is skipped and reports the existing todo.
	added, dup, err := s.AddTodo("confirm the ground-truth dataset with Gimou for accuracy report")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("near-duplicate should not have been added")
	}
	if dup == "" {
		t.Error("skip should report the conflicting todo's text")
	}
	// A genuinely different todo still adds.
	added, _, err = s.AddTodo("Organize a demo of Kanu with Karan")
	if err != nil || !added {
		t.Fatalf("distinct add: added=%v err=%v", added, err)
	}

	list, _ := s.ListTodos(false)
	if got := strings.Count(list, "- [ ] "); got != 2 {
		t.Fatalf("want 2 open todos (dup skipped), got %d:\n%s", got, list)
	}
}

func TestAddTodoSkipsRecentlyCompleted(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, dir, time.UTC)

	// File a todo, then complete it (moves it to done/<today>.md).
	if _, _, err := s.AddTodo("Reply to Jary in #team-software-leadership about the roadmap"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTodo("Jary"); err != nil {
		t.Fatal(err)
	}

	// Re-filing the same item (as a sweep would, re-seeing Jary's message) must
	// be skipped — not resurrected — because it was just completed.
	added, dup, err := s.AddTodo("reply to Jary in #team-software-leadership re the roadmap")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("a recently-completed item should not be re-added (resurrection)")
	}
	if dup == "" {
		t.Error("skip should report the completed todo it matched")
	}
	if list, _ := s.ListTodos(false); strings.Contains(list, "Jary") {
		t.Errorf("resurrected todo leaked back onto the open list:\n%s", list)
	}
}

func TestRecentDoneWindowExcludesOld(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, dir, time.UTC)

	// Simulate a todo completed long ago by writing a stale done/ file directly.
	oldDate := time.Now().AddDate(0, 0, -(dedupWindowDays + 10)).Format("2006-01-02")
	doneFile := filepath.Join(dir, "done", oldDate+".md")
	if err := os.MkdirAll(filepath.Dir(doneFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doneFile, []byte("- [x] Quarterly board prep deck\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Outside the window, the same task may legitimately recur — so it adds.
	added, _, err := s.AddTodo("Quarterly board prep deck")
	if err != nil || !added {
		t.Fatalf("task completed outside the window should be addable: added=%v err=%v", added, err)
	}
}

func TestRemoveTodo(t *testing.T) {
	content := "- [ ] call dentist\n- [ ] buy oat milk\n"

	newContent, removed, text, err := removeTodo(content, "milk")
	if err != nil {
		t.Fatalf("removeTodo: %v", err)
	}
	if text != "buy oat milk" {
		t.Errorf("completed text = %q", text)
	}
	if strings.Contains(newContent, "oat milk") {
		t.Errorf("todo not removed from todos.md: %q", newContent)
	}
	if !strings.Contains(newContent, "- [ ] call dentist") {
		t.Errorf("unrelated todo changed: %q", newContent)
	}
	if !strings.Contains(removed, "buy oat milk") {
		t.Errorf("removed line = %q", removed)
	}

	if _, _, _, err := removeTodo(content, "nonexistent"); err == nil {
		t.Error("completing a missing todo should error")
	}
	// Already-done items shouldn't match (only open "- [ ] " lines do).
	if _, _, _, err := removeTodo("- [x] done thing\n", "done thing"); err == nil {
		t.Error("should not 'complete' an already-done todo")
	}
}

func TestAddNoteSingleLine(t *testing.T) {
	out := addNote("", "remember to\nbatch the\nbriefings", "2026-06-14 14:30")
	if strings.Count(out, "\n") != 1 { // one entry, one trailing newline
		t.Errorf("note should collapse to one line: %q", out)
	}
	if !strings.Contains(out, "2026-06-14 14:30") || !strings.Contains(out, "remember to batch the briefings") {
		t.Errorf("note content wrong: %q", out)
	}
}

func TestSearchLines(t *testing.T) {
	content := "- 2026-06-14 idea about caching\n- 2026-06-13 grocery list\n\n- 2026-06-12 caching again\n"
	got := searchLines(content, "caching")
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %d: %v", len(got), got)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, dir, time.UTC)
	doneToday := filepath.Join(dir, "done", time.Now().UTC().Format("2006-01-02")+".md")

	if _, _, err := s.AddTodo("call dentist"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddTodo("buy milk"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListTodos(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list, "call dentist") || !strings.Contains(list, "buy milk") {
		t.Fatalf("list missing todos: %q", list)
	}

	if _, err := s.CompleteTodo("dentist"); err != nil {
		t.Fatal(err)
	}
	open, _ := s.ListTodos(false)
	if strings.Contains(open, "call dentist") {
		t.Errorf("completed todo still listed as open: %q", open)
	}
	// The completed todo moved out of todos.md into today's dated done file.
	if data, _ := os.ReadFile(filepath.Join(dir, "todos.md")); strings.Contains(string(data), "dentist") {
		t.Errorf("completed todo should be gone from todos.md: %q", data)
	}
	doneData, err := os.ReadFile(doneToday)
	if err != nil || !strings.Contains(string(doneData), "call dentist") {
		t.Errorf("completed todo not in today's done file (err=%v): %q", err, doneData)
	}
	// include_done surfaces it again.
	withDone, _ := s.ListTodos(true)
	if !strings.Contains(withDone, "call dentist") {
		t.Errorf("ListTodos(true) should include recent done: %q", withDone)
	}

	if err := s.AddNote("idea: process captures from email", "2026-06-14 09:00"); err != nil {
		t.Fatal(err)
	}
	found, err := s.SearchNotes("captures")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(found, "process captures") {
		t.Errorf("note not found: %q", found)
	}
}

func TestListTodosEmpty(t *testing.T) {
	s := NewStore(t.TempDir(), t.TempDir(), time.UTC)
	out, err := s.ListTodos(false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "(no open todos)" {
		t.Errorf("empty list = %q", out)
	}
}

func TestArchivesHandMarkedDone(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, dir, time.UTC)
	// Simulate the user hand-editing todos.md to mark items done with [x].
	if err := os.WriteFile(filepath.Join(dir, "todos.md"),
		[]byte("- [ ] open one\n- [x] hand-done two\n- [ ] open three\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Any todo op should sweep the [x] item out into today's done file.
	list, err := s.ListTodos(false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(list, "hand-done two") {
		t.Errorf("hand-done item still listed as open: %q", list)
	}
	if !strings.Contains(list, "open one") || !strings.Contains(list, "open three") {
		t.Errorf("open todos lost: %q", list)
	}
	// It's gone from todos.md and present in today's done file.
	todosData, _ := os.ReadFile(filepath.Join(dir, "todos.md"))
	if strings.Contains(string(todosData), "hand-done two") {
		t.Errorf("hand-done item not removed from todos.md: %q", todosData)
	}
	doneData, err := os.ReadFile(filepath.Join(dir, "done", time.Now().UTC().Format("2006-01-02")+".md"))
	if err != nil || !strings.Contains(string(doneData), "hand-done two") {
		t.Errorf("hand-done item not archived (err=%v): %q", err, doneData)
	}
}

func TestTodosAndNotesUseSeparateDirs(t *testing.T) {
	todosDir := t.TempDir()
	notesDir := t.TempDir()
	s := NewStore(todosDir, notesDir, time.UTC)

	if _, _, err := s.AddTodo("call dentist"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote("idea about caching", "2026-06-28 09:00"); err != nil {
		t.Fatal(err)
	}

	// todos.md lands in todosDir, not notesDir.
	if _, err := os.Stat(filepath.Join(todosDir, "todos.md")); err != nil {
		t.Errorf("todos.md not in todosDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(notesDir, "todos.md")); !os.IsNotExist(err) {
		t.Errorf("todos.md should not be in notesDir")
	}
	// inbox.md lands in notesDir, not todosDir.
	if _, err := os.Stat(filepath.Join(notesDir, "inbox.md")); err != nil {
		t.Errorf("inbox.md not in notesDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(todosDir, "inbox.md")); !os.IsNotExist(err) {
		t.Errorf("inbox.md should not be in todosDir")
	}
}
