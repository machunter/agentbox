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
	"strings"
	"sync"
)

const (
	todosFile = "todos.md"
	notesFile = "inbox.md"
)

// Store reads and writes the markdown todo/note files under a directory. The
// mutex serializes read-modify-write operations, since the agent may invoke
// several notes tools concurrently (Claude can emit parallel tool calls) and
// they all reach this one process.
type Store struct {
	mu  sync.Mutex
	dir string
}

// NewStore returns a Store rooted at dir (created on first write).
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Todo is a single checkbox item.
type Todo struct {
	Text string
	Done bool
}

// --- file-backed operations ---

func (s *Store) AddTodo(text, date string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("todo text is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := s.read(todosFile)
	if err != nil {
		return err
	}
	return s.write(todosFile, addTodo(content, text, date))
}

func (s *Store) ListTodos(includeDone bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := s.read(todosFile)
	if err != nil {
		return "", err
	}
	return formatTodos(parseTodos(content), includeDone), nil
}

func (s *Store) CompleteTodo(match string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := s.read(todosFile)
	if err != nil {
		return "", err
	}
	newContent, completed, err := completeTodo(content, match)
	if err != nil {
		return "", err
	}
	if err := s.write(todosFile, newContent); err != nil {
		return "", err
	}
	return completed, nil
}

func (s *Store) AddNote(text, timestamp string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("note text is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := s.read(notesFile)
	if err != nil {
		return err
	}
	return s.write(notesFile, addNote(content, text, timestamp))
}

func (s *Store) SearchNotes(query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := s.read(notesFile)
	if err != nil {
		return "", err
	}
	matches := searchLines(content, query)
	if len(matches) == 0 {
		return fmt.Sprintf("no notes matching %q", query), nil
	}
	return strings.Join(matches, "\n"), nil
}

func (s *Store) read(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return string(b), nil
}

func (s *Store) write(name, content string) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create notes dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// --- pure helpers (independently testable) ---

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

// completeTodo flips the first open todo whose text contains match (case
// insensitive) to done, returning the updated content and the completed text.
func completeTodo(content, match string) (string, string, error) {
	if strings.TrimSpace(match) == "" {
		return "", "", fmt.Errorf("match is empty")
	}
	needle := strings.ToLower(match)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "- [ ] ") {
			continue
		}
		text := strings.TrimSpace(t[6:])
		if strings.Contains(strings.ToLower(text), needle) {
			lines[i] = strings.Replace(line, "- [ ] ", "- [x] ", 1)
			return strings.Join(lines, "\n"), text, nil
		}
	}
	return "", "", fmt.Errorf("no open todo matching %q", match)
}

func formatTodos(todos []Todo, includeDone bool) string {
	var open, done []string
	for _, t := range todos {
		if t.Done {
			done = append(done, "- [x] "+t.Text)
		} else {
			open = append(open, "- [ ] "+t.Text)
		}
	}
	var b strings.Builder
	if len(open) == 0 {
		b.WriteString("(no open todos)")
	} else {
		b.WriteString(strings.Join(open, "\n"))
	}
	if includeDone && len(done) > 0 {
		b.WriteString("\n\nCompleted:\n")
		b.WriteString(strings.Join(done, "\n"))
	}
	return b.String()
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
