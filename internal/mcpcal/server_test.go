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
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 15), day(2026, 6, 23), time.UTC)

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
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 16), day(2026, 6, 18), time.UTC)
	if len(insts) != 0 {
		t.Fatalf("want 0 instances, got %d: %+v", len(insts), insts)
	}
}

func TestAllDayRendering(t *testing.T) {
	cal := decodeCal(t, sampleICS)
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 18), day(2026, 6, 19), time.UTC)
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
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 20), day(2026, 6, 21), time.UTC)
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
