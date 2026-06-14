package mcpcal

import (
	"strings"
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

	// Window: 2026-06-15 .. 2026-06-23 (exclusive end).
	// Expect: weekly on 06-15 and 06-22, plus the single on 06-20. (06-29 out.)
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 15), day(2026, 6, 23), time.UTC)

	if len(insts) != 3 {
		t.Fatalf("want 3 instances, got %d: %+v", len(insts), insts)
	}
	// Sorted by start: 06-15 standup, 06-20 single, 06-22 standup.
	if !insts[0].Start.Equal(time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("first start = %v", insts[0].Start)
	}
	if insts[1].Summary != "Single Meeting" || insts[1].Location != "Room A" {
		t.Errorf("second instance = %+v", insts[1])
	}
	if !insts[2].Start.Equal(time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("third start = %v", insts[2].Start)
	}
	standups := 0
	for _, e := range insts {
		if e.Summary == "Weekly Standup" {
			standups++
		}
		if e.Calendar != "Test Cal" {
			t.Errorf("calendar name not propagated: %q", e.Calendar)
		}
	}
	if standups != 2 {
		t.Errorf("want 2 expanded standups, got %d", standups)
	}
}

func TestCollectInstancesEmptyWindow(t *testing.T) {
	cal := decodeCal(t, sampleICS)
	// 06-16 .. 06-19: no single (06-20), no standup (06-15 before, 06-22 after).
	insts := collectInstances([]*ical.Calendar{cal}, day(2026, 6, 16), day(2026, 6, 19), time.UTC)
	if len(insts) != 0 {
		t.Fatalf("want 0 instances, got %d: %+v", len(insts), insts)
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
