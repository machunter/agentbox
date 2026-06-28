package mailer

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	t.Setenv("AGENTBOX_SMTP_HOST", "")
	if _, ok := LoadConfig(); ok {
		t.Error("no SMTP host -> not configured")
	}

	t.Setenv("AGENTBOX_SMTP_HOST", "smtp.gmail.com")
	t.Setenv("AGENTBOX_IMAP_USER", "me@example.com")
	t.Setenv("AGENTBOX_IMAP_PASS", "app-pw")
	t.Setenv("AGENTBOX_SMTP_PORT", "")
	t.Setenv("AGENTBOX_DELIVER_EMAIL", "")
	c, ok := LoadConfig()
	if !ok {
		t.Fatal("should be configured with host + IMAP user/pass")
	}
	if c.Port != "587" {
		t.Errorf("default port = %q, want 587", c.Port)
	}
	if c.From != "me@example.com" || c.To != "me@example.com" {
		t.Errorf("from/to should default to the user: %q / %q", c.From, c.To)
	}

	t.Setenv("AGENTBOX_DELIVER_EMAIL", "personal@example.com")
	if c, _ := LoadConfig(); c.To != "personal@example.com" {
		t.Errorf("AGENTBOX_DELIVER_EMAIL should override To: %q", c.To)
	}

	// Missing password -> not configured.
	t.Setenv("AGENTBOX_IMAP_PASS", "")
	if _, ok := LoadConfig(); ok {
		t.Error("no password -> not configured")
	}
}

func TestBuildMessage(t *testing.T) {
	when := time.Date(2026, 6, 28, 8, 0, 0, 0, time.UTC)
	// Subject contains a newline (injection attempt) and body is multi-line.
	msg := string(buildMessage("me@x.com", "me@x.com", "agentbox: daily-briefing\nBcc: evil@x.com", "line1\nline2", when))

	if !strings.Contains(msg, "From: me@x.com\r\n") || !strings.Contains(msg, "To: me@x.com\r\n") {
		t.Error("missing From/To headers")
	}
	// Subject flattened to one line (no header injection).
	if !strings.Contains(msg, "Subject: agentbox: daily-briefing Bcc: evil@x.com\r\n") {
		t.Errorf("subject not flattened: %q", msg)
	}
	if !strings.Contains(msg, "Date: ") || !strings.Contains(msg, "Content-Type: text/plain") {
		t.Error("missing Date/Content-Type")
	}
	// Body CRLF-normalized after the blank line.
	if !strings.Contains(msg, "\r\n\r\nline1\r\nline2\r\n") {
		t.Errorf("body not CRLF after header break: %q", msg)
	}
}
