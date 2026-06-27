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
	s := NewStore(t.TempDir(), time.UTC)
	const n = 25
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.AddTodo(fmt.Sprintf("todo number %d", i)); err != nil {
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
	s := NewStore(dir, time.UTC)
	doneToday := filepath.Join(dir, "done", time.Now().UTC().Format("2006-01-02")+".md")

	if err := s.AddTodo("call dentist"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTodo("buy milk"); err != nil {
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
	s := NewStore(t.TempDir(), time.UTC)
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
	s := NewStore(dir, time.UTC)
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
