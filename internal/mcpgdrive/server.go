// Package mcpgdrive is a read-only Google Drive MCP server. agentbox launches it
// as a subprocess of itself (the "mcp-gdrive" subcommand) and connects with
// ADK's mcptoolset, giving the agent tools to search Drive, list recent files,
// and read a file's contents — including native Google Docs, which are exported
// to Markdown/CSV/text rather than returned as the useless .gdoc stub an ICS- or
// mount-based approach would surface.
//
// Auth is OAuth 2.0 with a stored refresh token (obtained once via the
// "gdrive-login" subcommand). Tokens stay on the user's machine; the token is
// refreshed silently per run, so the scheduler works headless. Read-only by
// design (drive.readonly scope).
package mcpgdrive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	drive "google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

const (
	defaultLimit = 20
	maxLimit     = 100
	maxDocBytes  = 200 * 1024 // cap a single read_file response
	loopbackAddr = "localhost:8765"
)

// Config holds the OAuth client credentials and stored refresh token, read from
// the environment, plus the timezone for rendering modified times.
type Config struct {
	ClientID     string
	ClientSecret string
	RefreshToken string
	Loc          *time.Location
}

// LoadConfig reads Google Drive OAuth settings from the environment. The second
// return value is false when Drive isn't configured (so the agent skips it).
func LoadConfig() (Config, bool) {
	c := Config{
		ClientID:     strings.TrimSpace(os.Getenv("AGENTBOX_GDRIVE_CLIENT_ID")),
		ClientSecret: strings.TrimSpace(os.Getenv("AGENTBOX_GDRIVE_CLIENT_SECRET")),
		RefreshToken: strings.TrimSpace(os.Getenv("AGENTBOX_GDRIVE_REFRESH_TOKEN")),
		Loc:          time.UTC,
	}
	if tz := os.Getenv("AGENTBOX_TIMEZONE"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			c.Loc = l
		}
	}
	configured := c.ClientID != "" && c.ClientSecret != "" && c.RefreshToken != ""
	return c, configured
}

// Configured reports whether Drive is set up in the environment.
func Configured() bool {
	_, ok := LoadConfig()
	return ok
}

// oauthConfig builds the OAuth2 config for the drive.readonly scope. RedirectURL
// is only used by the login flow.
func (c Config) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{drive.DriveReadonlyScope},
		RedirectURL:  "http://" + loopbackAddr + "/callback",
	}
}

type server struct {
	cfg Config
	svc *drive.Service
}

// newServer builds a Drive service authenticated by the stored refresh token.
// The token source refreshes the short-lived access token automatically.
func newServer(ctx context.Context, cfg Config) (*server, error) {
	ts := cfg.oauthConfig().TokenSource(ctx, &oauth2.Token{RefreshToken: cfg.RefreshToken})
	svc, err := drive.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("drive client: %w", err)
	}
	return &server{cfg: cfg, svc: svc}, nil
}

// Serve runs the Drive MCP server over stdio until the context is cancelled.
func Serve(ctx context.Context) error {
	cfg, ok := LoadConfig()
	if !ok {
		return fmt.Errorf("google drive not configured (set AGENTBOX_GDRIVE_CLIENT_ID/SECRET/REFRESH_TOKEN; run 'agentbox gdrive-login')")
	}
	s, err := newServer(ctx, cfg)
	if err != nil {
		return err
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "agentbox-gdrive", Version: "0.1.0"}, nil)
	s.registerTools(srv)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// CheckConnection is a diagnostic: it lists a few recent files, used by the
// "gdrive-check" subcommand to verify the token without the MCP/agent layers.
func CheckConnection(ctx context.Context) (string, error) {
	cfg, ok := LoadConfig()
	if !ok {
		return "", fmt.Errorf("google drive not configured (set AGENTBOX_GDRIVE_CLIENT_ID/SECRET/REFRESH_TOKEN; run 'agentbox gdrive-login')")
	}
	s, err := newServer(ctx, cfg)
	if err != nil {
		return "", err
	}
	return s.listRecent(ctx, 5)
}

// --- tool inputs ---

type searchInput struct {
	Query string `json:"query" jsonschema:"text to match against file names and full text"`
	Limit int    `json:"limit" jsonschema:"max files to return (default 20, max 100)"`
}

type readInput struct {
	FileID string `json:"file_id" jsonschema:"the Drive file ID to read (from search_drive/list_recent_files)"`
}

type listInput struct {
	Limit int `json:"limit" jsonschema:"max files to return (default 20, max 100)"`
}

func (s *server) registerTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_drive",
		Description: "Search Google Drive by name and full text, newest first. Returns file ID, type, modified time, and name. Use the ID with read_drive_file.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
		out, err := s.search(ctx, in.Query, clampLimit(in.Limit))
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_drive_file",
		Description: "Read a Drive file's contents by ID. Native Google Docs/Sheets/Slides are exported to Markdown/CSV/text; other text files are returned as-is. Binary files (images, etc.) can't be read as text.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, any, error) {
		out, err := s.readFile(ctx, in.FileID)
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_recent_files",
		Description: "List the most recently modified Drive files: ID, type, modified time, and name.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, any, error) {
		out, err := s.listRecent(ctx, clampLimit(in.Limit))
		return textResult(out, err), nil, nil
	})
}

// --- operations ---

const fileFields = "files(id,name,mimeType,modifiedTime,owners/emailAddress)"

func (s *server) search(_ context.Context, query string, limit int) (string, error) {
	r, err := s.svc.Files.List().
		Q(buildQuery(query)).
		PageSize(int64(limit)).
		OrderBy("modifiedTime desc").
		Fields(fileFields).
		SupportsAllDrives(true).IncludeItemsFromAllDrives(true).
		Do()
	if err != nil {
		return "", apiError(err)
	}
	return formatFiles(r.Files, s.cfg.Loc), nil
}

func (s *server) listRecent(_ context.Context, limit int) (string, error) {
	r, err := s.svc.Files.List().
		Q("trashed = false").
		PageSize(int64(limit)).
		OrderBy("modifiedTime desc").
		Fields(fileFields).
		SupportsAllDrives(true).IncludeItemsFromAllDrives(true).
		Do()
	if err != nil {
		return "", apiError(err)
	}
	return formatFiles(r.Files, s.cfg.Loc), nil
}

func (s *server) readFile(_ context.Context, fileID string) (string, error) {
	if strings.TrimSpace(fileID) == "" {
		return "", fmt.Errorf("file_id is empty")
	}
	f, err := s.svc.Files.Get(fileID).Fields("id,name,mimeType").SupportsAllDrives(true).Do()
	if err != nil {
		return "", apiError(err)
	}

	var resp *http.Response
	switch {
	case isGoogleDoc(f.MimeType):
		resp, err = s.svc.Files.Export(fileID, exportMimeFor(f.MimeType)).Download()
	case isTextual(f.MimeType):
		resp, err = s.svc.Files.Get(fileID).SupportsAllDrives(true).Download()
	default:
		return "", fmt.Errorf("%q is %s — not readable as text; it's a binary file", f.Name, f.MimeType)
	}
	if err != nil {
		return "", apiError(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %q: %w", f.Name, err)
	}
	return fmt.Sprintf("# %s\n\n%s", f.Name, clip(string(body))), nil
}

// --- pure helpers (independently testable) ---

// buildQuery turns a user query into a Drive `q`. Empty lists everything
// non-trashed; otherwise it matches name OR full text. Single quotes are escaped
// so they can't break out of the query literal.
func buildQuery(userQuery string) string {
	q := strings.TrimSpace(userQuery)
	if q == "" {
		return "trashed = false"
	}
	esc := strings.ReplaceAll(q, `'`, `\'`)
	return fmt.Sprintf("trashed = false and (name contains '%s' or fullText contains '%s')", esc, esc)
}

// googleDocExports maps native Google MIME types to the text format we export
// them to. Types absent here aren't native Google docs.
var googleDocExports = map[string]string{
	"application/vnd.google-apps.document":     "text/markdown",
	"application/vnd.google-apps.spreadsheet":  "text/csv",
	"application/vnd.google-apps.presentation": "text/plain",
}

func isGoogleDoc(mime string) bool {
	_, ok := googleDocExports[mime]
	return ok
}

// exportMimeFor returns the export target for a native Google MIME type, or ""
// if it isn't one.
func exportMimeFor(mime string) string { return googleDocExports[mime] }

// isTextual reports whether a non-Google file can be downloaded and shown as
// text directly (via alt=media).
func isTextual(mime string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml", "application/x-yaml", "application/yaml", "application/rtf":
		return true
	}
	return false
}

// friendlyType renders a short, human label for a Drive MIME type.
func friendlyType(mime string) string {
	switch mime {
	case "application/vnd.google-apps.document":
		return "doc"
	case "application/vnd.google-apps.spreadsheet":
		return "sheet"
	case "application/vnd.google-apps.presentation":
		return "slides"
	case "application/vnd.google-apps.folder":
		return "folder"
	case "application/pdf":
		return "pdf"
	}
	if i := strings.LastIndexByte(mime, '/'); i >= 0 {
		return mime[i+1:]
	}
	return mime
}

func formatFiles(files []*drive.File, loc *time.Location) string {
	if len(files) == 0 {
		return "(no files)"
	}
	var b strings.Builder
	for _, f := range files {
		mod := f.ModifiedTime
		if t, err := time.Parse(time.RFC3339, f.ModifiedTime); err == nil {
			mod = t.In(loc).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", f.Id, friendlyType(f.MimeType), mod, f.Name)
	}
	return strings.TrimRight(b.String(), "\n")
}

// clip bounds a document body so a huge file doesn't flood the model context.
func clip(s string) string {
	if len(s) > maxDocBytes {
		return s[:maxDocBytes] + fmt.Sprintf("\n\n[truncated at %d bytes]", maxDocBytes)
	}
	return s
}

func clampLimit(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// apiError trims Google API errors to their message; the raw error can be long
// and includes the request URL.
func apiError(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, ", message:"); i >= 0 {
		return fmt.Errorf("drive: %s", strings.TrimSpace(msg[i+len(", message:"):]))
	}
	return fmt.Errorf("drive: %v", err)
}

func textResult(text string, err error) *mcp.CallToolResult {
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "error: " + err.Error()}},
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
