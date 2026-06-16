package mcpmail

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// crlf joins lines with CRLF, as real RFC 822 messages use.
func crlf(lines ...string) string { return strings.Join(lines, "\r\n") }

func TestParseMessageMultipart(t *testing.T) {
	raw := crlf(
		"From: Alice <alice@example.com>",
		"To: Bob <bob@example.com>",
		"Subject: Project Bluefin status",
		"Date: Mon, 02 Jan 2006 15:04:05 -0700",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=BOUNDARY",
		"",
		"--BOUNDARY",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"The deploy region is eu-west-3.",
		"--BOUNDARY",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>The deploy region is <b>eu-west-3</b>.</p>",
		"--BOUNDARY--",
		"",
	)

	pm, err := parseMessage([]byte(raw))
	if err != nil {
		t.Fatalf("parseMessage: %v", err)
	}
	if !strings.Contains(pm.From, "alice@example.com") {
		t.Errorf("From = %q", pm.From)
	}
	if pm.Subject != "Project Bluefin status" {
		t.Errorf("Subject = %q", pm.Subject)
	}
	if !strings.Contains(pm.Body, "eu-west-3") {
		t.Errorf("body missing text content: %q", pm.Body)
	}
	if strings.Contains(pm.Body, "<p>") || strings.Contains(pm.Body, "<b>") {
		t.Errorf("body should be the text/plain part, not HTML: %q", pm.Body)
	}
}

func TestParseMessageSimple(t *testing.T) {
	raw := crlf(
		"From: x@y.com",
		"Subject: hi",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"hello world",
		"",
	)
	pm, err := parseMessage([]byte(raw))
	if err != nil {
		t.Fatalf("parseMessage: %v", err)
	}
	if strings.TrimSpace(pm.Body) != "hello world" {
		t.Errorf("Body = %q", pm.Body)
	}
}

func TestParseMessageEncodedSubject(t *testing.T) {
	// RFC 2047 encoded-word should be decoded by Header.Text.
	raw := crlf(
		"From: x@y.com",
		"Subject: =?utf-8?q?caf=C3=A9_meeting?=",
		"Content-Type: text/plain",
		"",
		"body",
		"",
	)
	pm, err := parseMessage([]byte(raw))
	if err != nil {
		t.Fatalf("parseMessage: %v", err)
	}
	if pm.Subject != "café meeting" {
		t.Errorf("decoded Subject = %q, want %q", pm.Subject, "café meeting")
	}
}

func TestFormatAddresses(t *testing.T) {
	got := formatAddresses([]imap.Address{
		{Name: "Alice", Mailbox: "alice", Host: "example.com"},
		{Mailbox: "bob", Host: "example.com"},
	})
	if !strings.Contains(got, "Alice <alice@example.com>") || !strings.Contains(got, "bob@example.com") {
		t.Errorf("formatAddresses = %q", got)
	}
	if got := formatAddresses(nil); got != "(unknown)" {
		t.Errorf("empty addresses = %q, want (unknown)", got)
	}
}

func TestClampLimitAndMailbox(t *testing.T) {
	cases := []struct{ in, want int }{{0, defaultLimit}, {-5, defaultLimit}, {5, 5}, {999, maxLimit}}
	for _, c := range cases {
		if got := clampLimit(c.in); got != c.want {
			t.Errorf("clampLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	if mailboxOr("") != "INBOX" {
		t.Error("empty mailbox should default to INBOX")
	}
	if mailboxOr("Sent") != "Sent" {
		t.Error("explicit mailbox should pass through")
	}
}

func TestEffectiveSince(t *testing.T) {
	// No config default, no tool override -> no filter.
	s := &server{cfg: Config{}}
	if !s.effectiveSince(0).IsZero() {
		t.Error("expected zero time when neither config nor tool sets a window")
	}
	// Tool override applies.
	if got := daysSince(s.effectiveSince(7)); got != 7 {
		t.Errorf("tool since_days=7 -> daysSince=%d, want 7", got)
	}
	// Config default applies when tool param is 0.
	s = &server{cfg: Config{SinceDays: 5}}
	if got := daysSince(s.effectiveSince(0)); got != 5 {
		t.Errorf("config default -> daysSince=%d, want 5", got)
	}
	// Tool param wins over config default.
	if got := daysSince(s.effectiveSince(3)); got != 3 {
		t.Errorf("tool override -> daysSince=%d, want 3", got)
	}
}

func TestLoadConfigSinceDays(t *testing.T) {
	t.Setenv("AGENTBOX_IMAP_HOST", "imap.example.com")
	t.Setenv("AGENTBOX_IMAP_USER", "me@example.com")
	t.Setenv("AGENTBOX_IMAP_PASS", "pw")

	t.Setenv("AGENTBOX_EMAIL_SINCE_DAYS", "14")
	if cfg, _ := LoadConfig(); cfg.SinceDays != 14 {
		t.Errorf("SinceDays = %d, want 14", cfg.SinceDays)
	}
	// Unset / invalid / non-positive -> 0 (no filter).
	for _, v := range []string{"", "abc", "0", "-3"} {
		t.Setenv("AGENTBOX_EMAIL_SINCE_DAYS", v)
		if cfg, _ := LoadConfig(); cfg.SinceDays != 0 {
			t.Errorf("SinceDays for %q = %d, want 0", v, cfg.SinceDays)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	for _, k := range []string{"AGENTBOX_IMAP_HOST", "AGENTBOX_IMAP_PORT", "AGENTBOX_IMAP_USER", "AGENTBOX_IMAP_PASS"} {
		t.Setenv(k, "")
	}
	if _, ok := LoadConfig(); ok {
		t.Error("should be unconfigured when env is empty")
	}

	t.Setenv("AGENTBOX_IMAP_HOST", "imap.example.com")
	t.Setenv("AGENTBOX_IMAP_USER", "me@example.com")
	t.Setenv("AGENTBOX_IMAP_PASS", "app-password")
	cfg, ok := LoadConfig()
	if !ok {
		t.Fatal("should be configured")
	}
	if cfg.Port != "993" {
		t.Errorf("default port = %q, want 993", cfg.Port)
	}
}
