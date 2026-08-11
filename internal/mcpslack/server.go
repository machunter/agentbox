// Package mcpslack is a read-only Slack MCP server over the Slack Web API.
// agentbox launches it as a subprocess of itself (the "mcp-slack" subcommand)
// and connects with ADK's mcptoolset, giving the agent tools to list channels,
// read channel history and threads, and search messages.
//
// It is read-only by design: posting (chat.postMessage) is a separate,
// confirmation-gated capability to be added later, mirroring the email server.
// Authentication is a single configured token (AGENTBOX_SLACK_TOKEN) — no OAuth
// dance — consistent with the project's "configured credential" stance. A user
// token (xoxp-) is needed for search.messages; a bot token (xoxb-) works for the
// rest given the right scopes.
package mcpslack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	apiBase             = "https://slack.com/api/"
	defaultHistoryLimit = 30
	maxLimit            = 200
	defaultSearchCount  = 20
	maxFetchBytes       = 8 << 20 // cap a single API response
	defaultFetchTimeout = 30 * time.Second

	// Rate-limit handling. Slack's Tier 2/3 windows clear in well under a
	// minute, so a couple of bounded waits ride out a burst; past that the
	// error belongs to the caller rather than another minute of sleeping.
	maxRateLimitRetries = 2
	defaultRetryAfter   = 5 * time.Second  // when Slack sends no Retry-After
	maxRetryAfter       = 30 * time.Second // ceiling on a single wait
)

// fetchTimeout is the per-request HTTP timeout, overridable via
// AGENTBOX_SLACK_TIMEOUT (seconds).
func fetchTimeout() time.Duration {
	if v := os.Getenv("AGENTBOX_SLACK_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultFetchTimeout
}

// Config holds the Slack token, the default history lookback window, and the
// timezone used to render message timestamps.
type Config struct {
	Token string
	// LookbackDays bounds channel history by time; 0 means count-based only.
	LookbackDays int
	Loc          *time.Location
}

// LoadConfig reads Slack settings from the environment. The second return value
// is false when Slack isn't configured (so the agent can skip the connector).
func LoadConfig() (Config, bool) {
	c := Config{Token: strings.TrimSpace(os.Getenv("AGENTBOX_SLACK_TOKEN")), Loc: time.UTC}
	if n, err := strconv.Atoi(os.Getenv("AGENTBOX_SLACK_LOOKBACK_DAYS")); err == nil && n > 0 {
		c.LookbackDays = n
	}
	if tz := os.Getenv("AGENTBOX_TIMEZONE"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			c.Loc = l
		}
	}
	return c, c.Token != ""
}

// Configured reports whether Slack is set up in the environment.
func Configured() bool {
	_, ok := LoadConfig()
	return ok
}

type server struct {
	cfg    Config
	client *http.Client
	base   string // API base URL (overridable in tests)

	// In-process caches so the several tool calls in one run don't re-resolve
	// the same channels/users. The server is short-lived (one run).
	mu        sync.Mutex
	userName  map[string]string    // user ID -> display name
	chanByID  map[string]string    // channel ID -> name
	chanLists map[string][]channel // cached users.conversations results, by normalized types
}

// Serve runs the Slack MCP server over stdio until the context is cancelled.
func Serve(ctx context.Context) error {
	cfg, ok := LoadConfig()
	if !ok {
		return fmt.Errorf("slack not configured (set AGENTBOX_SLACK_TOKEN)")
	}
	s := newServer(cfg)
	srv := mcp.NewServer(&mcp.Implementation{Name: "agentbox-slack", Version: "0.1.0"}, nil)
	s.registerTools(srv)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

func newServer(cfg Config) *server {
	return &server{
		cfg:       cfg,
		client:    &http.Client{Timeout: fetchTimeout()},
		base:      apiBase,
		userName:  map[string]string{},
		chanByID:  map[string]string{},
		chanLists: map[string][]channel{},
	}
}

// CheckConnection is a diagnostic: it calls auth.test and reports the workspace
// and identity. Used by the "slack-check" subcommand to verify the token
// without the MCP/agent layers (auth.test needs no special scopes).
func CheckConnection(ctx context.Context) (string, error) {
	cfg, ok := LoadConfig()
	if !ok {
		return "", fmt.Errorf("slack not configured (set AGENTBOX_SLACK_TOKEN)")
	}
	s := newServer(cfg)
	var out authTestResp
	if err := s.call(ctx, "auth.test", nil, &out); err != nil {
		return "", err
	}
	return fmt.Sprintf("connected to %s as %s", out.Team, out.User), nil
}

// --- tool inputs ---

type listChannelsInput struct {
	Types string `json:"types" jsonschema:"comma-separated channel types: public_channel, private_channel, mpim, im (default public+private)"`
	Limit int    `json:"limit" jsonschema:"max channels to return (default 200)"`
}

type readChannelInput struct {
	Channel   string `json:"channel" jsonschema:"channel name (with or without #) or ID to read"`
	Limit     int    `json:"limit" jsonschema:"max messages to return (default 30, max 200)"`
	SinceDays int    `json:"since_days" jsonschema:"only include messages from the last N days; 0 uses the configured default"`
}

type readThreadInput struct {
	Channel  string `json:"channel" jsonschema:"channel name (with or without #) or ID the thread is in"`
	ThreadTS string `json:"thread_ts" jsonschema:"the ts of the thread's parent message (from read_channel)"`
}

type searchInput struct {
	Query string `json:"query" jsonschema:"text to search for across messages (requires a user token)"`
	Count int    `json:"count" jsonschema:"max matches to return (default 20, max 200)"`
}

func (s *server) registerTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_channels",
		Description: "List the Slack channels the user is a member of (and DMs/groups if requested): ID, name, and whether private. Use to find the channel to read. Channels the user hasn't joined are not listed and can't be read.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listChannelsInput) (*mcp.CallToolResult, any, error) {
		out, err := s.listChannels(ctx, in.Types, clampLimit(in.Limit, maxLimit))
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_channel",
		Description: "Read recent messages in a Slack channel (chronological), with sender and time. Accepts a channel name or ID.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readChannelInput) (*mcp.CallToolResult, any, error) {
		out, err := s.readChannel(ctx, in.Channel, clampLimit(in.Limit, defaultHistoryLimit), in.SinceDays)
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_thread",
		Description: "Read all replies in a Slack thread, given the channel and the parent message's ts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readThreadInput) (*mcp.CallToolResult, any, error) {
		out, err := s.readThread(ctx, in.Channel, in.ThreadTS)
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_messages",
		Description: "Search Slack messages matching a query, newest first, with channel, sender, and time. Requires a user token (xoxp-).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
		out, err := s.searchMessages(ctx, in.Query, clampLimit(in.Count, defaultSearchCount))
		return textResult(out, err), nil, nil
	})
}

// --- operations ---

func (s *server) listChannels(ctx context.Context, types string, limit int) (string, error) {
	chans, err := s.channels(ctx, types)
	if err != nil {
		return "", err
	}
	if len(chans) > limit {
		chans = chans[:limit]
	}
	return formatChannels(chans), nil
}

func (s *server) readChannel(ctx context.Context, ref string, limit, sinceDays int) (string, error) {
	id, err := s.resolveChannelID(ctx, ref)
	if err != nil {
		return "", err
	}
	params := url.Values{"channel": {id}, "limit": {strconv.Itoa(limit)}}
	if oldest := s.oldest(sinceDays); oldest != "" {
		params.Set("oldest", oldest)
	}
	var out historyResp
	if err := s.call(ctx, "conversations.history", params, &out); err != nil {
		return "", err
	}
	if len(out.Messages) == 0 {
		return "(no messages)", nil
	}
	return formatMessages(reversed(out.Messages), s.nameResolver(ctx), s.cfg.Loc), nil
}

func (s *server) readThread(ctx context.Context, ref, threadTS string) (string, error) {
	if strings.TrimSpace(threadTS) == "" {
		return "", fmt.Errorf("thread_ts is empty")
	}
	id, err := s.resolveChannelID(ctx, ref)
	if err != nil {
		return "", err
	}
	params := url.Values{"channel": {id}, "ts": {threadTS}, "limit": {strconv.Itoa(maxLimit)}}
	var out historyResp
	if err := s.call(ctx, "conversations.replies", params, &out); err != nil {
		return "", err
	}
	if len(out.Messages) == 0 {
		return "(no replies)", nil
	}
	return formatMessages(out.Messages, s.nameResolver(ctx), s.cfg.Loc), nil
}

func (s *server) searchMessages(ctx context.Context, query string, count int) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is empty")
	}
	params := url.Values{"query": {query}, "count": {strconv.Itoa(count)}, "sort": {"timestamp"}}
	var out searchResp
	if err := s.call(ctx, "search.messages", params, &out); err != nil {
		return "", err
	}
	if len(out.Messages.Matches) == 0 {
		return fmt.Sprintf("no messages matching %q", query), nil
	}
	return formatMatches(out.Messages.Matches, s.cfg.Loc), nil
}

// channels returns the caller's conversations of the given types, cached for the
// run. The cache is keyed by types: it used to hold one list for any request, so
// a list_channels call for public+private would satisfy a later lookup asking
// for im/mpim and a DM would come back "not found" despite existing.
func (s *server) channels(ctx context.Context, types string) ([]channel, error) {
	types = normalizeTypes(types)

	s.mu.Lock()
	if cached, ok := s.chanLists[types]; ok {
		defer s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	all, err := s.listConversations(ctx, types)
	if err != nil && isMissingScope(err) && types != "public_channel" {
		// The token lacks the scope for some channel types (e.g. groups:read for
		// private channels). Fall back to public channels so listing and channel
		// resolution still work with a minimally-scoped token.
		fmt.Fprintf(os.Stderr, "mcp-slack: %v — listing public channels only (add groups:read/im:read/mpim:read for private channels and DMs)\n", err)
		all, err = s.listConversations(ctx, "public_channel")
	}
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	// Cached under the requested types, including after a missing_scope
	// fallback, so the same request doesn't re-attempt the call that just failed.
	s.chanLists[types] = all
	for _, c := range all {
		s.chanByID[c.ID] = c.displayName()
	}
	s.mu.Unlock()
	return all, nil
}

// normalizeTypes canonicalizes a comma-separated conversation-type list so it
// works as a cache key: defaulted when empty, lowercased, deduped, and sorted,
// so "im,mpim" and "mpim, im" are one entry rather than two.
func normalizeTypes(types string) string {
	seen := map[string]bool{}
	var out []string
	for _, t := range strings.Split(strings.ToLower(types), ",") {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return "public_channel,private_channel"
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// listConversations pages through users.conversations for the given types: the
// conversations the token's own identity belongs to. conversations.list would
// return every channel in the workspace regardless of membership, which is the
// wrong answer for "my channels" and, in a large org, pages enough times to hit
// Slack's rate limit on a routine briefing.
func (s *server) listConversations(ctx context.Context, types string) ([]channel, error) {
	var all []channel
	cursor := ""
	for {
		params := url.Values{"types": {types}, "limit": {"200"}, "exclude_archived": {"true"}}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var out conversationsListResp
		if err := s.call(ctx, "users.conversations", params, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Channels...)
		cursor = out.ResponseMetadata.NextCursor
		if cursor == "" || len(all) >= maxLimit {
			break
		}
	}
	return all, nil
}

// isMissingScope reports whether err is a Slack missing_scope error.
func isMissingScope(err error) bool {
	return err != nil && strings.Contains(err.Error(), "missing_scope")
}

// resolveChannelID maps a channel reference (name, #name, or ID) to its ID.
// Slack channel/DM IDs start with C, G, or D; anything else is treated as a name
// and looked up in the (cached) channel list.
func (s *server) resolveChannelID(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ref), "#"))
	if ref == "" {
		return "", fmt.Errorf("channel is empty")
	}
	if looksLikeChannelID(ref) {
		return ref, nil
	}
	chans, err := s.channels(ctx, "public_channel,private_channel,mpim,im")
	if err != nil {
		return "", err
	}
	for _, c := range chans {
		if strings.EqualFold(c.Name, ref) {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("no channel named %q among the ones you're in (list_channels shows them; join it in Slack to make it readable)", ref)
}

// nameResolver returns a function that maps a user ID to a display name, caching
// lookups for the run. On any failure it falls back to the raw ID so formatting
// never blocks on a bad lookup.
func (s *server) nameResolver(ctx context.Context) func(string) string {
	return func(id string) string {
		if id == "" {
			return ""
		}
		s.mu.Lock()
		name, ok := s.userName[id]
		s.mu.Unlock()
		if ok {
			return name
		}
		var out userInfoResp
		name = id
		if err := s.call(ctx, "users.info", url.Values{"user": {id}}, &out); err == nil {
			name = out.displayName(id)
		}
		s.mu.Lock()
		s.userName[id] = name
		s.mu.Unlock()
		return name
	}
}

// oldest renders the Slack `oldest` ts cutoff from a per-call sinceDays (> 0) or
// the configured default; "" when no window applies.
func (s *server) oldest(sinceDays int) string {
	days := sinceDays
	if days <= 0 {
		days = s.cfg.LookbackDays
	}
	if days <= 0 {
		return ""
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	return strconv.FormatInt(cutoff.Unix(), 10)
}

// call performs a GET against a Slack Web API method and decodes the JSON
// envelope into out. It returns an error for transport failures and for Slack's
// {"ok": false, "error": "..."} responses. The token rides in the Authorization
// header (never the URL), so errors can be surfaced without leaking it.
// call performs a Slack API request, waiting out rate limits itself. A 429 is
// never returned to the agent while retries remain: left to the model it
// improvises its own sleep-and-retry with run_bash, which burns tool-call rounds
// against the cap for something the connector can handle.
func (s *server) call(ctx context.Context, method string, params url.Values, out apiResponse) error {
	for attempt := 0; ; attempt++ {
		wait, err := s.callOnce(ctx, method, params, out)
		if wait <= 0 || attempt >= maxRateLimitRetries {
			return err // not rate-limited, or out of patience: surface it
		}
		fmt.Fprintf(os.Stderr, "mcp-slack: %s rate limited, waiting %s (attempt %d/%d)\n",
			method, wait, attempt+1, maxRateLimitRetries)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// callOnce performs one request. It returns a positive delay together with the
// error when Slack rate-limited the call, so call can wait and retry; every
// other outcome returns a zero delay.
func (s *server) callOnce(ctx context.Context, method string, params url.Values, out apiResponse) (time.Duration, error) {
	endpoint := s.base + method
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.Token)

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s: request failed: %v", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		wait := retryAfter(resp.Header.Get("Retry-After"))
		return wait, fmt.Errorf("%s: rate limited (retry after %s)", method, wait)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return 0, fmt.Errorf("%s: read failed: %v", method, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return 0, fmt.Errorf("%s: decode failed: %v", method, err)
	}
	if !out.isOK() {
		return 0, fmt.Errorf("%s: %s", method, out.errMessage())
	}
	return 0, nil
}

// retryAfter parses Slack's Retry-After header (whole seconds), falling back to
// a default when it's missing or unparseable and clamping it so one hostile or
// mistaken value can't park a scheduled run. Always positive, so callers can use
// it as the "this was a 429" signal.
func retryAfter(header string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || n <= 0 {
		return defaultRetryAfter
	}
	if d := time.Duration(n) * time.Second; d < maxRetryAfter {
		return d
	}
	return maxRetryAfter
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
