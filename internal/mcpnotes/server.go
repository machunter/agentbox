package mcpnotes

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultDir returns the notes directory: AGENTBOX_NOTES_DIR if set, otherwise
// a "notes" folder under the working directory (which is the mounted workspace
// in the container, so the files are visible/editable/syncable).
func DefaultDir() string {
	if d := os.Getenv("AGENTBOX_NOTES_DIR"); d != "" {
		return d
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return filepath.Join(wd, "notes")
}

// Serve runs the notes MCP server over stdio, storing under dir.
func Serve(ctx context.Context, dir string) error {
	s := &server{store: NewStore(dir)}
	srv := mcp.NewServer(&mcp.Implementation{Name: "agentbox-notes", Version: "0.1.0"}, nil)
	s.registerTools(srv)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

type server struct {
	store *Store
}

// --- tool inputs ---

type addTodoInput struct {
	Text string `json:"text" jsonschema:"the todo to add"`
}

type listTodosInput struct {
	IncludeDone bool `json:"include_done" jsonschema:"also list completed todos (default false)"`
}

type completeTodoInput struct {
	Match string `json:"match" jsonschema:"text of the open todo to mark done (substring match)"`
}

type addNoteInput struct {
	Text string `json:"text" jsonschema:"the note to record"`
}

type searchNotesInput struct {
	Query string `json:"query" jsonschema:"text to search notes for"`
}

func (s *server) registerTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_todo",
		Description: "Add a todo item to the user's todo list.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in addTodoInput) (*mcp.CallToolResult, any, error) {
		if err := s.store.AddTodo(in.Text, today()); err != nil {
			return errResult(err), nil, nil
		}
		return textResult("added todo: " + in.Text), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_todos",
		Description: "List the user's open todos (optionally including completed ones).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listTodosInput) (*mcp.CallToolResult, any, error) {
		out, err := s.store.ListTodos(in.IncludeDone)
		return result(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "complete_todo",
		Description: "Mark an open todo done by matching its text. It's moved out of the active list into a dated done file.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in completeTodoInput) (*mcp.CallToolResult, any, error) {
		done, err := s.store.CompleteTodo(in.Match, today())
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult("completed: " + done), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_note",
		Description: "Record a free-form note to the user's inbox, timestamped.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in addNoteInput) (*mcp.CallToolResult, any, error) {
		if err := s.store.AddNote(in.Text, nowStamp()); err != nil {
			return errResult(err), nil, nil
		}
		return textResult("noted."), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_notes",
		Description: "Search the user's notes inbox for matching lines.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchNotesInput) (*mcp.CallToolResult, any, error) {
		out, err := s.store.SearchNotes(in.Query)
		return result(out, err), nil, nil
	})
}

// notesLoc is the timezone for note/todo timestamps (AGENTBOX_TIMEZONE, default
// UTC), so dates match the user's local day rather than the container's UTC.
var notesLoc = loadLocation()

func loadLocation() *time.Location {
	if tz := os.Getenv("AGENTBOX_TIMEZONE"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			return l
		}
	}
	return time.UTC
}

func today() string    { return time.Now().In(notesLoc).Format("2006-01-02") }
func nowStamp() string { return time.Now().In(notesLoc).Format("2006-01-02 15:04") }

func result(text string, err error) *mcp.CallToolResult {
	if err != nil {
		return errResult(err)
	}
	return textResult(text)
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "error: " + err.Error()}},
	}
}
