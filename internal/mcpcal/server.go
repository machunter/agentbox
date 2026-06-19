// Package mcpcal is a read-only calendar MCP server. It fetches one or more
// iCalendar (ICS) feeds — e.g. Google Calendar's "secret address in iCal
// format" — over plain HTTPS and exposes tools to list, inspect, and search
// events. agentbox launches it as a subprocess of itself (the "mcp-cal"
// subcommand) and connects with ADK's mcptoolset.
//
// ICS feeds are read-only and authenticated by a secret URL, so this needs no
// OAuth and no app password — consistent with the project's local, no-OAuth
// stance. Recurring events are expanded within the query window.
package mcpcal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultUpcomingDays = 7
	defaultSearchDays   = 30
	maxDays             = 366
	maxEvents           = 100
	fetchTimeout        = 20 * time.Second
)

// Config holds the calendar feeds and the timezone used to interpret day
// boundaries and floating event times.
type Config struct {
	URLs []string
	Loc  *time.Location
}

// LoadConfig reads ICS feed URLs (AGENTBOX_ICS_URLS, comma/space separated) and
// an optional timezone (AGENTBOX_TIMEZONE, default UTC). The second return value
// is false when no feeds are configured.
func LoadConfig() (Config, bool) {
	var urls []string
	for _, u := range strings.FieldsFunc(os.Getenv("AGENTBOX_ICS_URLS"), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	}) {
		if u != "" {
			urls = append(urls, u)
		}
	}
	loc := time.UTC
	if tz := os.Getenv("AGENTBOX_TIMEZONE"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	return Config{URLs: urls, Loc: loc}, len(urls) > 0
}

// Configured reports whether any calendar feed is set up.
func Configured() bool {
	_, ok := LoadConfig()
	return ok
}

type server struct {
	cfg    Config
	client *http.Client
}

// Serve runs the calendar MCP server over stdio until the context is cancelled.
func Serve(ctx context.Context) error {
	cfg, ok := LoadConfig()
	if !ok {
		return fmt.Errorf("calendar not configured (set AGENTBOX_ICS_URLS)")
	}
	s := &server{cfg: cfg, client: &http.Client{Timeout: fetchTimeout}}
	srv := mcp.NewServer(&mcp.Implementation{Name: "agentbox-calendar", Version: "0.1.0"}, nil)
	s.registerTools(srv)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// CheckConnection is a diagnostic: it fetches the feeds and lists the next
// week's events, used by the "cal-check" subcommand to verify setup without the
// MCP/agent layers.
func CheckConnection(ctx context.Context) (string, error) {
	cfg, ok := LoadConfig()
	if !ok {
		return "", fmt.Errorf("calendar not configured (set AGENTBOX_ICS_URLS)")
	}
	s := &server{cfg: cfg, client: &http.Client{Timeout: fetchTimeout}}
	return s.upcoming(ctx, defaultUpcomingDays, maxEvents)
}

// --- tool inputs ---

type upcomingInput struct {
	Days  int `json:"days" jsonschema:"how many days ahead to look (default 7)"`
	Limit int `json:"limit" jsonschema:"max events to return (default 100)"`
}

type dayInput struct {
	Date string `json:"date" jsonschema:"the day to list, as YYYY-MM-DD"`
}

type searchInput struct {
	Query string `json:"query" jsonschema:"text to match against event title and location"`
	Days  int    `json:"days" jsonschema:"how many days ahead to search (default 30)"`
}

func (s *server) registerTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_upcoming_events",
		Description: "List upcoming calendar events over the next N days (chronological), with start/end, title, and location.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in upcomingInput) (*mcp.CallToolResult, any, error) {
		out, err := s.upcoming(ctx, daysOr(in.Days, defaultUpcomingDays), limitOr(in.Limit))
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "events_on_day",
		Description: "List calendar events on a specific day (YYYY-MM-DD).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dayInput) (*mcp.CallToolResult, any, error) {
		out, err := s.onDay(ctx, in.Date)
		return textResult(out, err), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_events",
		Description: "Search upcoming events whose title or location matches a query.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
		out, err := s.search(ctx, in.Query, daysOr(in.Days, defaultSearchDays))
		return textResult(out, err), nil, nil
	})
}

// --- operations ---

func (s *server) upcoming(ctx context.Context, days, limit int) (string, error) {
	now := time.Now().In(s.cfg.Loc)
	insts, err := s.instances(ctx, now, now.AddDate(0, 0, days))
	if err != nil {
		return "", err
	}
	return formatInstances(capInstances(insts, limit), s.cfg.Loc), nil
}

func (s *server) onDay(ctx context.Context, date string) (string, error) {
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), s.cfg.Loc)
	if err != nil {
		return "", fmt.Errorf("invalid date %q (want YYYY-MM-DD)", date)
	}
	insts, err := s.instances(ctx, day, day.AddDate(0, 0, 1))
	if err != nil {
		return "", err
	}
	return formatInstances(insts, s.cfg.Loc), nil
}

func (s *server) search(ctx context.Context, query string, days int) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is empty")
	}
	now := time.Now().In(s.cfg.Loc)
	insts, err := s.instances(ctx, now, now.AddDate(0, 0, days))
	if err != nil {
		return "", err
	}
	needle := strings.ToLower(query)
	var matched []eventInstance
	for _, e := range insts {
		if strings.Contains(strings.ToLower(e.Summary), needle) || strings.Contains(strings.ToLower(e.Location), needle) {
			matched = append(matched, e)
		}
	}
	if len(matched) == 0 {
		return fmt.Sprintf("no events matching %q in the next %d days", query, days), nil
	}
	return formatInstances(capInstances(matched, maxEvents), s.cfg.Loc), nil
}

// instances fetches all feeds and returns event occurrences within the window.
func (s *server) instances(ctx context.Context, winStart, winEnd time.Time) ([]eventInstance, error) {
	cals, err := s.fetchAll(ctx)
	if err != nil {
		return nil, err
	}
	return collectInstances(cals, winStart, winEnd, s.cfg.Loc), nil
}

// transportCause unwraps a *url.Error to its underlying cause, which omits the
// request URL (and thus the secret feed token). Other errors pass through. This
// lets fetch errors be surfaced for diagnosis without leaking the secret.
func transportCause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (s *server) fetchAll(ctx context.Context) ([]*ical.Calendar, error) {
	cals := make([]*ical.Calendar, 0, len(s.cfg.URLs))
	for i, u := range s.cfg.URLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("calendar %d: %w", i+1, err)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			// Surface the transport cause (DNS, TLS, timeout) for diagnosis, but
			// strip the URL — it carries the secret feed token.
			return nil, fmt.Errorf("calendar %d: fetch failed: %v", i+1, transportCause(err))
		}
		cal, derr := func() (*ical.Calendar, error) {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("calendar %d: status %d", i+1, resp.StatusCode)
			}
			return ical.NewDecoder(resp.Body).Decode()
		}()
		if derr != nil {
			return nil, derr
		}
		cals = append(cals, cal)
	}
	return cals, nil
}

// --- pure helpers (independently testable) ---

type eventInstance struct {
	Start, End time.Time
	AllDay     bool
	Summary    string
	Location   string
	Calendar   string
}

// collectInstances expands each calendar's events (including recurrences) into
// concrete occurrences within [winStart, winEnd), sorted by start time.
func collectInstances(cals []*ical.Calendar, winStart, winEnd time.Time, loc *time.Location) []eventInstance {
	var out []eventInstance
	for _, cal := range cals {
		name := calendarName(cal)
		for _, ev := range cal.Events() {
			summary, _ := ev.Props.Text(ical.PropSummary)
			if summary == "" {
				summary = "(no title)"
			}
			location, _ := ev.Props.Text(ical.PropLocation)

			start, err := ev.DateTimeStart(loc)
			if err != nil {
				continue
			}
			// All-day events use a DATE (not DATE-TIME) DTSTART.
			startProp := ev.Props.Get(ical.PropDateTimeStart)
			allDay := startProp != nil && startProp.ValueType() == ical.ValueDate

			var dur time.Duration
			if end, eerr := ev.DateTimeEnd(loc); eerr == nil && end.After(start) {
				dur = end.Sub(start)
			}
			for _, occ := range occurrences(ev, start, winStart, winEnd, loc) {
				out = append(out, eventInstance{
					Start:    occ,
					End:      occ.Add(dur),
					AllDay:   allDay,
					Summary:  summary,
					Location: location,
					Calendar: name,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// occurrences returns the start times of an event within the window: expanded
// from its recurrence rule if it has one, otherwise the single start if in range.
func occurrences(ev ical.Event, start, winStart, winEnd time.Time, loc *time.Location) []time.Time {
	if roption, _ := ev.Props.RecurrenceRule(); roption != nil {
		if set, err := ev.RecurrenceSet(loc); err == nil && set != nil {
			return set.Between(winStart, winEnd, true)
		}
	}
	if !start.Before(winStart) && start.Before(winEnd) {
		return []time.Time{start}
	}
	return nil
}

// calendarName returns a calendar's display name (X-WR-CALNAME) if present.
func calendarName(cal *ical.Calendar) string {
	if name, err := cal.Props.Text("X-WR-CALNAME"); err == nil && name != "" {
		return name
	}
	return ""
}

func formatInstances(insts []eventInstance, loc *time.Location) string {
	if len(insts) == 0 {
		return "(no events)"
	}
	var b strings.Builder
	for _, e := range insts {
		if e.AllDay {
			fmt.Fprintf(&b, "%s all-day", e.Start.In(loc).Format("Mon 2006-01-02"))
		} else {
			fmt.Fprintf(&b, "%s", e.Start.In(loc).Format("Mon 2006-01-02 15:04"))
			if e.End.After(e.Start) {
				fmt.Fprintf(&b, "–%s", e.End.In(loc).Format("15:04"))
			}
		}
		fmt.Fprintf(&b, "\t%s", e.Summary)
		if e.Location != "" {
			fmt.Fprintf(&b, " @ %s", e.Location)
		}
		if e.Calendar != "" {
			fmt.Fprintf(&b, " [%s]", e.Calendar)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func capInstances(insts []eventInstance, limit int) []eventInstance {
	if limit > 0 && len(insts) > limit {
		return insts[:limit]
	}
	return insts
}

func daysOr(n, def int) int {
	if n <= 0 {
		return def
	}
	if n > maxDays {
		return maxDays
	}
	return n
}

func limitOr(n int) int {
	if n <= 0 || n > maxEvents {
		return maxEvents
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
