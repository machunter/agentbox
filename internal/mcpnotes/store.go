// Package mcpnotes is a local notes/todo MCP server. It stores todos and notes
// as plain markdown in a directory (so they're human-editable and syncable) and
// exposes precise tools to add/list/complete todos and add/search notes —
// reliable structured operations the agent can call without hand-editing files.
//
// agentbox launches it as a subprocess of itself (the "mcp-notes" subcommand).
// Writes are local and reversible, so they need no confirmation.
package mcpnotes

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	todosFile = "todos.md"
	notesFile = "inbox.md"
	doneDir   = "done" // completed todos move here, one file per day (done/<date>.md)
)

// Store reads and writes the markdown todo/note files. Todos (todos.md + done/)
// live in todosDir; free-form notes (inbox.md) live in notesDir — separate so
// the folder names are self-explanatory. The mutex serializes read-modify-write
// operations, since the agent may invoke several notes tools concurrently
// (Claude can emit parallel tool calls) and they all reach this one process.
type Store struct {
	mu       sync.Mutex
	todosDir string
	notesDir string
	loc      *time.Location // timezone for dating todos and done files
}

// NewStore returns a Store with todos under todosDir and notes under notesDir
// (created on first write), dating entries in loc (nil = UTC).
func NewStore(todosDir, notesDir string, loc *time.Location) *Store {
	if loc == nil {
		loc = time.UTC
	}
	return &Store{todosDir: todosDir, notesDir: notesDir, loc: loc}
}

// today is the current date (YYYY-MM-DD) in the store's timezone.
func (s *Store) today() string { return time.Now().In(s.loc).Format("2006-01-02") }

// Todo is a single checkbox item.
type Todo struct {
	Text string
	Done bool
}

// --- file-backed operations ---

func (s *Store) AddTodo(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("todo text is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.archiveDoneLocked(); err != nil {
		return err
	}
	content, err := s.read(s.todosDir, todosFile)
	if err != nil {
		return err
	}
	return s.write(s.todosDir, todosFile, addTodo(content, text, s.today()))
}

func (s *Store) ListTodos(includeDone bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.archiveDoneLocked(); err != nil {
		return "", err
	}
	content, err := s.read(s.todosDir, todosFile)
	if err != nil {
		return "", err
	}
	out := formatTodos(parseTodos(content)) // open todos live in todos.md
	if includeDone {
		// Completed todos were moved to dated done/ files; surface recent ones.
		if done := s.recentDone(7); done != "" {
			out += "\n\nCompleted (recent):\n" + done
		}
	}
	return out, nil
}

// CompleteTodo removes the first open todo matching `match` from todos.md and
// appends it (marked done) to today's done file, so the active list stays short
// and readable. Returns the completed todo's text.
func (s *Store) CompleteTodo(match string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.archiveDoneLocked(); err != nil {
		return "", err
	}
	content, err := s.read(s.todosDir, todosFile)
	if err != nil {
		return "", err
	}
	newContent, removed, text, err := removeTodo(content, match)
	if err != nil {
		return "", err
	}
	if err := s.write(s.todosDir, todosFile, newContent); err != nil {
		return "", err
	}
	doneLine := strings.Replace(removed, "- [ ] ", "- [x] ", 1)
	if err := s.appendDoneLocked([]string{doneLine}); err != nil {
		return "", err
	}
	return text, nil
}

// archiveDoneLocked moves any completed ("- [x]") lines out of todos.md into
// today's done file, so items marked done by hand (not just via complete_todo)
// don't pile up in the active list. The caller must hold s.mu. Returns the
// number archived.
func (s *Store) archiveDoneLocked() (int, error) {
	content, err := s.read(s.todosDir, todosFile)
	if err != nil {
		return 0, err
	}
	keep, done := extractDone(content)
	if len(done) == 0 {
		return 0, nil
	}
	if err := s.write(s.todosDir, todosFile, joinLines(keep)); err != nil {
		return 0, err
	}
	if err := s.appendDoneLocked(done); err != nil {
		return 0, err
	}
	return len(done), nil
}

// appendDoneLocked appends completed lines to today's done/<date>.md file. The
// caller must hold s.mu.
func (s *Store) appendDoneLocked(lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	name := filepath.Join(doneDir, s.today()+".md")
	content, err := s.read(s.todosDir, name)
	if err != nil {
		return err
	}
	for _, ln := range lines {
		content = appendLine(content, strings.TrimRight(ln, " "))
	}
	return s.write(s.todosDir, name, content)
}

func (s *Store) AddNote(text, timestamp string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("note text is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := s.read(s.notesDir, notesFile)
	if err != nil {
		return err
	}
	return s.write(s.notesDir, notesFile, addNote(content, text, timestamp))
}

func (s *Store) SearchNotes(query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := s.read(s.notesDir, notesFile)
	if err != nil {
		return "", err
	}
	matches := searchLines(content, query)
	if len(matches) == 0 {
		return fmt.Sprintf("no notes matching %q", query), nil
	}
	return strings.Join(matches, "\n"), nil
}

func (s *Store) read(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return string(b), nil
}

func (s *Store) write(dir, name, content string) error {
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// recentDone returns the contents of the most recent done files (up to limit),
// oldest first, for the optional "completed" section of ListTodos.
func (s *Store) recentDone(limit int) string {
	matches, _ := filepath.Glob(filepath.Join(s.todosDir, doneDir, "*.md"))
	sort.Strings(matches) // filenames are dates, so this is chronological
	if len(matches) > limit {
		matches = matches[len(matches)-limit:]
	}
	var b strings.Builder
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		b.WriteString(strings.TrimRight(string(data), "\n"))
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// --- pure helpers (independently testable) ---

// extractDone splits content into the lines to keep (open todos and anything
// else) and the completed "- [x]" lines to archive.
func extractDone(content string) (keep, done []string) {
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- [x] ") || strings.HasPrefix(t, "- [X] ") {
			done = append(done, line)
		} else {
			keep = append(keep, line)
		}
	}
	return keep, done
}

// joinLines joins lines with a single trailing newline ("" stays "").
func joinLines(lines []string) string {
	out := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if out != "" {
		out += "\n"
	}
	return out
}

// parseTodos extracts checkbox items from markdown, ignoring other lines.
func parseTodos(content string) []Todo {
	var todos []Todo
	for line := range strings.SplitSeq(content, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "- [ ] "):
			todos = append(todos, Todo{Text: strings.TrimSpace(t[6:]), Done: false})
		case strings.HasPrefix(t, "- [x] "), strings.HasPrefix(t, "- [X] "):
			todos = append(todos, Todo{Text: strings.TrimSpace(t[6:]), Done: true})
		}
	}
	return todos
}

func addTodo(content, text, date string) string {
	line := "- [ ] " + text
	if date != "" {
		line += fmt.Sprintf("  <!-- %s -->", date)
	}
	return appendLine(content, line)
}

func addNote(content, text, timestamp string) string {
	// Collapse internal newlines so each note is one greppable line.
	text = strings.Join(strings.Fields(text), " ")
	return appendLine(content, fmt.Sprintf("- %s  %s", timestamp, text))
}

// removeTodo removes the first open todo whose text contains match (case
// insensitive), returning the new todos.md content, the removed line verbatim,
// and the todo's text — or an error if none match.
func removeTodo(content, match string) (newContent, removed, text string, err error) {
	if strings.TrimSpace(match) == "" {
		return "", "", "", fmt.Errorf("match is empty")
	}
	needle := strings.ToLower(match)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "- [ ] ") {
			continue
		}
		txt := strings.TrimSpace(t[6:])
		if strings.Contains(strings.ToLower(txt), needle) {
			removed = line
			lines = append(lines[:i], lines[i+1:]...)
			nc := strings.TrimRight(strings.Join(lines, "\n"), "\n")
			if nc != "" {
				nc += "\n"
			}
			return nc, removed, txt, nil
		}
	}
	return "", "", "", fmt.Errorf("no open todo matching %q", match)
}

// formatTodos lists the open todos (completed ones live in dated done/ files).
func formatTodos(todos []Todo) string {
	var open []string
	for _, t := range todos {
		if !t.Done {
			open = append(open, "- [ ] "+t.Text)
		}
	}
	if len(open) == 0 {
		return "(no open todos)"
	}
	return strings.Join(open, "\n")
}

func searchLines(content, query string) []string {
	needle := strings.ToLower(query)
	var out []string
	for line := range strings.SplitSeq(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(strings.ToLower(line), needle) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// appendLine adds line to content, ensuring a single trailing newline.
func appendLine(content, line string) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return line + "\n"
	}
	return content + "\n" + line + "\n"
}
