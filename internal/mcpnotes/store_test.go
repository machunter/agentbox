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
			if _, err := s.AddTodo(fmt.Sprintf("todo number %d", i), "manual"); err != nil {
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
	out := addTodo("", "call dentist", "2026-06-14", nil)
	if !strings.Contains(out, "- [ ] call dentist") || !strings.Contains(out, "2026-06-14") {
		t.Fatalf("addTodo wrong: %q", out)
	}
	out = addTodo(out, "buy milk", "", nil)
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

	res, err := s.AddTodo("Confirm ground truth dataset with Gimou for the accuracy report", "email:gimou")
	if err != nil || !res.Added {
		t.Fatalf("first add: added=%v err=%v", res.Added, err)
	}
	// A near-duplicate (reworded) is skipped and reports the existing todo.
	res, err = s.AddTodo("confirm the ground-truth dataset with Gimou for accuracy report", "email:gimou")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added {
		t.Error("near-duplicate should not have been added")
	}
	if res.Dup == "" {
		t.Error("skip should report the conflicting todo's text")
	}
	// A genuinely different todo still adds.
	res, err = s.AddTodo("Organize a demo of Kanu with Karan", "slack:product")
	if err != nil || !res.Added {
		t.Fatalf("distinct add: added=%v err=%v", res.Added, err)
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
	if _, err := s.AddTodo("Reply to Jary in #team-software-leadership about the roadmap", "slack:team-software-leadership"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTodo("Jary"); err != nil {
		t.Fatal(err)
	}

	// Re-filing the same item (as a sweep would, re-seeing Jary's message) must
	// be skipped — not resurrected — because it was just completed.
	res, err := s.AddTodo("reply to Jary in #team-software-leadership re the roadmap", "slack:team-software-leadership")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added {
		t.Error("a recently-completed item should not be re-added (resurrection)")
	}
	if res.Dup == "" {
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
	res, err := s.AddTodo("Quarterly board prep deck", "manual")
	if err != nil || !res.Added {
		t.Fatalf("task completed outside the window should be addable: added=%v err=%v", res.Added, err)
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

	if _, err := s.AddTodo("call dentist", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddTodo("buy milk", "manual"); err != nil {
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

// A todo's source has to survive the round trip through the markdown line, and
// lines written before sources existed must read back as "unknown origin"
// rather than erroring or inventing one.
func TestTodoSourceRoundTrip(t *testing.T) {
	line := addTodo("", "Reply to Jary about the roadmap", "2026-08-10", []string{"slack:team-eng/1723456.123"})
	if !strings.Contains(line, "src:slack:team-eng/1723456.123") {
		t.Fatalf("source not rendered: %q", line)
	}
	got := parseSources(line)
	if len(got) != 1 || got[0] != "slack:team-eng/1723456.123" {
		t.Errorf("parseSources = %v", got)
	}
	if d := todoDate(line); d != "2026-08-10" {
		t.Errorf("todoDate = %q, want 2026-08-10", d)
	}

	// Legacy shapes: date-only, and no comment at all.
	if got := parseSources("- [ ] call dentist  <!-- 2026-06-14 -->"); got != nil {
		t.Errorf("date-only todo should have no sources, got %v", got)
	}
	if got := parseSources("- [ ] call dentist"); got != nil {
		t.Errorf("bare todo should have no sources, got %v", got)
	}
	if d := todoDate("- [ ] call dentist  <!-- 2026-06-14 -->"); d != "2026-06-14" {
		t.Errorf("legacy date lost: %q", d)
	}
}

// withSources must rewrite only the source list, leaving the text and the
// capture date intact.
func TestWithSourcesPreservesTextAndDate(t *testing.T) {
	line := "- [ ] Reply to Jary  <!-- 2026-08-10 src:email:jary -->"
	got := withSources(line, []string{"email:jary", "slack:team-eng"})
	if !strings.Contains(got, "- [ ] Reply to Jary  <!--") {
		t.Errorf("text mangled: %q", got)
	}
	if !strings.Contains(got, "2026-08-10") {
		t.Errorf("date lost: %q", got)
	}
	if srcs := parseSources(got); len(srcs) != 2 {
		t.Errorf("want 2 sources, got %v", srcs)
	}
	// A todo that never had a comment gains one.
	if got := withSources("- [ ] bare item", []string{"manual"}); !strings.Contains(got, "src:manual") {
		t.Errorf("source not added to bare line: %q", got)
	}
}

// A source label must not be able to break the one-line comment it lives in.
func TestSanitizeSource(t *testing.T) {
	for in, want := range map[string]string{
		"slack:team eng":     "slack:team_eng",
		"email:a,b":          "email:a;b",
		"nasty --> injected": "nasty_injected",
		"  slack:x  ":        "slack:x",
	} {
		if got := sanitizeSource(in); got != want {
			t.Errorf("sanitizeSource(%q) = %q, want %q", in, got, want)
		}
	}
}

// The same request can arrive by mail and in Slack. Dedup keeps one todo, but
// both origins must be recorded, since a reply at either one resolves it.
func TestAddTodoMergesSourcesFromSecondOrigin(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, dir, time.UTC)

	if _, err := s.AddTodo("Send Casey the finalized data labeling deck", "email:casey"); err != nil {
		t.Fatal(err)
	}
	res, err := s.AddTodo("send the finalized data-labeling deck to Casey", "slack:design-review")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added {
		t.Fatal("near-duplicate should not have created a second todo")
	}
	if !res.Merged {
		t.Error("the second origin should have been recorded on the existing todo")
	}

	data, _ := os.ReadFile(filepath.Join(dir, "todos.md"))
	if got := strings.Count(string(data), "- [ ] "); got != 1 {
		t.Fatalf("want 1 todo, got %d:\n%s", got, data)
	}
	srcs := parseSources(string(data))
	if len(srcs) != 2 || srcs[0] != "email:casey" || srcs[1] != "slack:design-review" {
		t.Errorf("want both origins recorded, got %v:\n%s", srcs, data)
	}

	// Re-filing from an origin already recorded changes nothing.
	res, err = s.AddTodo("send finalized data labeling deck to Casey", "email:casey")
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged {
		t.Error("an origin already on the todo should not count as a merge")
	}
}

// Completing a todo must carry its sources into the done file, so a later
// resurrection check can still see where the item came from.
func TestCompleteTodoKeepsSources(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, dir, time.UTC)

	if _, err := s.AddTodo("Reply to Jary about the roadmap", "slack:team-eng/1723456.123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteTodo("Jary"); err != nil {
		t.Fatal(err)
	}
	doneData, err := os.ReadFile(filepath.Join(dir, "done", time.Now().UTC().Format("2006-01-02")+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doneData), "src:slack:team-eng/1723456.123") {
		t.Errorf("source lost on completion: %q", doneData)
	}
}

// The src: tag is metadata, not content: it must not shift dedup either way.
func TestSourceTagDoesNotAffectDedup(t *testing.T) {
	withSrc := "Reply to Jary about the roadmap  <!-- 2026-08-10 src:slack:team-eng -->"
	if got := similarTodo([]Todo{{Text: withSrc}}, "reply to Jary re the roadmap"); got == "" {
		t.Error("a reworded duplicate should still match a source-tagged todo")
	}
	// Two distinct todos from the same channel must not merge on the tag alone.
	a := "Book the Bodrum villa  <!-- 2026-08-10 src:slack:team-eng -->"
	if got := similarTodo([]Todo{{Text: a}}, "Draft the Q3 board deck"); got != "" {
		t.Errorf("distinct todos sharing a source wrongly matched: %q", got)
	}
}

func TestTodosAndNotesUseSeparateDirs(t *testing.T) {
	todosDir := t.TempDir()
	notesDir := t.TempDir()
	s := NewStore(todosDir, notesDir, time.UTC)

	if _, err := s.AddTodo("call dentist", "manual"); err != nil {
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
