package mcpcal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ical "github.com/emersion/go-ical"
)

const sampleICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
X-WR-CALNAME:Test Cal
BEGIN:VEVENT
UID:single@test
DTSTART:20260620T100000Z
DTEND:20260620T110000Z
SUMMARY:Single Meeting
LOCATION:Room A
END:VEVENT
BEGIN:VEVENT
UID:weekly@test
DTSTART:20260615T090000Z
DTEND:20260615T093000Z
RRULE:FREQ=WEEKLY;COUNT=4
SUMMARY:Weekly Standup
END:VEVENT
BEGIN:VEVENT
UID:allday@test
DTSTART;VALUE=DATE:20260618
DTEND;VALUE=DATE:20260619
SUMMARY:Surgery
END:VEVENT
END:VCALENDAR
`

func decodeCal(t *testing.T, raw string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(raw)).Decode()
	if err != nil {
		t.Fatalf("decode ICS: %v", err)
	}
	return cal
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestCollectInstancesExpandsRecurrenceAndFiltersWindow(t *testing.T) {
	cal := decodeCal(t, sampleICS)

	// Window: 2026-06-15 .. 2026-06-23 (exclusive end). Expect, in order:
	// 06-15 standup, 06-18 Surgery (all-day), 06-20 single, 06-22 standup.
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 15), day(2026, 6, 23), time.UTC, nil)

	if len(insts) != 4 {
		t.Fatalf("want 4 instances, got %d: %+v", len(insts), insts)
	}
	wantStarts := []time.Time{
		time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC),
	}
	for i, want := range wantStarts {
		if !insts[i].Start.Equal(want) {
			t.Errorf("instance %d start = %v, want %v", i, insts[i].Start, want)
		}
		if insts[i].Calendar != "Test Cal" {
			t.Errorf("calendar name not propagated: %q", insts[i].Calendar)
		}
	}
	standups := 0
	for _, e := range insts {
		if e.Summary == "Weekly Standup" {
			standups++
		}
	}
	if standups != 2 {
		t.Errorf("want 2 expanded standups, got %d", standups)
	}
}

func TestCollectInstancesEmptyWindow(t *testing.T) {
	cal := decodeCal(t, sampleICS)
	// 06-16 .. 06-18: no single (06-20), no standup (06-15 before, 06-22 after),
	// no Surgery (06-18 is excluded by the end boundary).
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 16), day(2026, 6, 18), time.UTC, nil)
	if len(insts) != 0 {
		t.Fatalf("want 0 instances, got %d: %+v", len(insts), insts)
	}
}

func TestAllDayRendering(t *testing.T) {
	cal := decodeCal(t, sampleICS)
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 18), day(2026, 6, 19), time.UTC, nil)
	if len(insts) != 1 || !insts[0].AllDay {
		t.Fatalf("want 1 all-day instance, got %+v", insts)
	}
	out := formatInstances(insts, time.UTC)
	if !strings.Contains(out, "all-day") || !strings.Contains(out, "Surgery") {
		t.Errorf("all-day not rendered: %q", out)
	}
	if strings.Contains(out, "00:00") {
		t.Errorf("all-day event should not show 00:00 times: %q", out)
	}
}

func TestEventEndDuration(t *testing.T) {
	cal := decodeCal(t, sampleICS)
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 20), day(2026, 6, 21), time.UTC, nil)
	if len(insts) != 1 {
		t.Fatalf("want 1, got %d", len(insts))
	}
	if got := insts[0].End.Sub(insts[0].Start); got != time.Hour {
		t.Errorf("duration = %v, want 1h", got)
	}
}

func TestFormatInstances(t *testing.T) {
	insts := []eventInstance{{
		Start:    time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC),
		End:      time.Date(2026, 6, 20, 11, 0, 0, 0, time.UTC),
		Summary:  "Single Meeting",
		Location: "Room A",
		Calendar: "Test Cal",
	}}
	out := formatInstances(insts, time.UTC)
	for _, want := range []string{"2026-06-20 10:00", "11:00", "Single Meeting", "Room A", "Test Cal"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted output missing %q: %s", want, out)
		}
	}
	if formatInstances(nil, time.UTC) != "(no events)" {
		t.Error("empty should render (no events)")
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("AGENTBOX_ICS_URLS", "")
	t.Setenv("AGENTBOX_TIMEZONE", "")
	if _, ok := LoadConfig(); ok {
		t.Error("should be unconfigured with no URLs")
	}

	t.Setenv("AGENTBOX_ICS_URLS", "https://a.example/cal.ics, https://b.example/cal.ics")
	t.Setenv("AGENTBOX_TIMEZONE", "America/New_York")
	cfg, ok := LoadConfig()
	if !ok {
		t.Fatal("should be configured")
	}
	if len(cfg.URLs) != 2 {
		t.Errorf("want 2 URLs, got %d: %v", len(cfg.URLs), cfg.URLs)
	}
	if cfg.Loc.String() != "America/New_York" {
		t.Errorf("timezone = %q", cfg.Loc.String())
	}
}

func TestLoadConfigUserEmails(t *testing.T) {
	t.Setenv("AGENTBOX_ICS_URLS", "https://a.example/cal.ics")
	t.Setenv("AGENTBOX_IMAP_USER", "fallback@example.com")

	// Explicit AGENTBOX_CAL_EMAIL wins and is normalized (lowercase, multi-value).
	t.Setenv("AGENTBOX_CAL_EMAIL", "Me@Example.com, alias@example.com")
	cfg, _ := LoadConfig()
	if !cfg.UserEmails["me@example.com"] || !cfg.UserEmails["alias@example.com"] {
		t.Errorf("CAL_EMAIL not parsed/normalized: %v", cfg.UserEmails)
	}
	if cfg.UserEmails["fallback@example.com"] {
		t.Error("CAL_EMAIL should take precedence over IMAP_USER")
	}

	// Falls back to the IMAP user when CAL_EMAIL is unset.
	t.Setenv("AGENTBOX_CAL_EMAIL", "")
	cfg, _ = LoadConfig()
	if !cfg.UserEmails["fallback@example.com"] {
		t.Errorf("should fall back to IMAP_USER: %v", cfg.UserEmails)
	}
}

const rsvpICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:invite@test
DTSTART:20260620T100000Z
DTEND:20260620T110000Z
SUMMARY:Team Sync
ORGANIZER;CN=Boss:mailto:boss@example.com
ATTENDEE;CN=Boss;PARTSTAT=ACCEPTED:mailto:boss@example.com
ATTENDEE;CN=Me;PARTSTAT=NEEDS-ACTION:mailto:me@example.com
END:VEVENT
END:VCALENDAR
`

func TestParticipationStatus(t *testing.T) {
	cal := decodeCal(t, rsvpICS)
	ev := cal.Events()[0]

	me := map[string]bool{"me@example.com": true}
	if got := participationStatus(ev, me); got != "needs-action" {
		t.Errorf("my status = %q, want needs-action", got)
	}
	// No configured email -> unknown (preserves prior behavior).
	if got := participationStatus(ev, nil); got != "" {
		t.Errorf("status with no email = %q, want empty", got)
	}
	// An address not on the invite -> unknown, not someone else's status.
	if got := participationStatus(ev, map[string]bool{"stranger@example.com": true}); got != "" {
		t.Errorf("non-attendee status = %q, want empty", got)
	}
}

func TestCollectInstancesSurfacesRSVP(t *testing.T) {
	cal := decodeCal(t, rsvpICS)
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 20), day(2026, 6, 21), time.UTC, map[string]bool{"me@example.com": true})
	if len(insts) != 1 {
		t.Fatalf("want 1 instance, got %d", len(insts))
	}
	if insts[0].RSVP != "needs-action" {
		t.Errorf("RSVP = %q, want needs-action", insts[0].RSVP)
	}
	out := formatInstances(insts, time.UTC)
	if !strings.Contains(out, "not yet accepted") {
		t.Errorf("unconfirmed event should be flagged: %s", out)
	}
}

func TestRSVPNote(t *testing.T) {
	cases := map[string]string{
		"tentative":    "tentative",
		"needs-action": "not yet accepted",
		"accepted":     "", // confirmed -> no annotation
		"declined":     "", // dropped upstream, so no note even if it reached here
		"":             "", // unknown -> no annotation
	}
	for status, want := range cases {
		if got := rsvpNote(status); got != want {
			t.Errorf("rsvpNote(%q) = %q, want %q", status, got, want)
		}
	}
}

const declinedICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//test//EN
BEGIN:VEVENT
UID:declined@test
DTSTART:20260620T100000Z
DTEND:20260620T110000Z
SUMMARY:Meeting I Declined
ATTENDEE;CN=Me;PARTSTAT=DECLINED:mailto:me@example.com
END:VEVENT
BEGIN:VEVENT
UID:accepted@test
DTSTART:20260620T140000Z
DTEND:20260620T150000Z
SUMMARY:Meeting I Accepted
ATTENDEE;CN=Me;PARTSTAT=ACCEPTED:mailto:me@example.com
END:VEVENT
END:VCALENDAR
`

func TestCollectInstancesDropsDeclined(t *testing.T) {
	cal := decodeCal(t, declinedICS)
	me := map[string]bool{"me@example.com": true}
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 20), day(2026, 6, 21), time.UTC, me)

	if len(insts) != 1 {
		t.Fatalf("want 1 instance (declined dropped), got %d", len(insts))
	}
	if insts[0].Summary != "Meeting I Accepted" {
		t.Errorf("kept the wrong event: %q", insts[0].Summary)
	}
	if strings.Contains(formatInstances(insts, time.UTC), "Declined") {
		t.Error("declined event leaked into the listing")
	}

	// Without a configured email we can't detect the decline, so both remain
	// (we never hide what we can't attribute to the user).
	if got := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 20), day(2026, 6, 21), time.UTC, nil); len(got) != 2 {
		t.Errorf("want 2 instances when email is unconfigured, got %d", len(got))
	}
}

func TestDaysAndLimitClamps(t *testing.T) {
	if daysOr(0, 7) != 7 || daysOr(-1, 7) != 7 || daysOr(3, 7) != 3 || daysOr(99999, 7) != maxDays {
		t.Error("daysOr clamping wrong")
	}
	if limitOr(0) != maxEvents || limitOr(5) != 5 || limitOr(99999) != maxEvents {
		t.Error("limitOr clamping wrong")
	}
}

func TestTransportCauseStripsSecretURL(t *testing.T) {
	secret := "private-2ac72d4be3bdfe0315550b677cf89231"
	ue := &url.Error{
		Op:  "Get",
		URL: "https://calendar.google.com/calendar/ical/x/" + secret + "/basic.ics",
		Err: errors.New("x509: certificate signed by unknown authority"),
	}
	got := transportCause(ue).Error()
	if strings.Contains(got, secret) {
		t.Errorf("cause leaked secret token: %q", got)
	}
	if !strings.Contains(got, "x509") {
		t.Errorf("cause lost the useful detail: %q", got)
	}
	// Non-url errors pass through unchanged.
	plain := errors.New("boom")
	if transportCause(plain) != plain {
		t.Error("plain error should pass through unchanged")
	}
}

func TestFetchTimeoutEnv(t *testing.T) {
	t.Setenv("AGENTBOX_CAL_TIMEOUT", "")
	if got := fetchTimeout(); got != defaultFetchTimeout {
		t.Errorf("unset = %v, want default %v", got, defaultFetchTimeout)
	}
	t.Setenv("AGENTBOX_CAL_TIMEOUT", "120")
	if got := fetchTimeout(); got != 120*time.Second {
		t.Errorf("env 120 = %v, want 120s", got)
	}
	for _, bad := range []string{"junk", "0", "-5"} {
		t.Setenv("AGENTBOX_CAL_TIMEOUT", bad)
		if got := fetchTimeout(); got != defaultFetchTimeout {
			t.Errorf("env %q = %v, want default", bad, got)
		}
	}
}

func TestCalendarsCachesWithinTTL(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nEND:VCALENDAR\r\n")
	}))
	defer srv.Close()

	s := &server{cfg: Config{URLs: []string{srv.URL}, Loc: time.UTC}, client: srv.Client()}
	for range 3 {
		if _, err := s.calendars(context.Background()); err != nil {
			t.Fatalf("calendars: %v", err)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("feed fetched %d times across 3 calls, want 1 (cached)", n)
	}
}

func TestFeedCacheRoundTrip(t *testing.T) {
	c := newFeedCache(t.TempDir())
	if _, ok := c.load("https://x/secret/cal.ics"); ok {
		t.Error("empty cache should miss")
	}
	e := cacheEntry{ETag: `"v1"`, LastModified: "Mon, 01 Jun 2026 00:00:00 GMT", FetchedAt: time.Now(), Body: "BODY"}
	if err := c.save("https://x/secret/cal.ics", e); err != nil {
		t.Fatal(err)
	}
	got, ok := c.load("https://x/secret/cal.ics")
	if !ok || got.Body != "BODY" || got.ETag != `"v1"` {
		t.Errorf("roundtrip = %+v ok=%v", got, ok)
	}
	// Disabled (empty dir) and nil are safe no-ops.
	var nc *feedCache
	if _, ok := nc.load("x"); ok {
		t.Error("nil cache should miss")
	}
	if err := nc.save("x", e); err != nil {
		t.Errorf("nil save: %v", err)
	}
}

func TestFetchFeedConditionalGET(t *testing.T) {
	t.Setenv("AGENTBOX_CAL_CACHE_TTL", "0") // always revalidate (no fast-path)
	const etag = `"v1"`
	var full, notModified int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			atomic.AddInt32(&notModified, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		atomic.AddInt32(&full, 1)
		w.Header().Set("ETag", etag)
		io.WriteString(w, sampleICS)
	}))
	defer srv.Close()

	s := &server{cfg: Config{URLs: []string{srv.URL}, Loc: time.UTC}, client: srv.Client(), cache: newFeedCache(t.TempDir())}
	b1, err := s.fetchFeed(context.Background(), srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := s.fetchFeed(context.Background(), srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Error("body changed after a 304 revalidation")
	}
	if full != 1 {
		t.Errorf("full downloads = %d, want 1", full)
	}
	if notModified != 1 {
		t.Errorf("304 revalidations = %d, want 1", notModified)
	}
}

func TestFetchFeedTTLFastPathSkipsNetwork(t *testing.T) {
	t.Setenv("AGENTBOX_CAL_CACHE_TTL", "3600") // long TTL: serve cached without revalidating
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, sampleICS)
	}))
	defer srv.Close()

	s := &server{cfg: Config{URLs: []string{srv.URL}, Loc: time.UTC}, client: srv.Client(), cache: newFeedCache(t.TempDir())}
	for range 3 {
		if _, err := s.fetchFeed(context.Background(), srv.URL, 0); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 1 {
		t.Errorf("network hits = %d, want 1 (TTL fast-path)", hits)
	}
}
