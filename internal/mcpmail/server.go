// Package mcpmail is a read-only email MCP server over IMAP. agentbox launches
// it as a subprocess of itself (the "mcp-mail" subcommand) and connects with
// ADK's mcptoolset, giving the agent tools to list, search, and read mail.
//
// It is read-only by design: sending (SMTP) is a separate, confirmation-gated
// capability to be added later. Credentials come from the environment, so they
// inherit into this subprocess from the parent.
package mcpmail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/mail"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultMailbox = "INBOX"
	defaultLimit   = 10
	maxLimit       = 50
	maxBodyBytes   = 64 * 1024
)

// Config holds IMAP connection settings, read from the environment.
type Config struct {
	Host string
	Port string
	User string
	Pass string
	// SinceDays is the default lookback window: list/search only return mail from
	// the last N days. 0 means no date filter (count-based only).
	SinceDays int
}

// LoadConfig reads IMAP settings from the environment. The second return value
// is false when email isn't configured (so the agent can skip the connector).
func LoadConfig() (Config, bool) {
	c := Config{
		Host: os.Getenv("AGENTBOX_IMAP_HOST"),
		Port: os.Getenv("AGENTBOX_IMAP_PORT"),
		User: os.Getenv("AGENTBOX_IMAP_USER"),
		Pass: os.Getenv("AGENTBOX_IMAP_PASS"),
	}
	if c.Port == "" {
		c.Port = "993"
	}
	if n, err := strconv.Atoi(os.Getenv("AGENTBOX_EMAIL_SINCE_DAYS")); err == nil && n > 0 {
		c.SinceDays = n
	}
	configured := c.Host != "" && c.User != "" && c.Pass != ""
	return c, configured
}

// effectiveSince resolves the lookback cutoff: a per-call toolDays (> 0) wins,
// else the configured default. Returns the zero time when no filter applies.
func (s *server) effectiveSince(toolDays int) time.Time {
	days := toolDays
	if days <= 0 {
		days = s.cfg.SinceDays
	}
	if days <= 0 {
		return time.Time{}
	}
	return time.Now().AddDate(0, 0, -days)
}

// Configured reports whether email is set up in the environment.
func Configured() bool {
	_, ok := LoadConfig()
	return ok
}

// server connects to IMAP fresh for each tool call. A long-lived connection is
// avoided deliberately: it sits idle between the server starting and the first
// tool call (while the agent thinks), and the server then drops it. Per-call
// connections are immediately used, which is reliable.
type server struct {
	cfg Config
}

// Serve runs the email MCP server over stdio until the context is cancelled. It
// writes nothing to stdout but the MCP protocol. Credentials are validated per
// call (see withClient), not held open here.
func Serve(ctx context.Context) error {
	cfg, ok := LoadConfig()
	if !ok {
		return fmt.Errorf("email not configured (set AGENTBOX_IMAP_HOST/USER/PASS)")
	}
	s := &server{cfg: cfg}
	srv := mcp.NewServer(&mcp.Implementation{Name: "agentbox-mail", Version: "0.1.0"}, nil)
	s.registerTools(srv)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// CheckConnection is a diagnostic: it connects and lists a few INBOX messages,
// returning the result as plain text. Used by the "mail-check" subcommand to
// test IMAP reachability without the MCP/agent layers.
func CheckConnection(ctx context.Context) (string, error) {
	cfg, ok := LoadConfig()
	if !ok {
		return "", fmt.Errorf("email not configured (set AGENTBOX_IMAP_HOST/USER/PASS)")
	}
	s := &server{cfg: cfg}
	return s.listRecent(ctx, defaultMailbox, 3, 0)
}

// withClient dials and logs in, runs fn, then logs out and closes — one fresh
// connection per call.
func (s *server) withClient(fn func(*imapclient.Client) (string, error)) (string, error) {
	var opts *imapclient.Options
	if os.Getenv("AGENTBOX_IMAP_DEBUG") != "" {
		opts = &imapclient.Options{DebugWriter: os.Stderr} // raw protocol trace to stderr
	}
	client, err := imapclient.DialTLS(s.cfg.Host+":"+s.cfg.Port, opts)
	if err != nil {
		return "", fmt.Errorf("imap dial: %w", err)
	}
	defer client.Close()
	if err := client.Login(s.cfg.User, s.cfg.Pass).Wait(); err != nil {
		return "", fmt.Errorf("imap login: %w", err)
	}
	// Wrap in a closure: `defer client.Logout().Wait()` would evaluate
	// client.Logout() immediately, sending LOGOUT before fn runs.
	defer func() { _ = client.Logout().Wait() }()
	return fn(client)
}

// --- tool inputs ---

type listInput struct {
	Mailbox   string `json:"mailbox" jsonschema:"mailbox to list; empty means INBOX"`
	Limit     int    `json:"limit" jsonschema:"max messages to return (default 10, max 50)"`
	SinceDays int    `json:"since_days" jsonschema:"only include mail from the last N days; 0 uses the configured default"`
}

type searchInput struct {
	Query     string `json:"query" jsonschema:"text to search for across headers and body"`
	Mailbox   string `json:"mailbox" jsonschema:"mailbox to search; empty means INBOX"`
	Limit     int    `json:"limit" jsonschema:"max messages to return (default 10, max 50)"`
	SinceDays int    `json:"since_days" jsonschema:"only include mail from the last N days; 0 uses the configured default"`
}

type readInput struct {
	UID     uint32 `json:"uid" jsonschema:"the UID of the message to read (from list/search results)"`
	Mailbox string `json:"mailbox" jsonschema:"mailbox the message is in; empty means INBOX"`
}

func (s *server) registerTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_recent_emails",
		Description: "List the most recent emails in a mailbox (newest first): UID, date, from, and subject.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, any, error) {
		out, err := s.listRecent(ctx, mailboxOr(in.Mailbox), clampLimit(in.Limit), in.SinceDays)
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_emails",
		Description: "Search a mailbox for messages matching a text query, returning UID, date, from, and subject (newest first).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
		out, err := s.search(ctx, in.Query, mailboxOr(in.Mailbox), clampLimit(in.Limit), in.SinceDays)
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_email",
		Description: "Read a single email by UID, returning its headers and plain-text body.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, any, error) {
		out, err := s.read(ctx, in.UID, mailboxOr(in.Mailbox))
		return textResult(out, err), nil, nil
	})
}

// --- IMAP operations (thin; integration-tested with a real server) ---

func (s *server) listRecent(_ context.Context, mailbox string, limit, sinceDays int) (string, error) {
	since := s.effectiveSince(sinceDays)
	return s.withClient(func(c *imapclient.Client) (string, error) {
		sel, err := c.Select(mailbox, nil).Wait()
		if err != nil {
			return "", fmt.Errorf("select %q: %w", mailbox, err)
		}
		if sel.NumMessages == 0 {
			return "(mailbox is empty)", nil
		}

		// With a date filter, find recent UIDs by SINCE; otherwise take the last
		// `limit` messages by sequence number.
		if !since.IsZero() {
			data, err := c.UIDSearch(&imap.SearchCriteria{Since: since}, nil).Wait()
			if err != nil {
				return "", fmt.Errorf("search: %w", err)
			}
			uids := data.AllUIDs()
			if len(uids) == 0 {
				return fmt.Sprintf("(no messages in the last %d days)", daysSince(since)), nil
			}
			if len(uids) > limit {
				uids = uids[len(uids)-limit:]
			}
			msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{Envelope: true, UID: true}).Collect()
			if err != nil {
				return "", fmt.Errorf("fetch: %w", err)
			}
			return formatList(msgs), nil
		}

		start := uint32(1)
		if sel.NumMessages > uint32(limit) {
			start = sel.NumMessages - uint32(limit) + 1
		}
		var seq imap.SeqSet
		seq.AddRange(start, sel.NumMessages)

		msgs, err := c.Fetch(seq, &imap.FetchOptions{Envelope: true, UID: true}).Collect()
		if err != nil {
			return "", fmt.Errorf("fetch: %w", err)
		}
		return formatList(msgs), nil
	})
}

func (s *server) search(_ context.Context, query, mailbox string, limit, sinceDays int) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is empty")
	}
	since := s.effectiveSince(sinceDays)
	return s.withClient(func(c *imapclient.Client) (string, error) {
		if _, err := c.Select(mailbox, nil).Wait(); err != nil {
			return "", fmt.Errorf("select %q: %w", mailbox, err)
		}
		criteria := &imap.SearchCriteria{Text: []string{query}}
		if !since.IsZero() {
			criteria.Since = since
		}
		data, err := c.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return "", fmt.Errorf("search: %w", err)
		}
		uids := data.AllUIDs()
		if len(uids) == 0 {
			return fmt.Sprintf("no emails matching %q", query), nil
		}
		if len(uids) > limit { // keep the most recent (highest UIDs)
			uids = uids[len(uids)-limit:]
		}
		msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{Envelope: true, UID: true}).Collect()
		if err != nil {
			return "", fmt.Errorf("fetch: %w", err)
		}
		return formatList(msgs), nil
	})
}

func (s *server) read(_ context.Context, uid uint32, mailbox string) (string, error) {
	return s.withClient(func(c *imapclient.Client) (string, error) {
		if _, err := c.Select(mailbox, nil).Wait(); err != nil {
			return "", fmt.Errorf("select %q: %w", mailbox, err)
		}
		msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
			BodySection: []*imap.FetchItemBodySection{{Peek: true}},
		}).Collect()
		if err != nil {
			return "", fmt.Errorf("fetch: %w", err)
		}
		if len(msgs) == 0 || len(msgs[0].BodySection) == 0 {
			return "", fmt.Errorf("no message with UID %d", uid)
		}
		pm, err := parseMessage(msgs[0].BodySection[0].Bytes)
		if err != nil {
			return "", fmt.Errorf("parse message: %w", err)
		}
		return pm.String(), nil
	})
}

// --- pure helpers (independently testable) ---

// parsedMail is a flattened, display-ready view of an email.
type parsedMail struct {
	From    string
	To      string
	Subject string
	Date    string
	Body    string
}

func (m parsedMail) String() string {
	body := m.Body
	if len(body) > maxBodyBytes {
		body = body[:maxBodyBytes] + fmt.Sprintf("\n\n[truncated at %d bytes]", maxBodyBytes)
	}
	return fmt.Sprintf("From: %s\nTo: %s\nDate: %s\nSubject: %s\n\n%s",
		m.From, m.To, m.Date, m.Subject, body)
}

// parseMessage extracts headers and the plain-text body from a raw RFC 822
// message, walking MIME parts for the first text/plain content.
func parseMessage(raw []byte) (parsedMail, error) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return parsedMail{}, err
	}
	pm := parsedMail{
		From: mr.Header.Get("From"),
		To:   mr.Header.Get("To"),
		Date: mr.Header.Get("Date"),
	}
	pm.Subject, _ = mr.Header.Text("Subject")

	var body strings.Builder
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break // tolerate malformed trailing parts
		}
		if h, ok := part.Header.(*mail.InlineHeader); ok {
			ct, _, _ := h.ContentType()
			if strings.HasPrefix(ct, "text/plain") {
				b, _ := io.ReadAll(part.Body)
				body.Write(b)
				body.WriteString("\n")
			}
		}
	}
	pm.Body = strings.TrimSpace(body.String())
	if pm.Body == "" {
		pm.Body = "(no text/plain body found)"
	}
	return pm, nil
}

// formatList renders fetched messages newest-first as one line each.
func formatList(msgs []*imapclient.FetchMessageBuffer) string {
	if len(msgs) == 0 {
		return "(no messages)"
	}
	var b strings.Builder
	for i := len(msgs) - 1; i >= 0; i-- { // newest first
		m := msgs[i]
		env := m.Envelope
		date, subject, from := "?", "(no subject)", "(unknown)"
		if env != nil {
			if !env.Date.IsZero() {
				date = env.Date.Format("2006-01-02 15:04")
			}
			if env.Subject != "" {
				subject = env.Subject
			}
			from = formatAddresses(env.From)
		}
		fmt.Fprintf(&b, "UID %d\t%s\t%s\t%s\n", m.UID, date, from, subject)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatAddresses renders IMAP addresses as "Name <mailbox@host>" joined.
func formatAddresses(addrs []imap.Address) string {
	if len(addrs) == 0 {
		return "(unknown)"
	}
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		email := a.Mailbox + "@" + a.Host
		if a.Name != "" {
			parts = append(parts, fmt.Sprintf("%s <%s>", a.Name, email))
		} else {
			parts = append(parts, email)
		}
	}
	return strings.Join(parts, ", ")
}

func mailboxOr(m string) string {
	if m == "" {
		return defaultMailbox
	}
	return m
}

// daysSince rounds the elapsed days since t, for human-readable messages.
func daysSince(t time.Time) int {
	return int(time.Since(t).Hours()/24 + 0.5)
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

func textResult(text string, err error) *mcp.CallToolResult {
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "error: " + err.Error()}},
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
