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
	"unicode"
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

// AddResult reports what AddTodo did with an item.
type AddResult struct {
	Added  bool   // a new todo was written
	Dup    string // the existing todo this matched, when not added
	Merged bool   // the item's source was recorded on that existing todo
}

// AddTodo appends a new open todo, tagged with where it came from (source may be
// empty when unknown). It writes no new item and returns Added=false (with Dup
// set to the conflicting todo's text) when the item is a near-duplicate of either
// an existing open todo OR one completed within the dedup window. The open check
// stops the same item arriving from two sources (email update + Slack mention);
// the completed check stops "resurrection" — a source message re-seen during a
// multi-day sweep being re-filed after its todo was already done and archived out
// of the open list.
//
// Matching an open todo still records the new source on it (Merged=true), since
// the same request can legitimately arrive by mail and in Slack and a reply at
// either place resolves it. That is why sources are a list, not one field.
func (s *Store) AddTodo(text, source string) (AddResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return AddResult{}, fmt.Errorf("todo text is empty")
	}
	source = sanitizeSource(source)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.archiveDoneLocked(); err != nil {
		return AddResult{}, err
	}
	content, err := s.read(s.todosDir, todosFile)
	if err != nil {
		return AddResult{}, err
	}
	if existing := similarTodo(parseTodos(content), text); existing != "" {
		updated, changed := addSourceToLine(content, existing, source)
		if changed {
			if err := s.write(s.todosDir, todosFile, updated); err != nil {
				return AddResult{}, err
			}
		}
		return AddResult{Dup: existing, Merged: changed}, nil
	}
	if existing := similarText(s.recentDoneTextsLocked(dedupWindowDays), text); existing != "" {
		return AddResult{Dup: existing}, nil
	}
	var sources []string
	if source != "" {
		sources = []string{source}
	}
	if err := s.write(s.todosDir, todosFile, addTodo(content, text, s.today(), sources)); err != nil {
		return AddResult{}, err
	}
	return AddResult{Added: true}, nil
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

// dupThreshold is the token-overlap fraction above which a new todo is treated
// as a near-duplicate of an existing open one. Deliberately high: dedup must not
// silently drop genuinely distinct todos, so it fires only on strong overlap.
const dupThreshold = 0.8

// todoStopwords are low-signal words ignored when comparing todos, so overlap
// reflects the actual subject (people, topics) rather than filler.
var todoStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "to": true, "and": true, "or": true,
	"of": true, "for": true, "with": true, "about": true, "re": true, "in": true,
	"on": true, "at": true, "is": true, "are": true, "be": true, "regarding": true,
	"our": true, "my": true, "from": true, "this": true, "that": true,
}

// todoTokens normalizes a todo's text into a set of content tokens for
// comparison: it drops the trailing <!-- date --> marker, lowercases, splits on
// non-alphanumerics, and removes stopwords and single characters.
func todoTokens(text string) map[string]bool {
	if i := strings.Index(text, "<!--"); i >= 0 {
		text = text[:i]
	}
	set := map[string]bool{}
	for _, tok := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if todoStopwords[tok] {
			continue
		}
		// Drop single *letters* (low signal), but keep numbers — a lone digit
		// can be what distinguishes two otherwise-identical items.
		if len(tok) <= 1 && !isNumeric(tok) {
			continue
		}
		set[tok] = true
	}
	return set
}

// isNumeric reports whether tok is all digits.
func isNumeric(tok string) bool {
	for _, r := range tok {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return tok != ""
}

// containment returns the fraction of a's tokens that also appear in b.
func containment(a, b map[string]bool) float64 {
	if len(a) == 0 {
		return 0
	}
	hit := 0
	for t := range a {
		if b[t] {
			hit++
		}
	}
	return float64(hit) / float64(len(a))
}

// similarTodo returns the text of an open todo that is a near-duplicate of
// candidate, or "" if none.
func similarTodo(todos []Todo, candidate string) string {
	var open []string
	for _, t := range todos {
		if !t.Done {
			open = append(open, t.Text)
		}
	}
	return similarText(open, candidate)
}

// similarText returns the first entry in existing that is a near-duplicate of
// candidate, or "". It matches when either side's content tokens are mostly
// contained in the other's, so a shorter rephrase of a longer item still counts
// — while the high threshold keeps distinct todos from being dropped.
func similarText(existing []string, candidate string) string {
	cand := todoTokens(candidate)
	if len(cand) == 0 {
		return ""
	}
	for _, ex := range existing {
		et := todoTokens(ex)
		if len(et) == 0 {
			continue
		}
		if containment(cand, et) >= dupThreshold || containment(et, cand) >= dupThreshold {
			return ex
		}
	}
	return ""
}

// dedupWindowDays bounds how far back completed todos are considered when
// deduping a new one. Long enough to cover a multi-day email/Slack sweep (so a
// re-seen message isn't re-filed after its todo was completed), but bounded so a
// genuinely recurring task can legitimately reappear after the window.
const dedupWindowDays = 30

// recentDoneTextsLocked returns the texts of todos completed within the last
// `days`, read from the dated done/ files (named by completion date). The caller
// must hold s.mu.
func (s *Store) recentDoneTextsLocked(days int) []string {
	matches, _ := filepath.Glob(filepath.Join(s.todosDir, doneDir, "*.md"))
	cutoff := time.Now().In(s.loc).AddDate(0, 0, -days)
	var texts []string
	for _, m := range matches {
		date := strings.TrimSuffix(filepath.Base(m), ".md")
		if d, err := time.ParseInLocation("2006-01-02", date, s.loc); err == nil && d.Before(cutoff) {
			continue // completed before the window
		}
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		for _, t := range parseTodos(string(data)) {
			texts = append(texts, t.Text)
		}
	}
	return texts
}

// srcPrefix marks the source list inside a todo's trailing HTML comment:
//
//	- [ ] Reply to Jary about the roadmap  <!-- 2026-08-10 src:slack:team-eng/1723456.123 -->
//
// Sources say where the item came from, so a later sweep looks for the reply in
// the place that asked for it rather than always scanning Sent mail. Todos
// written before this existed carry a date and no src, which readers treat as
// "unknown origin".
const srcPrefix = "src:"

// sanitizeSource normalizes a source label so it survives round-tripping through
// the one-line HTML comment: no whitespace, no commas (the list separator), and
// no comment terminator.
// Strip the comment terminator before collapsing whitespace, so removing it
// can't leave a doubled separator behind ("a --> b" becomes "a_b", not "a__b").
func sanitizeSource(s string) string {
	s = strings.ReplaceAll(s, "-->", "")
	s = strings.ReplaceAll(s, ",", ";")
	return strings.Join(strings.Fields(s), "_")
}

// todoComment returns the body of a todo's trailing HTML comment, without the
// delimiters, or "" when there is none.
func todoComment(text string) string {
	i := strings.Index(text, "<!--")
	if i < 0 {
		return ""
	}
	rest := text[i+len("<!--"):]
	j := strings.Index(rest, "-->")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// parseSources returns the sources recorded on a todo, or nil when its origin is
// unknown (including every todo written before sources existed).
func parseSources(text string) []string {
	for _, f := range strings.Fields(todoComment(text)) {
		if !strings.HasPrefix(f, srcPrefix) {
			continue
		}
		var out []string
		for _, s := range strings.Split(strings.TrimPrefix(f, srcPrefix), ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// todoDate returns the capture date recorded on a todo, or "".
func todoDate(text string) string {
	for _, f := range strings.Fields(todoComment(text)) {
		if !strings.HasPrefix(f, srcPrefix) {
			return f
		}
	}
	return ""
}

// metaComment renders a todo's trailing comment from its capture date and
// sources, or "" when there is nothing to record.
func metaComment(date string, sources []string) string {
	var parts []string
	if date != "" {
		parts = append(parts, date)
	}
	if len(sources) > 0 {
		parts = append(parts, srcPrefix+strings.Join(sources, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	return "<!-- " + strings.Join(parts, " ") + " -->"
}

// withSources rewrites a todo line to record exactly sources, keeping its text
// and capture date.
func withSources(line string, sources []string) string {
	body := line
	if i := strings.Index(line, "<!--"); i >= 0 {
		body = strings.TrimRight(line[:i], " ")
	}
	if c := metaComment(todoDate(line), sources); c != "" {
		return body + "  " + c
	}
	return body
}

// mergeSource appends source to sources when it isn't already there, reporting
// whether anything changed. An empty source never changes anything.
func mergeSource(sources []string, source string) ([]string, bool) {
	if source == "" {
		return sources, false
	}
	for _, s := range sources {
		if strings.EqualFold(s, source) {
			return sources, false
		}
	}
	return append(sources, source), true
}

// addSourceToLine records source on the open todo whose text is todoText,
// returning the updated content and whether it changed.
func addSourceToLine(content, todoText, source string) (string, bool) {
	if source == "" {
		return content, false
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "- [ ] ") || strings.TrimSpace(t[6:]) != todoText {
			continue
		}
		sources, changed := mergeSource(parseSources(t), source)
		if !changed {
			return content, false
		}
		lines[i] = withSources(line, sources)
		return joinLines(lines), true
	}
	return content, false
}

func addTodo(content, text, date string, sources []string) string {
	line := "- [ ] " + text
	if c := metaComment(date, sources); c != "" {
		line += "  " + c
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
